// milvus_adapter.go - Enterprise Vector Database Integration
package vectordb

import (
	"context"
	"crypto/tls"
	"fmt"
	"sync"
	"time"

	"github.com/milvus-io/milvus-sdk-go/v2/client"
	"github.com/milvus-io/milvus-sdk-go/v2/entity"
	"go.uber.org/zap"
	"golang.org/x/sync/semaphore"
)

const (
	maxRetryAttempts   = 5
	baseRetryDelay     = 500 * time.Millisecond
	maxConnPoolSize    = 20
	maxBulkInsertSize  = 5000
	queryTimeout       = 30 * time.Second
	healthCheckPeriod  = 1 * time.Minute
)

type MilvusConfig struct {
	Host              string
	Port              int
	Username          string
	Password          string
	TLSConfig         *tls.Config
	ConnectionTimeout time.Duration
	Namespace         string
}

type MilvusAdapter struct {
	client      client.Client
	config      MilvusConfig
	logger      *zap.Logger
	connPool    *semaphore.Weighted
	healthCheck chan struct{}
	metrics     *VectorDBMetrics
	mu          sync.RWMutex
}

type VectorDBMetrics struct {
	QueryDuration   prometheus.Histogram
	InsertDuration  prometheus.Histogram
	ErrorCount      prometheus.Counter
	ConnectionState prometheus.Gauge
}

func NewMilvusAdapter(cfg MilvusConfig, logger *zap.Logger) (*MilvusAdapter, error) {
	adapter := &MilvusAdapter{
		config:      cfg,
		logger:      logger.Named("milvus_adapter"),
		connPool:    semaphore.NewWeighted(maxConnPoolSize),
		healthCheck: make(chan struct{}, 1),
	}

	if err := adapter.connectWithRetry(); err != nil {
		return nil, fmt.Errorf("failed to initialize connection: %w", err)
	}

	go adapter.connectionMonitor()
	return adapter, nil
}

func (m *MilvusAdapter) connectWithRetry() error {
	var lastErr error
	for attempt := 1; attempt <= maxRetryAttempts; attempt++ {
		conn, err := client.NewGrpcClient(context.Background(), 
			fmt.Sprintf("%s:%d", m.config.Host, m.config.Port),
			client.WithUsername(m.config.Username),
			client.WithPassword(m.config.Password),
			client.WithTLSCfg(m.config.TLSConfig),
		)
		
		if err == nil {
			m.client = conn
			m.logger.Info("Successfully connected to Milvus cluster")
			return nil
		}
		
		lastErr = err
		delay := baseRetryDelay * time.Duration(attempt)
		m.logger.Warn("Connection attempt failed", 
			zap.Int("attempt", attempt),
			zap.Error(err),
			zap.Duration("retry_delay", delay),
		)
		time.Sleep(delay)
	}
	return fmt.Errorf("exhausted connection attempts: %w", lastErr)
}

func (m *MilvusAdapter) connectionMonitor() {
	ticker := time.NewTicker(healthCheckPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := m.healthCheckConnection(); err != nil {
				m.logger.Error("Connection health check failed", zap.Error(err))
				m.reconnect()
			}
		case <-m.healthCheck:
			return
		}
	}
}

func (m *MilvusAdapter) healthCheckConnection() error {
	ctx, cancel := context.WithTimeout(context.Background(), m.config.ConnectionTimeout)
	defer cancel()
	
	_, err := m.client.ListCollections(ctx)
	return err
}

func (m *MilvusAdapter) reconnect() {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	if err := m.client.Close(); err != nil {
		m.logger.Error("Error closing stale connection", zap.Error(err))
	}
	
	if err := m.connectWithRetry(); err != nil {
		m.logger.Error("Failed to re-establish connection", zap.Error(err))
	}
}

func (m *MilvusAdapter) CreateCollection(ctx context.Context, name string, dim int64) error {
	if err := m.connPool.Acquire(ctx, 1); err != nil {
		return err
	}
	defer m.connPool.Release(1)

	schema := &entity.Schema{
		CollectionName: name,
		Description:    "Nuzon AI Agent Memory",
		AutoID:         false,
		Fields: []*entity.Field{
			{
				Name:       "vector",
				DataType:   entity.FieldTypeFloatVector,
				TypeParams: map[string]string{"dim": fmt.Sprintf("%d", dim)},
			},
			{
				Name:       "metadata",
				DataType:   entity.FieldTypeJSON,
			},
		},
	}

	index := entity.NewGenericIndex("nuzon_agent_index", 
		entity.L2,
		[]string{"vector"},
		entity.WithIndexParam("nlist", "2048"),
		entity.WithIndexParam("m", "24"),
	)

	err := m.client.CreateCollection(ctx, schema, 2)
	if err != nil {
		return fmt.Errorf("failed to create collection: %w", err)
	}

	return m.client.CreateIndex(ctx, name, index)
}

func (m *MilvusAdapter) InsertVectors(ctx context.Context, collection string, vectors []float32, metadatas []map[string]interface{}) error {
	if len(vectors) == 0 || len(vectors) != len(metadatas) {
		return fmt.Errorf("invalid input dimensions")
	}

	batches := chunkSlice(vectors, maxBulkInsertSize)
	metaBatches := chunkSlice(metadatas, maxBulkInsertSize)

	var wg sync.WaitGroup
	errChan := make(chan error, len(batches))

	for i := range batches {
		wg.Add(1)
		go func(batchIndex int) {
			defer wg.Done()
			
			if err := m.connPool.Acquire(ctx, 1); err != nil {
				errChan <- err
				return
			}
			defer m.connPool.Release(1)

			start := time.Now()
			vectors := entity.NewColumnFloatVector("vector", int32(len(batches[batchIndex])/dim), batches[batchIndex])
			metadatas := entity.NewColumnJSONBytes("metadata", serializeMetadata(metaBatches[batchIndex]))
			
			_, err := m.client.Insert(ctx, collection, "", vectors, metadatas)
			m.metrics.InsertDuration.Observe(time.Since(start).Seconds())
			
			if err != nil {
				m.metrics.ErrorCount.Inc()
				errChan <- fmt.Errorf("batch %d insert failed: %w", batchIndex, err)
				return
			}
		}(i)
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		if err != nil {
			return err
		}
	}
	return nil
}

func (m *MilvusAdapter) SearchVectors(ctx context.Context, collection string, query []float32, k int) ([]SearchResult, error) {
	if err := m.connPool.Acquire(ctx, 1); err != nil {
		return nil, err
	}
	defer m.connPool.Release(1)

	start := time.Now()
	defer func() {
		m.metrics.QueryDuration.Observe(time.Since(start).Seconds())
	}()

	sp, err := entity.NewIndexFlatSearchParam()
	if err != nil {
		return nil, fmt.Errorf("failed to create search params: %w", err)
	}

	vectors := []entity.Vector{entity.FloatVector(query)}
	results, err := m.client.Search(
		ctx,
		collection,
		[]string{},
		"",
		[]string{"vector", "metadata"},
		vectors,
		"vector",
		entity.L2,
		k,
		sp,
	)

	if err != nil {
		m.metrics.ErrorCount.Inc()
		return nil, fmt.Errorf("search operation failed: %w", err)
	}

	var searchResults []SearchResult
	for _, result := range results {
		for _, score := range result.Scores {
			searchResults = append(searchResults, SearchResult{
				ID:       result.IDs.(*entity.ColumnInt64).Data()[0],
				Score:    score,
				Metadata: deserializeMetadata(result.Fields["metadata"].(*entity.ColumnJSONBytes).Data()[0]),
			})
		}
	}
	return searchResults, nil
}

func (m *MilvusAdapter) Close() error {
	close(m.healthCheck)
	return m.client.Close()
}

// Helper functions omitted for brevity: chunkSlice, serializeMetadata, deserializeMetadata

type SearchResult struct {
	ID       int64
	Score    float32
	Metadata map[string]interface{}
}

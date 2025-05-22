// core/memory/memgpt_adapter.go
package memory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/klauspost/compress/zstd"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/crypto/chacha20poly1305"
)

var (
	memOpsCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cirium_memory_operations_total",
			Help: "Total memory operations by type and status",
		},
		[]string{"operation", "status"},
	)

	memLatencyHist = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "cirium_memory_latency_seconds",
			Help:    "Memory operation latency distribution",
			Buckets: []float64{0.001, 0.01, 0.1, 0.5, 1, 5},
		},
		[]string{"operation"},
	)

	memSizeGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "cirium_memory_storage_bytes",
			Help: "Total encrypted memory storage usage",
		},
		[]string{"tenant"},
	)
)

func init() {
	prometheus.MustRegister(memOpsCounter, memLatencyHist, memSizeGauge)
}

// MemoryRecord represents an encrypted memory unit with versioning
type MemoryRecord struct {
	ID        string    `db:"id"`
	AgentID   string    `db:"agent_id"`
	Version   int       `db:"version"`
	Data      []byte    `db:"data"`
	Metadata  []byte    `db:"metadata"`
	CreatedAt time.Time `db:"created_at"`
	ExpiresAt time.Time `db:"expires_at"`
}

// MemoryConfig contains encryption and storage parameters
type MemoryConfig struct {
	PostgresDSN      string
	EncryptionKey    [32]byte
	CompressionLevel zstd.EncoderLevel
	CacheSize        int
}

// MemoryAdapter implements secure long-term memory storage
type MemoryAdapter struct {
	db        *sqlx.DB
	aead      *chacha20poly1305.Aead
	encoder   *zstd.Encoder
	decoder   *zstd.Decoder
	cache     *LRUCache
	config    MemoryConfig
}

// NewMemoryAdapter creates a new memory subsystem instance
func NewMemoryAdapter(ctx context.Context, cfg MemoryConfig) (*MemoryAdapter, error) {
	db, err := sqlx.ConnectContext(ctx, "postgres", cfg.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	aead, err := chacha20poly1305.New(cfg.EncryptionKey[:])
	if err != nil {
		return nil, fmt.Errorf("failed to initialize crypto: %w", err)
	}

	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(cfg.CompressionLevel))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize compressor: %w", err)
	}

	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize decompressor: %w", err)
	}

	return &MemoryAdapter{
		db:        db,
		aead:      aead,
		encoder:   encoder,
		decoder:   decoder,
		cache:     NewLRUCache(cfg.CacheSize),
		config:    cfg,
	}, nil
}

// StoreMemory persists encrypted memory with version control
func (m *MemoryAdapter) StoreMemory(ctx context.Context, agentID string, data any) (string, error) {
	start := time.Now()
	defer func() {
		memLatencyHist.WithLabelValues("store").Observe(time.Since(start).Seconds())
	}()

	plaintext, err := json.Marshal(data)
	if err != nil {
		memOpsCounter.WithLabelValues("store", "error").Inc()
		return "", fmt.Errorf("serialization failed: %w", err)
	}

	compressed := m.encoder.EncodeAll(plaintext, make([]byte, 0, len(plaintext)))
	
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		memOpsCounter.WithLabelValues("store", "error").Inc()
		return "", fmt.Errorf("nonce generation failed: %w", err)
	}

	encrypted := m.aead.Seal(nil, nonce, compressed, nil)
	record := MemoryRecord{
		ID:        generateUUID(),
		AgentID:   agentID,
		Version:   1,
		Data:      append(nonce, encrypted...),
		Metadata:  []byte(`{"source":"direct_input"}`),
		CreatedAt: time.Now().UTC(),
		ExpiresAt: time.Now().UTC().Add(720 * time.Hour),
	}

	tx, err := m.db.BeginTxx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		memOpsCounter.WithLabelValues("store", "error").Inc()
		return "", fmt.Errorf("transaction start failed: %w", err)
	}
	defer tx.Rollback()

	if err := tx.GetContext(ctx, &record.Version, 
		`SELECT COALESCE(MAX(version),0)+1 
		 FROM memories 
		 WHERE agent_id = \$1`, agentID); err != nil {
		memOpsCounter.WithLabelValues("store", "error").Inc()
		return "", fmt.Errorf("versioning failed: %w", err)
	}

	if _, err := tx.NamedExecContext(ctx, 
		`INSERT INTO memories 
		 (id, agent_id, version, data, metadata, created_at, expires_at)
		 VALUES 
		 (:id, :agent_id, :version, :data, :metadata, :created_at, :expires_at)`, 
		 record); err != nil {
		memOpsCounter.WithLabelValues("store", "error").Inc()
		return "", fmt.Errorf("insert failed: %w", err)
	}

	if err := tx.Commit(); err != nil {
		memOpsCounter.WithLabelValues("store", "error").Inc()
		return "", fmt.Errorf("commit failed: %w", err)
	}

	m.cache.Set(record.ID, record)
	memSizeGauge.WithLabelValues(record.AgentID).Add(float64(len(record.Data)))
	memOpsCounter.WithLabelValues("store", "success").Inc()
	return record.ID, nil
}

// RetrieveMemory fetches and decrypts memory records
func (m *MemoryAdapter) RetrieveMemory(ctx context.Context, agentID string, version int) ([]byte, error) {
	start := time.Now()
	defer func() {
		memLatencyHist.WithLabelValues("retrieve").Observe(time.Since(start).Seconds())
	}()

	var record MemoryRecord
	if cached, ok := m.cache.Get(agentID); ok {
		record = cached.(MemoryRecord)
	} else {
		err := m.db.GetContext(ctx, &record,
			`SELECT * FROM memories 
			 WHERE agent_id = \$1 AND version = \$2
			 ORDER BY created_at DESC 
			 LIMIT 1`, agentID, version)
		if err != nil {
			memOpsCounter.WithLabelValues("retrieve", "error").Inc()
			return nil, fmt.Errorf("query failed: %w", err)
		}
		m.cache.Set(record.ID, record)
	}

	nonceSize := m.aead.NonceSize()
	if len(record.Data) < nonceSize {
		memOpsCounter.WithLabelValues("retrieve", "error").Inc()
		return nil, fmt.Errorf("invalid ciphertext length")
	}

	nonce, ciphertext := record.Data[:nonceSize], record.Data[nonceSize:]
	compressed, err := m.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		memOpsCounter.WithLabelValues("retrieve", "error").Inc()
		return nil, fmt.Errorf("decryption failed: %w", err)
	}

	decompressed, err := m.decoder.DecodeAll(compressed, nil)
	if err != nil {
		memOpsCounter.WithLabelValues("retrieve", "error").Inc()
		return nil, fmt.Errorf("decompression failed: %w", err)
	}

	memOpsCounter.WithLabelValues("retrieve", "success").Inc()
	return decompressed, nil
}

// Required SQL schema (execute during initialization)
/*
CREATE TABLE IF NOT EXISTS memories (
    id          UUID PRIMARY KEY,
    agent_id    VARCHAR(255) NOT NULL,
    version     INTEGER NOT NULL,
    data        BYTEA NOT NULL,
    metadata    JSONB NOT NULL,
    created_at  TIMESTAMP WITH TIME ZONE NOT NULL,
    expires_at  TIMESTAMP WITH TIME ZONE NOT NULL
);

CREATE INDEX idx_agent_version ON memories (agent_id, version);
CREATE INDEX idx_expiration ON memories (expires_at);
*/

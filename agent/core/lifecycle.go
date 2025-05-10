// lifecycle.go - Enterprise State Management Engine
package state

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/coreos/etcd/clientv3"
	"go.uber.org/zap"
	"go.opentelemetry.io/otel"
)

// State represents the FSM of enterprise components
type State int

const (
	StateBooting State = iota
	StateConfiguring
	StateHealthy
	StateDegraded
	StateMaintenance
	StateTerminating
)

var stateStrings = map[State]string{
	StateBooting:     "BOOTING",
	StateConfiguring: "CONFIGURING",
	StateHealthy:     "HEALTHY",
	StateDegraded:    "DEGRADED",
	StateMaintenance: "MAINTENANCE",
	StateTerminating: "TERMINATING",
}

// LifecycleManager coordinates distributed state transitions
type LifecycleManager struct {
	mu            sync.RWMutex
	currentState  State
	previousState State
	stateTTL      time.Duration

	etcdClient   *clientv3.Client
	leaseID      clientv3.LeaseID
	shutdownChan chan struct{}

	logger     *zap.Logger
	tracer     trace.Tracer
	metrics    *stateMetrics
	cipherSuite *tls.CipherSuite
}

type StateTransition struct {
	From      State
	To        State
	Timestamp time.Time
	Reason    string
}

// NewLifecycleManager creates production-grade state handler
func NewLifecycleManager(etcdEndpoints []string, tlsConfig *tls.Config) (*LifecycleManager, error) {
	cli, err := clientv3.New(clientv3.Config{
		Endpoints:   etcdEndpoints,
		DialTimeout: 5 * time.Second,
		TLS:         tlsConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("etcd connection failed: %v", err)
	}

	return &LifecycleManager{
		etcdClient:   cli,
		stateTTL:     10 * time.Second,
		shutdownChan: make(chan struct{}),
		logger:       zap.NewExample(),
		tracer:       otel.Tracer("state"),
		metrics:      newStateMetrics(),
		cipherSuite: selectCipherSuite(tlsConfig),
	}, nil
}

// Start begins state synchronization and monitoring
func (lm *LifecycleManager) Start(ctx context.Context) error {
	ctx, span := lm.tracer.Start(ctx, "LifecycleManager.Start")
	defer span.End()

	if err := lm.acquireStateLock(ctx); err != nil {
		return fmt.Errorf("cluster leadership acquisition failed: %v", err)
	}

	go lm.stateHeartbeat()
	go lm.monitorStateConditions()
	return nil
}

// Transition performs atomic state changes with distributed consensus
func (lm *LifecycleManager) Transition(ctx context.Context, newState State, reason string) error {
	ctx, span := lm.tracer.Start(ctx, "LifecycleManager.Transition")
	defer span.End()

	lm.mu.Lock()
	defer lm.mu.Unlock()

	if !validTransition(lm.currentState, newState) {
		return fmt.Errorf("invalid state transition %s → %s", 
			stateStrings[lm.currentState], stateStrings[newState])
	}

	transition := StateTransition{
		From:      lm.currentState,
		To:        newState,
		Timestamp: time.Now().UTC(),
		Reason:    reason,
	}

	if err := lm.persistTransition(ctx, transition); err != nil {
		return fmt.Errorf("state persistence failed: %v", err)
	}

	lm.previousState = lm.currentState
	lm.currentState = newState
	lm.metrics.transitionsTotal.WithLabelValues(transition.String()).Inc()
	return nil
}

// Shutdown performs graceful termination sequence
func (lm *LifecycleManager) Shutdown(ctx context.Context) error {
	ctx, span := lm.tracer.Start(ctx, "LifecycleManager.Shutdown")
	defer span.End()

	close(lm.shutdownChan)
	
	if err := lm.Transition(ctx, StateTerminating, "System shutdown"); err != nil {
		return err
	}
	
	if lm.leaseID != 0 {
		if _, err := lm.etcdClient.Revoke(ctx, lm.leaseID); err != nil {
			return fmt.Errorf("lease revocation failed: %v", err)
		}
	}
	return lm.etcdClient.Close()
}

// Implementation Details

func (lm *LifecycleManager) acquireStateLock(ctx context.Context) error {
	resp, err := lm.etcdClient.Grant(ctx, int64(lm.stateTTL.Seconds()))
	if err != nil {
		return err
	}
	lm.leaseID = resp.ID

	_, err = lm.etcdClient.Put(ctx, "nuzon/state/leader", 
		lm.cipherSuite.String(), clientv3.WithLease(lm.leaseID))
	return err
}

func (lm *LifecycleManager) stateHeartbeat() {
	ticker := time.NewTicker(lm.stateTTL / 2)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			_, err := lm.etcdClient.KeepAliveOnce(context.Background(), lm.leaseID)
			if err != nil {
				lm.logger.Error("State lease renewal failed", zap.Error(err))
			}
		case <-lm.shutdownChan:
			return
		}
	}
}

func validTransition(from, to State) bool {
	transitionMatrix := map[State][]State{
		StateBooting:     {StateConfiguring, StateTerminating},
		StateConfiguring: {StateHealthy, StateDegraded},
		StateHealthy:     {StateDegraded, StateMaintenance},
		StateDegraded:    {StateHealthy, StateMaintenance},
		StateMaintenance: {StateHealthy, StateTerminating},
		StateTerminating: {},
	}
	for _, valid := range transitionMatrix[from] {
		if to == valid {
			return true
		}
	}
	return false
}

func selectCipherSuite(tlsConfig *tls.Config) *tls.CipherSuite {
	for _, cs := range tlsConfig.CipherSuites {
		switch cs {
		case tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384:
			return &cs
		}
	}
	return nil
}

// Production Deployment Configuration
/*
apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      containers:
      - name: statemgr
        image: nuzonai/state-manager:v2.7
        env:
        - name: ETCD_ENDPOINTS
          value: "etcd-cluster.nuzon.svc:2379"
        - name: STATE_TTL
          value: "15s"
        ports:
        - containerPort: 9090
          name: metrics
        readinessProbe:
          httpGet:
            path: /healthz
            port: metrics
*/ 

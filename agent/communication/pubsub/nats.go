// nats.go - Enterprise Event Streaming Core
package messaging

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

var (
	msgPublished = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nuzon_nats_messages_published_total",
		Help: "Total published messages",
	}, []string{"subject"})

	msgDelivered = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nuzon_nats_messages_delivered_total",
		Help: "Successfully delivered messages",
	}, []string{"subject"})

	msgFailed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "nuzon_nats_messages_failed_total",
		Help: "Failed message deliveries",
	}, []string{"subject", "error"})
)

type EnterpriseNATS struct {
	conn         *nats.Conn
	js           nats.JetStreamContext
	cfg          Config
	logger       *zap.Logger
	shutdownChan chan struct{}
}

type Config struct {
	URLs         []string
	TLSConfig    *tls.Config
	AuthMethod   string
	NKeySeed     string
	StreamConfig *nats.StreamConfig
	MaxReconnect int
}

func NewEnterpriseNATS(cfg Config, logger *zap.Logger) (*EnterpriseNATS, error) {
	opts := []nats.Option{
		nats.MaxReconnects(cfg.MaxReconnect),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(c *nats.Conn, err error) {
			logger.Warn("NATS connection lost", zap.Error(err))
		}),
		nats.ClosedHandler(func(c *nats.Conn) {
			logger.Fatal("NATS connection permanently closed")
		}),
	}

	if cfg.TLSConfig != nil {
		opts = append(opts, nats.Secure(cfg.TLSConfig))
	}

	switch cfg.AuthMethod {
	case "nkey":
		kp, err := nkeys.FromSeed([]byte(cfg.NKeySeed))
		if err != nil {
			return nil, fmt.Errorf("nkey auth failed: %w", err)
		}
		opts = append(opts, nats.NkeyFromKeyPair(kp))
	case "tls":
		opts = append(opts, nats.ClientCert("", ""))
	}

	conn, err := nats.Connect(strings.Join(cfg.URLs, ","), opts...)
	if err != nil {
		return nil, fmt.Errorf("connection failed: %w", err)
	}

	js, err := conn.JetStream(nats.PublishAsyncMaxPending(256))
	if err != nil {
		return nil, fmt.Errorf("jetstream init failed: %w", err)
	}

	en := &EnterpriseNATS{
		conn:         conn,
		js:           js,
		cfg:          cfg,
		logger:       logger,
		shutdownChan: make(chan struct{}),
	}

	if cfg.StreamConfig != nil {
		if err := en.ensureStream(); err != nil {
			return nil, err
		}
	}

	prometheus.MustRegister(msgPublished, msgDelivered, msgFailed)
	return en, nil
}

func (en *EnterpriseNATS) ensureStream() error {
	stream, err := en.js.StreamInfo(en.cfg.StreamConfig.Name)
	if err == nil {
		if !stream.Config.Equals(en.cfg.StreamConfig) {
			return fmt.Errorf("existing stream configuration mismatch")
		}
		return nil
	}

	_, err = en.js.AddStream(en.cfg.StreamConfig)
	return err
}

func (en *EnterpriseNATS) Publish(ctx context.Context, subject string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal failed: %w", err)
	}

	msgPublished.WithLabelValues(subject).Inc()

	ack, err := en.js.PublishAsync(subject, data)
	if err != nil {
		msgFailed.WithLabelValues(subject, "publish_error").Inc()
		return fmt.Errorf("publish failed: %w", err)
	}

	go en.trackAck(ack, subject)
	return nil
}

func (en *EnterpriseNATS) Subscribe(subject string, handler func([]byte) error) error {
	_, err := en.js.Subscribe(subject, func(msg *nats.Msg) {
		if err := handler(msg.Data); err != nil {
			msgFailed.WithLabelValues(subject, "handler_error").Inc()
			_ = msg.Nak()
			return
		}
		msgDelivered.WithLabelValues(subject).Inc()
		_ = msg.Ack()
	}, nats.ManualAck(), nats.MaxDeliver(5))
	
	return err
}

func (en *EnterpriseNATS) trackAck(ack nats.PubAckFuture, subject string) {
	select {
	case <-ack.Ok():
		msgDelivered.WithLabelValues(subject).Inc()
	case err := <-ack.Err():
		msgFailed.WithLabelValues(subject, "nack_error").Inc()
		en.logger.Error("Message rejected", 
			zap.String("subject", subject),
			zap.Error(err))
	case <-time.After(30 * time.Second):
		msgFailed.WithLabelValues(subject, "ack_timeout").Inc()
		en.logger.Error("Ack timeout",
			zap.String("subject", subject))
	}
}

func (en *EnterpriseNATS) Run() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	en.Shutdown()
}

func (en *EnterpriseNATS) Shutdown() {
	en.logger.Info("Initiating graceful shutdown")
	close(en.shutdownChan)

	if !en.conn.IsClosed() {
		if err := en.conn.Drain(); err != nil {
			en.logger.Error("Drain failed", zap.Error(err))
		}
	}

	en.conn.Close()
	prometheus.Unregister(msgPublished)
	prometheus.Unregister(msgDelivered)
	prometheus.Unregister(msgFailed)
}

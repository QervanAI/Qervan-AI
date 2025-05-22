package main

import (
	"context"
	"crypto/tls"
	"database/sql"
	"embed"
	_ "embed"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"cirium.ai/core/agent"
	"cirium.ai/core/auth"
	"cirium.ai/core/config"
	"cirium.ai/core/crypto/quantum"
	"cirium.ai/core/db"
	"cirium.ai/core/telemetry"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var (
	//go:embed config/*.yaml
	configFS embed.FS

	//go:embed migrations/*.sql
	migrationFS embed.FS
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Initialize structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))
	slog.SetDefault(logger)

	// Load quantum-safe root certificates
	qtlsConfig, err := quantum.NewServerConfig()
	if err != nil {
		slog.Error("quantum TLS initialization failed", "error", err)
		os.Exit(1)
	}

	// Load multi-environment configuration
	cfg, err := config.Load(ctx, configFS)
	if err != nil {
		slog.Error("configuration loading failed", "error", err)
		os.Exit(1)
	}

	// Initialize observability
	shutdownTelemetry, err := telemetry.Init(ctx, cfg.Telemetry)
	if err != nil {
		slog.Error("telemetry initialization failed", "error", err)
		os.Exit(1)
	}
	defer shutdownTelemetry()

	// Database initialization
	sqlDB, err := db.NewPostgresPool(ctx, cfg.Database)
	if err != nil {
		slog.Error("database connection failed", "error", err)
		os.Exit(1)
	}
	defer sqlDB.Close()
	
	// Run database migrations
	if err := db.RunMigrations(ctx, sqlDB, migrationFS); err != nil {
		slog.Error("database migrations failed", "error", err)
		os.Exit(1)
	}

	// Initialize core subsystems
	authService := auth.NewService(sqlDB, cfg.Auth)
	agentManager := agent.NewManager(sqlDB, cfg.Agents)

	// Create gRPC server with quantum-safe TLS
	grpcServer := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(qtlsConfig)),
		grpc.ChainUnaryInterceptor(
			auth.GRPCInterceptor(authService),
			otelgrpc.UnaryServerInterceptor(),
		),
	)

	// Register gRPC services
	agent.RegisterAgentServiceServer(grpcServer, agentManager)
	auth.RegisterAuthServiceServer(grpcServer, authService)

	// Create HTTP gateway mux
	httpMux := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{}),
		runtime.WithIncomingHeaderMatcher(auth.HeaderMatcher),
	)

	// Register gRPC gateway endpoints
	if err := agent.RegisterAgentServiceHandlerServer(ctx, httpMux, agentManager); err != nil {
		slog.Error("failed to register agent HTTP gateway", "error", err)
		os.Exit(1)
	}

	// Configure HTTP server
	httpSrv := &http.Server{
		Addr:         cfg.Server.HTTPAddr,
		Handler:      registerHTTPRoutes(httpMux, sqlDB, cfg),
		TLSConfig:    qtlsConfig,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		BaseContext:  func(net.Listener) context.Context { return ctx },
	}

	// Start servers with graceful shutdown
	var wg sync.WaitGroup
	startServers(ctx, &wg, cfg.Server.GRPCAddr, grpcServer, httpSrv)

	// Wait for termination signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	slog.Info("shutdown signal received, draining connections")
	cancel()
	
	if err := httpSrv.Shutdown(ctx); err != nil {
		slog.Error("HTTP server shutdown error", "error", err)
	}
	grpcServer.GracefulStop()
	wg.Wait()
}

func startServers(ctx context.Context, wg *sync.WaitGroup, grpcAddr string, grpcServer *grpc.Server, httpSrv *http.Server) {
	wg.Add(2)

	// Start gRPC server
	go func() {
		defer wg.Done()
		lis, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			slog.Error("gRPC server listen failed", "error", err)
			return
		}
		slog.Info("gRPC server started", "addr", grpcAddr)
		if err := grpcServer.Serve(lis); err != nil {
			slog.Error("gRPC server failed", "error", err)
		}
	}()

	// Start HTTP server
	go func() {
		defer wg.Done()
		slog.Info("HTTP server starting", "addr", httpSrv.Addr)
		if err := httpSrv.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
			slog.Error("HTTP server failed", "error", err)
		}
	}()
}

func registerHTTPRoutes(mux *runtime.ServeMux, db *sql.DB, cfg *config.Config) http.Handler {
	rootMux := http.NewServeMux()
	
	// Register monitoring endpoints
	rootMux.Handle("/metrics", telemetry.Handler())
	rootMux.Handle("/health", healthCheckHandler(db))

	// API routes
	rootMux.Handle("/api/", http.StripPrefix("/api", mux))

	// Apply middleware chain
	return auth.MiddlewareChain(rootMux,
		auth.NewRateLimiter(cfg.Auth.RateLimit),
		telemetry.HTTPMiddleware(),
		auth.CORSMiddleware(cfg.Server.CORS),
	)
}

func healthCheckHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := db.PingContext(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

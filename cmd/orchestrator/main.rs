// src/main.rs
#![forbid(unsafe_code)]
#![warn(missing_docs)]
#![warn(clippy::all)]

use std::{net::SocketAddr, sync::Arc, time::Duration};
use nuzon_core::{
    config::{load_config, Config},
    coordinator::QuantumCoordinator,
    crypto::postquantum::{KyberKeypair, KyberProvider},
    db::PgPool,
    metrics::MetricsRegistry,
    pb::{
        coordinator_service_server::CoordinatorServiceServer,
        health_server::HealthServer,
    },
    telemetry::{init_tracing, shutdown_tracing},
};
use tokio::{signal, sync::mpsc};
use tonic::transport::Server;
use tracing::{info, error};

mod error;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Load environment configuration
    dotenvy::dotenv().ok();
    let config = load_config().await?;
    
    // Initialize telemetry
    let telemetry_guard = init_tracing(&config.telemetry);
    
    // Setup quantum-secure cryptography
    let kyber_provider = KyberProvider::new(config.security.kyber_params.clone());
    let server_kp = kyber_provider.generate_keypair().await?;
    
    // Initialize database pool
    let db_pool = PgPool::connect_with_retry(
        &config.database.url,
        config.database.max_connections,
        Duration::from_secs(5),
        10,
    ).await?;
    
    // Create metrics registry
    let metrics = MetricsRegistry::new(
        config.metrics.endpoint.clone(),
        config.metrics.interval_secs,
    );
    
    // Build coordination engine
    let (shutdown_tx, shutdown_rx) = mpsc::channel(1);
    let coordinator = QuantumCoordinator::new(
        db_pool.clone(),
        server_kp.public_key().clone(),
        metrics.clone(),
        shutdown_rx,
    ).await?;
    
    // Prepare gRPC server
    let reflection = tonic_reflection::server::Builder::configure()
        .register_encoded_file_descriptor_set(nuzon_core::pb::FILE_DESCRIPTOR_SET)
        .build()?;

    let svc = CoordinatorServiceServer::new(coordinator);
    let health = HealthServer::new(coordinator.clone());
    
    // Start metrics exporter
    let metrics_handle = metrics.start_exporter().await?;
    
    // Configure server with quantum-secure TLS
    let tls_config = kyber_provider
        .server_tls_config(server_kp)
        .await?;

    let addr: SocketAddr = config.server.addr.parse()?;
    let server = Server::builder()
        .tls_config(tls_config)?
        .add_service(svc)
        .add_service(health)
        .add_service(reflection)
        .with_graceful_shutdown(shutdown_signal(shutdown_tx.clone()));

    // Start coordination engine
    info!("Starting coordination engine on {}", addr);
    server.serve(addr).await?;
    
    // Cleanup resources
    shutdown_tx.send(()).await?;
    metrics_handle.await??;
    shutdown_tracing(telemetry_guard);
    Ok(())
}

async fn shutdown_signal(shutdown_tx: mpsc::Sender<()>) {
    let ctrl_c = async {
        signal::ctrl_c()
            .await
            .expect("failed to install Ctrl+C handler");
    };

    #[cfg(unix)]
    let terminate = async {
        signal::unix::signal(signal::unix::SignalKind::terminate())
            .expect("failed to install signal handler")
            .recv()
            .await;
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => {},
        _ = terminate => {},
    }

    info!("Signal received, starting graceful shutdown");
    let _ = shutdown_tx.send(()).await;
}

// error.rs
mod error {
    use thiserror::Error;
    
    #[derive(Error, Debug)]
    pub enum CoordinationError {
        #[error("Database connection failed: {0}")]
        DbConnection(#[from] sqlx::Error),
        #[error("Configuration error: {0}")]
        Config(#[from] config::ConfigError),
        #[error("Cryptographic operation failed: {0}")]
        Crypto(String),
        #[error("gRPC transport error: {0}")]
        Transport(#[from] tonic::transport::Error),
        #[error("Invalid protocol state: {0}")]
        ProtocolViolation(String),
        #[error("Resource exhausted: {0}")]
        ResourceExhausted(String),
        #[error("I/O operation failed: {0}")]
        Io(#[from] std::io::Error),
    }
}

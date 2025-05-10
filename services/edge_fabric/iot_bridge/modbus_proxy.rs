// modbus_proxy.rs - Industrial SCADA Protocol Gateway
#![forbid(unsafe_code)]
#![warn(missing_docs)]
#![feature(async_fn_in_trait)]

use std::{net::SocketAddr, time::Duration};
use tokio::{net::TcpListener, sync::Mutex};
use tokio_modbus::{
    prelude::*,
    server::tcp::{accept_tcp_connection, Server},
};
use rustls::{ServerConfig, Certificate, PrivateKey};
use futures::StreamExt;
use serde::Deserialize;
use log::{info, error, warn};

const MAX_CONNECTIONS: u32 = 1024;
const DEFAULT_TIMEOUT: u64 = 5000; // milliseconds

#[derive(Debug, Deserialize)]
struct ModbusProxyConfig {
    listen_addr: String,
    tls_cert_path: String,
    tls_key_path: String,
    scada_endpoints: Vec<ScadaEndpoint>,
    access_policies: Vec<AccessPolicy>,
    #[serde(default = "default_timeout")]
    request_timeout: u64,
}

struct ScadaSecurity {
    tls_config: Arc<ServerConfig>,
    access_control: Mutex<AccessController>,
    session_logger: Mutex<SessionAuditor>,
}

impl ScadaSecurity {
    async fn new(config: &ModbusProxyConfig) -> Result<Self> {
        let certs = load_certs(&config.tls_cert_path)?;
        let key = load_private_key(&config.tls_key_path)?;
        
        let tls_config = ServerConfig::builder()
            .with_safe_defaults()
            .with_no_client_auth()
            .with_single_cert(certs, key)?;

        Ok(Self {
            tls_config: Arc::new(tls_config),
            access_control: Mutex::new(AccessController::new(&config.access_policies)),
            session_logger: Mutex::new(SessionAuditor::new()),
        })
    }
}

struct ModbusProxy {
    security: ScadaSecurity,
    scada_ctx: Arc<ScadaContext>,
    runtime: RuntimeManager,
}

impl ModbusProxy {
    pub async fn run(config: ModbusProxyConfig) -> Result<()> {
        let security = ScadaSecurity::new(&config).await?;
        let scada_ctx = Arc::new(ScadaContext::new(config.scada_endpoints));
        let proxy = Self { security, scada_ctx, runtime: RuntimeManager::new() };

        let listener = TcpListener::bind(&config.listen_addr).await?;
        info!("Modbus/TLS proxy listening on {}", config.listen_addr);

        loop {
            let (stream, peer_addr) = listener.accept().await?;
            let ctx = proxy.scada_ctx.clone();
            let tls_config = proxy.security.tls_config.clone();
            
            proxy.runtime.spawn_task(async move {
                match proxy.handle_connection(stream, peer_addr, ctx, tls_config).await {
                    Ok(_) => info!("Connection closed: {}", peer_addr),
                    Err(e) => error!("Connection error: {} - {}", peer_addr, e),
                }
            });
        }
    }

    async fn handle_connection(
        &self,
        stream: TcpStream,
        peer_addr: SocketAddr,
        ctx: Arc<ScadaContext>,
        tls_config: Arc<ServerConfig>,
    ) -> Result<()> {
        let tls_stream = TlsServerStream::new(stream, tls_config);
        let mut transport = Framed::new(tls_stream, BytesCodec::new());
        
        self.security.session_logger.lock().await.log_connection(peer_addr).await;

        while let Some(frame) = transport.next().await {
            let frame = frame?;
            let req = ModbusRequest::try_from(frame)?;
            
            if !self.security.access_control.lock().await.check_policy(&peer_addr, &req).await? {
                warn!("Access denied for {}: {:?}", peer_addr, req);
                continue;
            }

            let response = ctx.process_request(req).await?;
            transport.send(response.into()).await?;
            
            self.security.session_logger.lock().await.log_transaction(
                &peer_addr, 
                &req, 
                &response
            ).await;
        }

        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::net::TcpStream;
    use std::net::{IpAddr, Ipv4Addr};

    #[tokio::test]
    async fn test_secure_modbus_handshake() {
        let config = ModbusProxyConfig {
            listen_addr: "127.0.0.1:8502".into(),
            tls_cert_path: "certs/server.pem".into(),
            tls_key_path: "certs/server-key.pem".into(),
            scada_endpoints: vec![],
            access_policies: vec![],
            request_timeout: 1000,
        };
        
        let proxy_task = tokio::spawn(ModbusProxy::run(config));
        let client = TcpStream::connect("127.0.0.1:8502").await.unwrap();
        
        // Verify TLS handshake
        let tls_connector = TlsConnector::from(Arc::new(client_tls_config()));
        let mut tls_stream = tls_connector.connect("localhost", client).await.unwrap();
        
        tls_stream.close().await.unwrap();
        proxy_task.abort();
    }
}

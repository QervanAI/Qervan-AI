// hsm_integration.rs - Enterprise Hardware Security Module Integration
#![forbid(unsafe_code)]
#![warn(missing_docs)]
#![feature(async_fn_in_trait)]

use std::{
    path::Path,
    sync::Arc,
    time::{Duration, Instant},
};
use pkcs11::{
    types::{
        CInitializeArgs, CK_OBJECT_HANDLE, CK_SESSION_HANDLE, 
        Mechanism, MechanismType, Ulong,
    },
    Ctx,
};
use thiserror::Error;
use tracing::{debug, error, info, instrument, warn};
use prometheus::{register_int_counter_vec, IntCounterVec};

#[derive(Debug, Clone)]
pub struct HsmConfig {
    lib_path: String,
    pin: String,
    slot: Ulong,
    key_label: String,
    operation_timeout: Duration,
}

#[derive(Debug, Error)]
pub enum HsmError {
    #[error("HSM initialization failed: {0}")]
    InitializationFailed(String),
    #[error("Authentication failure")]
    AuthError,
    #[error("Key not found: {0}")]
    KeyNotFound(String),
    #[error("Cryptographic operation failed: {0}")]
    CryptoError(String),
    #[error("Connection timeout")]
    Timeout,
    #[error("Invalid configuration: {0}")]
    ConfigError(String),
}

pub struct HsmClient {
    ctx: Arc<Ctx>,
    session: CK_SESSION_HANDLE,
    config: HsmConfig,
    metrics: HsmMetrics,
}

#[derive(Clone)]
struct HsmMetrics {
    operations: IntCounterVec,
    errors: IntCounterVec,
    latency: prometheus::HistogramVec,
}

impl HsmClient {
    #[instrument]
    pub async fn new(config: HsmConfig) -> Result<Self, HsmError> {
        let ctx = Arc::new(
            Ctx::new_and_initialize(
                Path::new(&config.lib_path),
                CInitializeArgs::OsThreads)
                .map_err(|e| HsmError::InitializationFailed(e.to_string()))?
        );
        
        let session = ctx.open_session(config.slot, pkcs11::types::SessionType::Rw)
            .map_err(|e| HsmError::InitializationFailed(e.to_string()))?;
        
        ctx.login(session, pkcs11::types::UserType::User, &config.pin)
            .map_err(|_| HsmError::AuthError)?;

        let metrics = HsmMetrics::register();
        
        Ok(Self { ctx, session, config, metrics })
    }

    #[instrument(skip(self))]
    pub async fn generate_key_pair(&self) -> Result<(CK_OBJECT_HANDLE, CK_OBJECT_HANDLE), HsmError> {
        let start = Instant::now();
        let mechanism = Mechanism::RsaPkcsKeyPairGen;
        
        let pub_template = vec![
            pkcs11::types::Attribute::Token(true),
            pkcs11::types::Attribute::Verify(true),
            pkcs11::types::Attribute::Label(self.config.key_label.as_bytes().to_vec()),
        ];

        let priv_template = vec![
            pkcs11::types::Attribute::Token(true),
            pkcs11::types::Attribute::Sign(true),
            pkcs11::types::Attribute::Sensitive(true),
            pkcs11::types::Attribute::Label(self.config.key_label.as_bytes().to_vec()),
        ];

        match self.ctx.generate_key_pair(self.session, &mechanism, &pub_template, &priv_template) {
            Ok((public, private)) => {
                self.metrics.operations.with_label_values(&["keygen"]).inc();
                self.metrics.latency.with_label_values(&["keygen"])
                    .observe(start.elapsed().as_secs_f64());
                Ok((public, private))
            }
            Err(e) => {
                self.metrics.errors.with_label_values(&["keygen"]).inc();
                error!("Key generation failed: {:?}", e);
                Err(HsmError::CryptoError(e.to_string()))
            }
        }
    }

    #[instrument(skip(self, data))]
    pub async fn sign(&self, data: &[u8]) -> Result<Vec<u8>, HsmError> {
        let start = Instant::now();
        let key = self.find_key()?;
        let mechanism = Mechanism::RsaPkcs;
        
        self.ctx.sign_init(self.session, &mechanism, key)
            .map_err(|e| HsmError::CryptoError(e.to_string()))?;

        match self.ctx.sign(self.session, data) {
            Ok(signature) => {
                self.metrics.operations.with_label_values(&["sign"]).inc();
                self.metrics.latency.with_label_values(&["sign"])
                    .observe(start.elapsed().as_secs_f64());
                Ok(signature)
            }
            Err(e) => {
                self.metrics.errors.with_label_values(&["sign"]).inc();
                error!("Signing failed: {:?}", e);
                Err(HsmError::CryptoError(e.to_string()))
            }
        }
    }

    #[instrument(skip(self))]
    fn find_key(&self) -> Result<CK_OBJECT_HANDLE, HsmError> {
        let template = vec![
            pkcs11::types::Attribute::Class(pkcs11::types::ObjectClass::PRIVATE_KEY),
            pkcs11::types::Attribute::Label(self.config.key_label.as_bytes().to_vec()),
        ];

        match self.ctx.find_objects(self.session, &template, 1) {
            Ok(mut objects) => objects.pop()
                .ok_or_else(|| HsmError::KeyNotFound(self.config.key_label.clone())),
            Err(e) => {
                error!("Key search failed: {:?}", e);
                Err(HsmError::CryptoError(e.to_string()))
            }
        }
    }
}

impl HsmMetrics {
    fn register() -> Self {
        Self {
            operations: register_int_counter_vec!(
                "hsm_operations_total",
                "HSM cryptographic operations count",
                &["operation"]
            ).unwrap(),
            errors: register_int_counter_vec!(
                "hsm_errors_total",
                "HSM operation errors",
                &["operation"]
            ).unwrap(),
            latency: prometheus::register_histogram_vec!(
                "hsm_operation_duration_seconds",
                "HSM operation latency",
                &["operation"],
                vec![0.01, 0.05, 0.1, 0.5, 1.0, 5.0]
            ).unwrap(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::runtime::Runtime;

    #[test]
    fn test_hsm_initialization() {
        let config = HsmConfig {
            lib_path: "/usr/lib/softhsm/libsofthsm2.so".to_string(),
            pin: "1234".to_string(),
            slot: 0,
            key_label: "test-key".to_string(),
            operation_timeout: Duration::from_secs(5),
        };

        let rt = Runtime::new().unwrap();
        rt.block_on(async {
            let client = HsmClient::new(config).await;
            assert!(client.is_ok());
        });
    }
}

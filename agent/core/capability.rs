// capability.rs - Enterprise Skill Management Engine
#![forbid(unsafe_code)]
#![warn(missing_docs)]
#![feature(type_alias_impl_trait)]

use std::{
    collections::{BTreeMap, HashMap},
    sync::Arc,
    time::Duration,
};
use anyhow::{Context, Result};
use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use tokio::sync::{Mutex, Semaphore};
use tracing::{debug_span, instrument, Instrument};
use uuid::Uuid;

/// Enterprise capability metadata
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CapabilityMeta {
    pub id: Uuid,
    pub version: semver::Version,
    pub required_claims: Vec<String>,
    pub resource_limits: ResourceLimits,
    pub dependencies: Vec<CapabilityRef>,
}

/// Hardware resource constraints
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ResourceLimits {
    pub max_memory_mb: u32,
    pub max_cpu_cores: f32,
    pub timeout_secs: u64,
}

/// Versioned capability reference
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CapabilityRef {
    pub name: String,
    pub version_req: semver::VersionReq,
}

/// Runtime capability interface
#[async_trait]
pub trait EnterpriseCapability: Send + Sync {
    async fn execute(
        &self,
        params: serde_json::Value,
        context: ExecutionContext,
    ) -> Result<serde_json::Value>;
}

/// Security context for capability execution
pub struct ExecutionContext {
    pub caller_identity: String,
    pub auth_claims: Vec<String>,
    pub resource_budget: ResourceBudget,
}

/// Runtime resource allocation
pub struct ResourceBudget {
    semaphore: Arc<Semaphore>,
    cpu_cores: f32,
    _guard: tokio::sync::OwnedSemaphorePermit,
}

/// Central capability registry
#[derive(Default)]
pub struct CapabilityRegistry {
    capabilities: Mutex<HashMap<String, BTreeMap<semver::Version, Arc<dyn EnterpriseCapability>>>>,
    resource_pools: Mutex<HashMap<String, ResourcePool>>,
}

impl CapabilityRegistry {
    /// Register new capability version
    #[instrument(skip_all)]
    pub async fn register(
        &self,
        meta: CapabilityMeta,
        capability: Arc<dyn EnterpriseCapability>,
    ) -> Result<()> {
        let mut caps = self.capabilities.lock().await;
        let versions = caps.entry(meta.id.to_string()).or_default();
        
        if versions.contains_key(&meta.version) {
            anyhow::bail!("Capability version already registered");
        }

        // Initialize resource pool
        let mut pools = self.resource_pools.lock().await;
        pools.entry(meta.id.to_string())
            .or_insert_with(|| ResourcePool::new(
                meta.resource_limits.max_memory_mb,
                meta.resource_limits.max_cpu_cores,
            ));

        versions.insert(meta.version.clone(), capability);
        Ok(())
    }

    /// Execute capability with security controls
    #[instrument(skip_all)]
    pub async fn execute(
        &self,
        capability_id: &str,
        version: &semver::VersionReq,
        params: serde_json::Value,
        context: ExecutionContext,
    ) -> Result<serde_json::Value> {
        let caps = self.capabilities.lock().await;
        let versions = caps.get(capability_id)
            .context("Capability not found")?;

        // Select latest compatible version
        let selected = versions.iter()
            .rev()
            .find(|(v, _)| version.matches(v))
            .context("No compatible version available")?;

        // Acquire resource budget
        let pools = self.resource_pools.lock().await;
        let pool = pools.get(capability_id)
            .context("Resource pool missing")?;

        let budget = pool.allocate(
            context.caller_identity.clone(),
            context.auth_claims.clone(),
        ).await?;

        // Execute with timeout
        let result = tokio::time::timeout(
            Duration::from_secs(pool.timeout_secs),
            selected.1.execute(params, ExecutionContext {
                resource_budget: budget,
                ..context
            }),
        ).await??;

        Ok(result)
    }
}

/// Resource isolation pool
struct ResourcePool {
    semaphore: Arc<Semaphore>,
    cpu_cores: f32,
    memory_mb: u32,
    timeout_secs: u64,
}

impl ResourcePool {
    fn new(memory_mb: u32, cpu_cores: f32) -> Self {
        Self {
            semaphore: Arc::new(Semaphore::new(cpu_cores as usize)),
            cpu_cores,
            memory_mb,
            timeout_secs: 30, // Default timeout
        }
    }

    async fn allocate(&self, caller: String, claims: Vec<String>) -> Result<ResourceBudget> {
        let permit = self.semaphore.clone()
            .acquire_owned()
            .await
            .context("Resource allocation timeout")?;

        Ok(ResourceBudget {
            semaphore: self.semaphore.clone(),
            cpu_cores: self.cpu_cores,
            _guard: permit,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    struct TestCapability;
    
    #[async_trait]
    impl EnterpriseCapability for TestCapability {
        async fn execute(
            &self,
            _params: serde_json::Value,
            _context: ExecutionContext,
        ) -> Result<serde_json::Value> {
            Ok(serde_json::json!({"status": "success"}))
        }
    }

    #[tokio::test]
    async fn test_capability_lifecycle() {
        let registry = CapabilityRegistry::default();
        let meta = CapabilityMeta {
            id: Uuid::new_v4(),
            version: semver::Version::parse("1.0.0").unwrap(),
            required_claims: vec!["admin".into()],
            resource_limits: ResourceLimits {
                max_memory_mb: 1024,
                max_cpu_cores: 2.0,
                timeout_secs: 5,
            },
            dependencies: vec![],
        };

        registry.register(meta.clone(), Arc::new(TestCapability))
            .await
            .unwrap();

        let result = registry.execute(
            &meta.id.to_string(),
            &semver::VersionReq::parse("^1.0").unwrap(),
            serde_json::Value::Null,
            ExecutionContext {
                caller_identity: "test".into(),
                auth_claims: vec!["admin".into()],
                resource_budget: ResourceBudget {
                    semaphore: Arc::new(Semaphore::new(1)),
                    cpu_cores: 1.0,
                    _guard: Semaphore::new(1).acquire_owned().await.unwrap(),
                },
            },
        ).await.unwrap();

        assert_eq!(result, serde_json::json!({"status": "success"}));
    }
}

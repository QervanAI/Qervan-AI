// eigen_trust.rs - Enterprise-Grade Reputation Management
#![forbid(unsafe_code)]
#![warn(missing_docs)]
#![feature(map_first_last)]

use std::{
    collections::{BTreeMap, HashMap},
    sync::Arc,
    time::SystemTime
};

use ed25519_dalek::{PublicKey, Signature, Signer, Verifier};
use rayon::prelude::*;
use serde::{Deserialize, Serialize};
use tokio_postgres::{Client, NoTls};

const CONVERGENCE_THRESHOLD: f64 = 1e-9;
const MAX_ITERATIONS: usize = 100;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Node {
    pub id: String,
    pub public_key: PublicKey,
    local_trust: BTreeMap<String, f64>,
    global_trust: f64,
    last_updated: SystemTime,
}

#[derive(Debug)]
pub struct ReputationEngine {
    nodes: Arc<tokio::sync::RwLock<HashMap<String, Node>>>,
    db_client: Client,
    alpha: f64,
}

impl ReputationEngine {
    pub async fn new(db_uri: &str, alpha: f64) -> Result<Self, ReputationError> {
        let (client, connection) = tokio_postgres::connect(db_uri, NoTls).await?;
        tokio::spawn(async move { connection.await });
        
        Ok(Self {
            nodes: Arc::new(tokio::sync::RwLock::new(HashMap::new())),
            db_client: client,
            alpha,
        })
    }

    pub async fn initialize_trust(&self) -> Result<(), ReputationError> {
        let mut nodes = self.nodes.write().await;
        let rows = self.db_client.query("SELECT id, public_key, trust_data FROM nodes", &[]).await?;
        
        for row in rows {
            let id: String = row.get(0);
            let public_key: Vec<u8> = row.get(1);
            let trust_data: Vec<u8> = row.get(2);
            
            nodes.insert(id.clone(), Node {
                id,
                public_key: PublicKey::from_bytes(&public_key)?,
                local_trust: bincode::deserialize(&trust_data)?,
                global_trust: 1.0,
                last_updated: SystemTime::now(),
            });
        }
        Ok(())
    }

    pub async fn update_trust(&self) -> Result<(), ReputationError> {
        let nodes = self.nodes.read().await;
        let mut prev_global = HashMap::new();
        let mut current_global = nodes.iter()
            .map(|(id, node)| (id.clone(), node.global_trust))
            .collect::<HashMap<_, _>>();

        for _ in 0..MAX_ITERATIONS {
            prev_global = current_global.clone();
            current_global = self.compute_global_trust(¤t_global).await?;
            
            let delta = prev_global.iter()
                .map(|(k, v)| (current_global[k] - v).abs())
                .fold(0.0, f64::max);
                
            if delta < CONVERGENCE_THRESHOLD {
                break;
            }
        }

        let mut nodes = self.nodes.write().await;
        for (id, trust) in current_global {
            if let Some(node) = nodes.get_mut(&id) {
                node.global_trust = trust;
            }
        }

        self.persist_trust().await
    }

    async fn compute_global_trust(&self, prev_trust: &HashMap<String, f64>) -> Result<HashMap<String, f64>, ReputationError> {
        let nodes = self.nodes.read().await;
        let new_trust: HashMap<String, f64> = nodes.par_iter()
            .map(|(node_id, node)| {
                let weighted_sum = node.local_trust.iter()
                    .map(|(neighbor_id, local)| {
                        let global = prev_trust.get(neighbor_id).copied().unwrap_or(0.0);
                        local * global
                    })
                    .sum::<f64>();
                    
                (node_id.clone(), self.alpha * weighted_sum + (1.0 - self.alpha) * node.global_trust)
            })
            .collect();

        Ok(normalize_trust(&new_trust))
    }

    async fn persist_trust(&self) -> Result<(), ReputationError> {
        let nodes = self.nodes.read().await;
        let transaction = self.db_client.transaction().await?;

        for (id, node) in nodes.iter() {
            let trust_data = bincode::serialize(&node.local_trust)?;
            transaction.execute(
                "INSERT INTO nodes (id, public_key, trust_data, global_trust) 
                 VALUES (\$1, \$2, \$3, \$4)
                 ON CONFLICT (id) DO UPDATE SET 
                     trust_data = EXCLUDED.trust_data,
                     global_trust = EXCLUDED.global_trust",
                &[&id, &node.public_key.to_bytes().to_vec(), &trust_data, &node.global_trust]
            ).await?;
        }

        transaction.commit().await?;
        Ok(())
    }

    pub async fn add_interaction(
        &self,
        source_id: &str,
        target_id: &str,
        score: f64,
        signature: &Signature
    ) -> Result<(), ReputationError> {
        let mut nodes = self.nodes.write().await;
        let source = nodes.get(source_id)
            .ok_or(ReputationError::NodeNotFound)?;

        source.public_key.verify(
            format!("{}{}{}", source_id, target_id, score).as_bytes(),
            signature
        )?;

        let entry = nodes.get_mut(source_id)
            .ok_or(ReputationError::NodeNotFound)?
            .local_trust
            .entry(target_id.to_string())
            .or_insert(0.0);
            
        *entry = (*entry + score).max(0.0).min(1.0);
        Ok(())
    }
}

fn normalize_trust(trust_scores: &HashMap<String, f64>) -> HashMap<String, f64> {
    let total: f64 = trust_scores.values().sum();
    if total.abs() < f64::EPSILON {
        return trust_scores.iter()
            .map(|(k, _)| (k.clone(), 1.0 / trust_scores.len() as f64))
            .collect();
    }
    
    trust_scores.iter()
        .map(|(k, v)| (k.clone(), v / total))
        .collect()
}

#[derive(Debug, thiserror::Error)]
pub enum ReputationError

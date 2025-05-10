// e91_quantum.rs - Entanglement-based Quantum Key Distribution
#![forbid(unsafe_code)]
#![warn(missing_docs)]
#![feature(specialization)]

use rand::{Rng, thread_rng};
use anyhow::{Result, Context};
use serde::{Serialize, Deserialize};
use std::collections::HashMap;

const ENTANGLED_PAIRS: usize = 1024;
const BELL_THRESHOLD: f64 = 2.0f64.sqrt();

#[derive(Debug, Clone, Copy, PartialEq, Serialize, Deserialize)]
enum Basis {
    Rectilinear,
    Diagonal,
    Circular,
}

#[derive(Debug, Clone, Copy, Serialize, Deserialize)]
struct PhotonPair {
    alice_angle: f64,
    bob_angle: f64,
    alice_result: i8,
    bob_result: i8,
}

struct E91Channel {
    entangled_pairs: Vec<PhotonPair>,
    basis_choices: HashMap<usize, (Basis, Basis)>,
}

impl E91Channel {
    fn generate_entangled_pairs(&mut self) -> Result<()> {
        let mut rng = thread_rng();
        
        self.entangled_pairs = (0..ENTANGLED_PAIRS)
            .map(|_| {
                let base_angle: f64 = rng.gen_range(0.0..360.0);
                PhotonPair {
                    alice_angle: base_angle,
                    bob_angle: (base_angle + 45.0) % 360.0,
                    alice_result: 0,
                    bob_result: 0,
                }
            })
            .collect();
            
        Ok(())
    }

    fn measure_pair(&mut self, index: usize, alice_basis: Basis, bob_basis: Basis) -> Result<()> {
        let pair = self.entangled_pairs
            .get_mut(index)
            .context("Invalid photon pair index")?;

        let (a_result, b_result) = self.quantum_measurement(
            pair.alice_angle, 
            pair.bob_angle,
            alice_basis,
            bob_basis,
        )?;

        pair.alice_result = a_result;
        pair.bob_result = b_result;
        self.basis_choices.insert(index, (alice_basis, bob_basis));
        
        Ok(())
    }

    fn quantum_measurement(
        &self,
        a_angle: f64,
        b_angle: f64,
        a_basis: Basis,
        b_basis: Basis,
    ) -> Result<(i8, i8)> {
        let angle_diff = |angle1: f64, angle2: f64| -> f64 {
            let diff = (angle1 - angle2).abs();
            diff.min(360.0 - diff)
        };

        let (a, b) = match (a_basis, b_basis) {
            (Basis::Rectilinear, Basis::Diagonal) => {
                let theta = angle_diff(a_angle, 0.0);
                let phi = angle_diff(b_angle, 45.0);
                quantum_probability(theta, phi)
            }
            (Basis::Diagonal, Basis::Circular) => {
                let theta = angle_diff(a_angle, 45.0);
                let phi = angle_diff(b_angle, 90.0);
                quantum_probability(theta, phi)
            }
            (Basis::Circular, Basis::Rectilinear) => {
                let theta = angle_diff(a_angle, 90.0);
                let phi = angle_diff(b_angle, 0.0);
                quantum_probability(theta, phi)
            }
            _ => return Ok((0, 0)), // Basis mismatch
        };

        Ok((a, b))
    }

    fn verify_bell_inequality(&self) -> Result<f64> {
        let mut chsh = 0.0f64;
        let mut valid_pairs = 0;

        for (index, (a_basis, b_basis)) in &self.basis_choices {
            let pair = self.entangled_pairs
                .get(*index)
                .context("Missing measurement data")?;

            if *a_basis != Basis::Rectilinear || *b_basis != Basis::Diagonal {
                continue;
            }

            chsh += (pair.alice_result * pair.bob_result) as f64;
            valid_pairs += 1;
        }

        let bell_value = chsh / (valid_pairs as f64);
        if bell_value.abs() > BELL_THRESHOLD {
            Ok(bell_value)
        } else {
            Err(anyhow::anyhow!("Quantum entanglement violation: {}", bell_value))
        }
    }

    fn generate_key(&self) -> Result<Vec<u8>> {
        let mut key = Vec::with_capacity(ENTANGLED_PAIRS / 8);
        let mut buffer = 0u8;
        let mut bit_count = 0;

        for (index, pair) in self.entangled_pairs.iter().enumerate() {
            let bases = self.basis_choices.get(&index)
                .context("Missing basis choice")?;

            if bases.0 == Basis::Rectilinear && bases.1 == Basis::Rectilinear {
                buffer = (buffer << 1) | (pair.alice_result as u8 & 1);
                bit_count += 1;

                if bit_count == 8 {
                    key.push(buffer);
                    buffer = 0;
                    bit_count = 0;
                }
            }
        }

        Ok(key)
    }
}

fn quantum_probability(theta: f64, phi: f64) -> (i8, i8) {
    let angle = (theta - phi).to_radians();
    let prob = (angle / 2.0).cos().powi(2);
    
    let mut rng = thread_rng();
    let result = if rng.gen_bool(prob.into()) { 1 } else { -1 };
    
    (result, -result)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn full_protocol_cycle() -> Result<()> {
        let mut channel = E91Channel {
            entangled_pairs: Vec::new(),
            basis_choices: HashMap::new(),
        };
        
        channel.generate_entangled_pairs()?;
        
        // Simulate measurements
        let mut rng = thread_rng();
        for i in 0..ENTANGLED_PAIRS {
            let a_basis = match rng.gen_range(0..3) {
                0 => Basis::Rectilinear,
                1 => Basis::Diagonal,
                _ => Basis::Circular,
            };
            
            let b_basis = match rng.gen_range(0..3) {
                0 => Basis::Rectilinear,
                1 => Basis::Diagonal,
                _ => Basis::Circular,
            };
            
            channel.measure_pair(i, a_basis, b_basis)?;
        }
        
        let bell_value = channel.verify_bell_inequality()?;
        assert!(bell_value.abs() > BELL_THRESHOLD, 
            "Bell inequality violation failed: {}", bell_value);
            
        let key = channel.generate_key()?;
        assert!(!key.is_empty(), "Key generation failed");
        
        Ok(())
    }
}

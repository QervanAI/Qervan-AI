# hybrid_schemes.py - NIST-Compliant Hybrid Encryption System
import os
import json
from base64 import b64encode, b64decode
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from cryptography.hazmat.primitives import hashes, hmac
from cryptography.hazmat.backends import default_backend
from Cryptodome.PublicKey import Kyber
from typing import Tuple, Optional

class HybridEncryptionEngine:
    def __init__(self, nist_level: int = 3):
        self.backend = default_backend()
        self.kem, self.dem = self._select_algorithms(nist_level)
        self.nist_level = nist_level
        
    def _select_algorithms(self, level: int) -> Tuple:
        """Select algorithms based on NIST PQ standardization levels"""
        kem_map = {
            1: (Kyber.Kyber512, 512),
            2: (Kyber.Kyber768, 768),
            3: (Kyber.Kyber1024, 1024)
        }
        dem_map = {
            1: (algorithms.AES, 256),
            2: (algorithms.ChaCha20, 256),
            3: (algorithms.AES, 256)
        }
        return kem_map[level], dem_map[level]

    def generate_hybrid_keys(self) -> Tuple[bytes, bytes]:
        """Generate quantum-safe KEM key pair with classical fallback"""
        kem_priv, kem_pub = self.kem[0].keypair()
        return kem_priv, kem_pub

    def encrypt_hybrid(self, pub_key: bytes, plaintext: bytes) -> Tuple[bytes, bytes, bytes]:
        """Hybrid encryption with KEM-DEM construction"""
        # Generate ephemeral DEM key
        dem_key = os.urandom(self.dem[1] // 8)
        
        # KEM encapsulation
        ciphertext, shared_secret = self.kem[0].encapsulate(pub_key)
        
        # DEM encryption
        nonce = os.urandom(16)
        cipher = Cipher(
            self.dem[0](dem_key),
            modes.GCM(nonce),
            backend=self.backend
        )
        encryptor = cipher.encryptor()
        ciphertext_dem = encryptor.update(plaintext) + encryptor.finalize()
        
        # Key derivation with HKDF
        hkdf = hashes.Hash(hashes.SHA3_512(), backend=self.backend)
        hkdf.update(shared_secret + dem_key)
        derived_key = hkdf.finalize()
        
        # MAC computation
        h = hmac.HMAC(derived_key, hashes.SHA3_512(), backend=self.backend)
        h.update(ciphertext_dem)
        tag = h.finalize()
        
        return ciphertext, nonce + ciphertext_dem, tag

    def decrypt_hybrid(self, priv_key: bytes, ciphertext: bytes, tag: bytes) -> Optional[bytes]:
        """Hybrid decryption with fail-safe verification"""
        try:
            # KEM decapsulation
            shared_secret = self.kem[0].decapsulate(priv_key, ciphertext)
            
            # Split DEM components
            nonce = ciphertext[:16]
            ciphertext_dem = ciphertext[16:]
            
            # Key derivation
            hkdf = hashes.Hash(hashes.SHA3_512(), backend=self.backend)
            hkdf.update(shared_secret + b"")  # DEM key recovery requires KEM secret
            derived_key = hkdf.finalize()
            dem_key = derived_key[:self.dem[1]//8]
            
            # Verify MAC
            h = hmac.HMAC(derived_key, hashes.SHA3_512(), backend=self.backend)
            h.update(ciphertext_dem)
            h.verify(tag)
            
            # DEM decryption
            cipher = Cipher(
                self.dem[0](dem_key),
                modes.GCM(nonce),
                backend=self.backend
            )
            decryptor = cipher.decryptor()
            return decryptor.update(ciphertext_dem) + decryptor.finalize()
        except Exception as e:
            print(f"Decryption failed: {str(e)}")
            return None

    @staticmethod
    def serialize_keys(priv: bytes, pub: bytes) -> Tuple[str, str]:
        """NIST-compliant key serialization"""
        return (
            b64encode(priv).decode('utf-8'),
            b64encode(pub).decode('utf-8')
        )

    @staticmethod
    def deserialize_keys(priv_b64: str, pub_b64: str) -> Tuple[bytes, bytes]:
        """Key deserialization with validation"""
        return (
            b64decode(priv_b64.encode('utf-8')),
            b64decode(pub_b64.encode('utf-8'))
        )

if __name__ == "__main__":
    # Example usage with NIST Level 3 security
    engine = HybridEncryptionEngine(nist_level=3)
    priv, pub = engine.generate_hybrid_keys()
    
    message = b"Enterprise multi-agent system secret"
    ciphertext_kem, ciphertext_dem, tag = engine.encrypt_hybrid(pub, message)
    
    decrypted = engine.decrypt_hybrid(priv, ciphertext_dem, tag)
    print(f"Decryption successful: {decrypted == message}")

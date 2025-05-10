// nist_rotator.go - Quantum-Safe Cryptographic Migration Engine
package crypto

import (
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/cloudflare/circl/kem"
	"github.com/cloudflare/circl/kem/kyber/kyber768"
	"golang.org/x/crypto/chacha20poly1305"
)

type KeyMigrationEngine struct {
	db           *sql.DB
	currentAlgo  AlgorithmSpec
	targetAlgo   AlgorithmSpec
	keyStore     KeyStorage
	metrics      MigrationMetrics
	compliance   NISTValidator
	rollbackPlan RollbackStrategy
}

type AlgorithmSpec struct {
	Type       AlgorithmType
	Params     json.RawMessage
	NISTLevel  int
	QuantumSafe bool
}

type MigrationMetrics struct {
	TotalRecords    int64
	Processed       int64
	Failed          int64
	StartTime       time.Time
	Throughput      float64
	ResourceUsage   ResourceMonitor
	SecurityChecks  int
}

const (
	RSA2048 AlgorithmType = iota + 1
	ECDSA_P256
	AES256_GCM
	Kyber768
	Dilithium3
	ChaCha20_Poly1305
)

func (e *KeyMigrationEngine) RotateKeys(ctx context.Context) error {
	e.metrics.StartTime = time.Now()
	defer e.logMigrationSummary()

	rows, err := e.db.QueryContext(ctx, 
		`SELECT id, public_key, encrypted_private, key_spec FROM crypto_keys 
		WHERE algo_type = \$1`, e.currentAlgo.Type)
	if err != nil {
		return fmt.Errorf("key query failed: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			id      string
			pubKey  []byte
			privKey []byte
			spec    AlgorithmSpec
		)
		
		if err := rows.Scan(&id, &pubKey, &privKey, &spec); err != nil {
			return fmt.Errorf("key scan failed: %w", err)
		}

		if err := e.migrateKey(ctx, id, pubKey, privKey, spec); err != nil {
			e.metrics.Failed++
			if errors.Is(err, context.Canceled) {
				return e.rollbackPlan.Execute(ctx)
			}
		}
		e.metrics.Processed++
	}

	return e.validatePostMigration(ctx)
}

func (e *KeyMigrationEngine) migrateKey(ctx context.Context, id string, 
	pubKey, privKey []byte, spec AlgorithmSpec) error {
	
	// 1. Decrypt legacy private key
	legacyKey, err := e.decryptLegacyKey(privKey)
	if err != nil {
		return fmt.Errorf("legacy decryption failed: %w", err)
	}

	// 2. Generate new key pair
	newKey, err := e.generateNewKeyPair()
	if err != nil {
		return fmt.Errorf("key generation failed: %w", err)
	}

	// 3. Cross-sign certificates
	if err := e.verifyCompatibility(legacyKey, newKey); err != nil {
		return fmt.Errorf("compatibility check failed: %w", err)
	}

	// 4. Store new encrypted key
	if err := e.keyStore.Store(ctx, id, newKey, e.targetAlgo); err != nil {
		return fmt.Errorf("key storage failed: %w", err)
	}

	// 5. Maintain legacy key during transition
	if err := e.keyStore.Archive(ctx, id, legacyKey); err != nil {
		return fmt.Errorf("key archiving failed: %w", err)
	}

	e.metrics.SecurityChecks++
	return nil
}

func (e *KeyMigrationEngine) decryptLegacyKey(encrypted []byte) (crypto.PrivateKey, error) {
	switch e.currentAlgo.Type {
	case RSA2048:
		return rsa.DecryptPKCS1v15(rand.Reader, nil, encrypted)
	case ECDSA_P256:
		return x509.ParseECPrivateKey(encrypted)
	case AES256_GCM:
		block, _ := aes.NewCipher(e.keyStore.GetLegacyKey())
		gcm, _ := cipher.NewGCM(block)
		nonce := encrypted[:gcm.NonceSize()]
		return gcm.Open(nil, nonce, encrypted[gcm.NonceSize():], nil)
	default:
		return nil, ErrUnsupportedAlgorithm
	}
}

func (e *KeyMigrationEngine) generateNewKeyPair() (crypto.PrivateKey, error) {
	switch e.targetAlgo.Type {
	case Kyber768:
		pub, priv, err := kyber768.GenerateKeyPair(rand.Reader)
		return &HybridPrivateKey{
			Classical: e.generateFallbackKey(),
			Quantum:   priv,
			Public:    pub,
		}, err
	case Dilithium3:
		return dilithium.GenerateKey(nil)
	case ChaCha20_Poly1305:
		key := make([]byte, chacha20poly1305.KeySize)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, err
		}
		return key, nil
	default:
		return nil, ErrUnsupportedAlgorithm
	}
}

func (e *KeyMigrationEngine) validatePostMigration(ctx context.Context) error {
	// Verify NIST SP 800-208 compliance
	if err := e.compliance.Validate(ctx, e.targetAlgo); err != nil {
		return fmt.Errorf("compliance check failed: %w", err)
	}

	// Check quantum resistance thresholds
	if e.targetAlgo.QuantumSafe {
		if err := verifyQuantumSecurity(e.keyStore); err != nil {
			return fmt.Errorf("quantum validation failed: %w", err)
		}
	}

	// Perform cryptographic proof of migration
	if err := e.generateProofOfRotation(); err != nil {
		return fmt.Errorf("audit proof generation failed: %w", err)
	}

	return nil
}

package clientauth

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"fmt"
	"math/big"
)

const (
	AlgorithmP256ES256 = "p256-es256"
	AlgorithmEd25519   = "ed25519"
)

func KeyFingerprint(spkiDER []byte) [32]byte {
	return sha256.Sum256(spkiDER)
}

// VerifyProof validates SPKI algorithm identity and the frozen signature wire
// encoding. P-256 accepts only a 64-byte P1363 low-S signature; DER signatures
// and mathematically equivalent high-S signatures are rejected.
func VerifyProof(algorithm string, spkiDER, message, signature []byte) error {
	publicKey, err := x509.ParsePKIXPublicKey(spkiDER)
	if err != nil {
		return fmt.Errorf("%w: invalid SPKI", ErrMalformed)
	}
	switch algorithm {
	case AlgorithmP256ES256:
		key, ok := publicKey.(*ecdsa.PublicKey)
		if !ok || key.Curve != elliptic.P256() || key.X == nil || key.Y == nil || !key.Curve.IsOnCurve(key.X, key.Y) {
			return fmt.Errorf("%w: SPKI is not P-256", ErrMalformed)
		}
		return verifyP256P1363LowS(key, message, signature)
	case AlgorithmEd25519:
		key, ok := publicKey.(ed25519.PublicKey)
		if !ok || len(key) != ed25519.PublicKeySize || allZero(key) {
			return fmt.Errorf("%w: SPKI is not Ed25519", ErrMalformed)
		}
		if len(signature) != ed25519.SignatureSize || !ed25519.Verify(key, message, signature) {
			return ErrVerificationFailed
		}
		return nil
	default:
		return fmt.Errorf("%w: proof algorithm", ErrUnsupported)
	}
}

func verifyP256P1363LowS(publicKey *ecdsa.PublicKey, message, signature []byte) error {
	if len(signature) != 64 {
		return fmt.Errorf("%w: P-256 signature must be P1363", ErrMalformed)
	}
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	order := publicKey.Params().N
	if r.Sign() <= 0 || s.Sign() <= 0 || r.Cmp(order) >= 0 || s.Cmp(order) >= 0 || s.Cmp(new(big.Int).Rsh(new(big.Int).Set(order), 1)) > 0 {
		return fmt.Errorf("%w: P-256 r/s range or high-S", ErrMalformed)
	}
	digest := sha256.Sum256(message)
	if !ecdsa.Verify(publicKey, digest[:], r, s) {
		return ErrVerificationFailed
	}
	return nil
}

func allZero(value []byte) bool {
	var combined byte
	for _, item := range value {
		combined |= item
	}
	return combined == 0
}

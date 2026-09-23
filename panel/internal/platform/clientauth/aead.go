package clientauth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
)

const replayAADDomain = "AEGIS-CLIENT-REPLAY-AAD-V1"

type ReplayKind string

const (
	ReplayIssuance ReplayKind = "issuance"
	ReplayRefresh  ReplayKind = "refresh"
)

type ReplayAAD struct {
	Kind                     ReplayKind
	TenantID                 string
	ReplayID                 string
	StableRequestHashB64U    string
	KeyFingerprintSHA256B64U string
	ExpiresAtMicrosecondUTC  string
}

func BuildReplayAAD(value ReplayAAD) ([]byte, error) {
	if value.Kind != ReplayIssuance && value.Kind != ReplayRefresh {
		return nil, fmt.Errorf("%w: replay kind", ErrUnsupported)
	}
	if _, err := ParseCanonicalUUID(value.TenantID); err != nil {
		return nil, fmt.Errorf("%w: tenant-id", err)
	}
	if _, err := ParseCanonicalUUID(value.ReplayID); err != nil {
		return nil, fmt.Errorf("%w: replay-id", err)
	}
	if _, err := DecodeBase64URLNoPad(value.StableRequestHashB64U, 32); err != nil {
		return nil, fmt.Errorf("%w: stable-request-hash", err)
	}
	if _, err := DecodeBase64URLNoPad(value.KeyFingerprintSHA256B64U, 32); err != nil {
		return nil, fmt.Errorf("%w: key-fingerprint", err)
	}
	if _, err := ParseMicrosecondUTC(value.ExpiresAtMicrosecondUTC); err != nil {
		return nil, fmt.Errorf("%w: expires-at", err)
	}
	encoded := replayAADDomain + "\n" +
		"kind:" + string(value.Kind) + "\n" +
		"tenant-id:" + value.TenantID + "\n" +
		"replay-id:" + value.ReplayID + "\n" +
		"stable-request-hash:" + value.StableRequestHashB64U + "\n" +
		"key-fingerprint:" + value.KeyFingerprintSHA256B64U + "\n" +
		"expires-at:" + value.ExpiresAtMicrosecondUTC + "\n"
	return []byte(encoded), nil
}

type AESGCMSealed struct {
	Nonce      []byte
	Ciphertext []byte
	Tag        []byte
}

func SealAES256GCM(key, nonce, plaintext, aad []byte) (AESGCMSealed, error) {
	gcm, err := newAES256GCM(key)
	if err != nil {
		return AESGCMSealed{}, err
	}
	if len(nonce) != gcm.NonceSize() {
		return AESGCMSealed{}, fmt.Errorf("%w: AES-GCM nonce must be 12 bytes", ErrMalformed)
	}
	combined := gcm.Seal(nil, nonce, plaintext, aad)
	cut := len(combined) - gcm.Overhead()
	return AESGCMSealed{
		Nonce:      append([]byte(nil), nonce...),
		Ciphertext: append([]byte(nil), combined[:cut]...),
		Tag:        append([]byte(nil), combined[cut:]...),
	}, nil
}

func SealRandomAES256GCM(key, plaintext, aad []byte) (AESGCMSealed, error) {
	return sealRandomAES256GCM(rand.Reader, key, plaintext, aad)
}

func sealRandomAES256GCM(random io.Reader, key, plaintext, aad []byte) (AESGCMSealed, error) {
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(random, nonce); err != nil {
		return AESGCMSealed{}, fmt.Errorf("generate AES-GCM nonce: %w", err)
	}
	return SealAES256GCM(key, nonce, plaintext, aad)
}

func OpenAES256GCM(key, nonce, ciphertext, tag, aad []byte) ([]byte, error) {
	gcm, err := newAES256GCM(key)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() || len(tag) != gcm.Overhead() {
		return nil, fmt.Errorf("%w: AES-GCM nonce or tag length", ErrMalformed)
	}
	combined := make([]byte, 0, len(ciphertext)+len(tag))
	combined = append(combined, ciphertext...)
	combined = append(combined, tag...)
	plaintext, err := gcm.Open(nil, nonce, combined, aad)
	if err != nil {
		return nil, ErrVerificationFailed
	}
	return plaintext, nil
}

func newAES256GCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("%w: AES-256 key must be 32 bytes", ErrMalformed)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if gcm.NonceSize() != 12 || gcm.Overhead() != 16 {
		return nil, fmt.Errorf("%w: unexpected GCM parameters", ErrUnsupported)
	}
	return gcm, nil
}

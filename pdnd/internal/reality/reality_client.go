package reality

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"golang.org/x/crypto/hkdf"
)

var defaultRealityClientVersion = []byte{26, 3, 27}

// applyRealityClientHello adds the Xray-compatible REALITY authentication
// envelope to a ClientHello. It is opt-in: a normal TLS/QUIC client is left
// unchanged unless PublicKey is configured.
func applyRealityClientHello(hello *clientHelloMsg, keys *keySharePrivateKeys, config *Config) ([]byte, error) {
	if len(config.PublicKey) == 0 {
		return nil, nil
	}
	if len(config.PublicKey) != x25519PublicKeySize {
		return nil, fmt.Errorf("REALITY: PublicKey must be %d bytes", x25519PublicKeySize)
	}
	if len(config.ShortId) > 8 {
		return nil, errors.New("REALITY: ShortId must be at most 8 bytes")
	}
	if keys == nil || keys.ecdhe == nil || len(keys.ecdhe.PublicKey().Bytes()) != x25519PublicKeySize {
		return nil, errors.New("REALITY: TLS 1.3 X25519 key share is required")
	}
	version := config.ClientVersion
	if len(version) == 0 {
		version = defaultRealityClientVersion
	}
	if len(version) != 3 {
		return nil, errors.New("REALITY: ClientVersion must be three bytes")
	}
	serverKey, err := ecdh.X25519().NewPublicKey(config.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("REALITY: invalid PublicKey: %w", err)
	}
	authKey, err := keys.ecdhe.ECDH(serverKey)
	if err != nil {
		return nil, fmt.Errorf("REALITY: derive client auth key: %w", err)
	}
	if len(hello.random) != 32 {
		return nil, errors.New("REALITY: ClientHello random has invalid length")
	}
	if _, err := hkdf.New(sha256.New, authKey, hello.random[:20], []byte("REALITY")).Read(authKey); err != nil {
		return nil, fmt.Errorf("REALITY: derive handshake key: %w", err)
	}
	block, err := aes.NewCipher(authKey)
	if err != nil {
		return nil, fmt.Errorf("REALITY: auth cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("REALITY: auth AEAD: %w", err)
	}
	// QUIC normally omits the TLS compatibility session ID. REALITY uses the
	// same 32-byte envelope as the TCP client, so deliberately restore it.
	plain := make([]byte, 32)
	copy(plain, version)
	binary.BigEndian.PutUint32(plain[4:], uint32(time.Now().Unix()))
	copy(plain[8:], config.ShortId)
	// Xray deliberately keeps the fixed session-ID area zeroed in the AAD and
	// replaces it with the encrypted envelope only after sealing.
	hello.sessionId = make([]byte, 32)
	original, err := hello.marshal()
	if err != nil {
		return nil, fmt.Errorf("REALITY: marshal ClientHello: %w", err)
	}
	sealed := aead.Seal(nil, hello.random[20:], plain[:16], original)
	if len(sealed) != 32 {
		return nil, errors.New("REALITY: auth envelope has invalid length")
	}
	copy(hello.sessionId, sealed)
	return authKey, nil
}

func verifyRealityPeerCertificate(rawCerts [][]byte, authKey []byte) error {
	if len(rawCerts) == 0 || len(authKey) == 0 {
		return errors.New("REALITY: peer certificate is missing")
	}
	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("REALITY: parse peer certificate: %w", err)
	}
	pub, ok := cert.PublicKey.(ed25519.PublicKey)
	if !ok || len(pub) != ed25519.PublicKeySize {
		return errors.New("REALITY: peer certificate is not a REALITY certificate")
	}
	h := hmac.New(sha512.New, authKey)
	_, _ = h.Write(pub)
	if !hmac.Equal(h.Sum(nil), cert.Signature) {
		return errors.New("REALITY: peer certificate authentication failed")
	}
	return nil
}

// installRealityClientVerifier keeps REALITY verification fail-closed while
// allowing a caller-provided certificate hook to run after the native check.
func (c *Conn) installRealityClientVerifier() {
	if len(c.config.PublicKey) == 0 {
		return
	}
	previous := c.config.VerifyPeerCertificate
	c.config.InsecureSkipVerify = true
	c.config.VerifyPeerCertificate = func(rawCerts [][]byte, verifiedChains [][]*x509.Certificate) error {
		if err := verifyRealityPeerCertificate(rawCerts, c.AuthKey); err != nil {
			return err
		}
		if previous != nil {
			return previous(rawCerts, verifiedChains)
		}
		return nil
	}
}

// realityEnvelopeEqual is kept local to the package for focused unit tests
// without exposing authentication material through the public API.
func realityEnvelopeEqual(a, b []byte) bool { return bytes.Equal(a, b) }

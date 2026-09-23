package reality

import (
	"crypto/ecdh"
	"crypto/rand"
	"testing"
)

func TestRealityClientEnvelopeAuthenticates(t *testing.T) {
	serverKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var shortID [8]byte
	copy(shortID[:], []byte("pandora1"))
	clientHello := &clientHelloMsg{
		vers:                         VersionTLS12,
		random:                       make([]byte, 32),
		cipherSuites:                 []uint16{TLS_AES_128_GCM_SHA256},
		compressionMethods:           []uint8{compressionNone},
		serverName:                   "reality.test",
		supportedVersions:            []uint16{VersionTLS13},
		supportedCurves:              []CurveID{X25519},
		supportedPoints:              []uint8{pointFormatUncompressed},
		secureRenegotiationSupported: true,
		keyShares:                    []keyShare{{group: X25519, data: clientKey.PublicKey().Bytes()}},
	}
	if _, err := rand.Read(clientHello.random); err != nil {
		t.Fatal(err)
	}
	clientConfig := &Config{
		ServerName:    "reality.test",
		PublicKey:     serverKey.PublicKey().Bytes(),
		ShortId:       shortID[:],
		ClientVersion: []byte{26, 3, 27},
	}
	keys := &keySharePrivateKeys{curveID: X25519, ecdhe: clientKey}
	authKey, err := applyRealityClientHello(clientHello, keys, clientConfig)
	if err != nil {
		t.Fatal(err)
	}
	if len(authKey) != 32 || len(clientHello.sessionId) != 32 {
		t.Fatalf("unexpected REALITY envelope sizes: key=%d session=%d", len(authKey), len(clientHello.sessionId))
	}
	original, err := clientHello.marshal()
	if err != nil {
		t.Fatal(err)
	}
	decoded := new(clientHelloMsg)
	if !decoded.unmarshal(original) {
		t.Fatal("failed to decode generated ClientHello")
	}
	serverConfig := &Config{PrivateKey: serverKey.Bytes(), ShortIds: map[[8]byte]bool{shortID: true}}
	hs := &serverHandshakeStateTLS13{c: &Conn{}, clientHello: decoded}
	if err := authenticateHandoff(hs, serverConfig); err != nil {
		t.Fatalf("server rejected native client envelope: %v", err)
	}
	if hs.c.ClientShortId != shortID {
		t.Fatalf("short ID mismatch: got %x want %x", hs.c.ClientShortId, shortID)
	}
	if !realityEnvelopeEqual(hs.c.AuthKey, authKey) {
		t.Fatal("client and server derived different REALITY auth keys")
	}
}

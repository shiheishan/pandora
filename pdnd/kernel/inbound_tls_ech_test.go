package kernel

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"net"
	"testing"
)

func testECHKey(t *testing.T, publicName string) (config []byte, list []byte, priv []byte) {
	t.Helper()
	privKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate x25519 key: %v", err)
	}
	pub := privKey.PublicKey().Bytes()

	var configID [1]byte
	if _, err := rand.Read(configID[:]); err != nil {
		t.Fatalf("rand configID: %v", err)
	}

	nameBytes := []byte(publicName)
	contentsLen := 1 + 2 + 2 + 32 + 2 + 8 + 1 + 1 + len(nameBytes) + 2
	contents := make([]byte, contentsLen)
	off := 0
	contents[off] = configID[0]
	off++
	binary.BigEndian.PutUint16(contents[off:], 0x0020) // DHKEM(X25519)
	off += 2
	binary.BigEndian.PutUint16(contents[off:], 32)
	off += 2
	copy(contents[off:], pub)
	off += 32
	binary.BigEndian.PutUint16(contents[off:], 8)
	off += 2
	binary.BigEndian.PutUint16(contents[off:], 0x0001)
	off += 2
	binary.BigEndian.PutUint16(contents[off:], 0x0001)
	off += 2
	binary.BigEndian.PutUint16(contents[off:], 0x0001)
	off += 2
	binary.BigEndian.PutUint16(contents[off:], 0x0003)
	off += 2
	contents[off] = 0 // max_name_length
	off++
	contents[off] = byte(len(nameBytes))
	off++
	copy(contents[off:], nameBytes)
	off += len(nameBytes)
	binary.BigEndian.PutUint16(contents[off:], 0) // extensions

	config = make([]byte, 4+len(contents))
	binary.BigEndian.PutUint16(config[0:], 0xfe0d)
	binary.BigEndian.PutUint16(config[2:], uint16(len(contents)))
	copy(config[4:], contents)

	list = make([]byte, 2+len(config))
	binary.BigEndian.PutUint16(list[0:], uint16(len(config)))
	copy(list[2:], config)

	return config, list, privKey.Bytes()
}

func TestInboundECHKeysValidation(t *testing.T) {
	cfg1, _, priv1 := testECHKey(t, "public.example.com")
	cfg2, _, priv2 := testECHKey(t, "public2.example.com")

	cfg1B64 := base64.StdEncoding.EncodeToString(cfg1)
	priv1B64 := base64.StdEncoding.EncodeToString(priv1)
	cfg2B64 := base64.StdEncoding.EncodeToString(cfg2)
	priv2B64 := base64.StdEncoding.EncodeToString(priv2)

	// 缺席或 nil 或空数组
	for _, raw := range []map[string]any{
		nil,
		{},
		{"ech_keys": nil},
		{"ech_keys": []any{}},
	} {
		keys, err := inboundECHKeys(raw)
		if err != nil {
			t.Fatalf("expected nil error, got %v for %#v", err, raw)
		}
		if keys != nil {
			t.Fatalf("expected nil keys, got %#v", keys)
		}
	}

	// 合法两项保持顺序与 SendAsRetry
	validRaw := map[string]any{
		"ech_keys": []any{
			map[string]any{
				"config":        cfg1B64,
				"private_key":   priv1B64,
				"send_as_retry": true,
			},
			map[string]any{
				"config":      cfg2B64,
				"private_key": priv2B64,
				// send_as_retry 缺席默认 false
			},
		},
	}
	keys, err := inboundECHKeys(validRaw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if string(keys[0].Config) != string(cfg1) || string(keys[0].PrivateKey) != string(priv1) || !keys[0].SendAsRetry {
		t.Fatalf("key 0 mismatch: %+v", keys[0])
	}
	if string(keys[1].Config) != string(cfg2) || string(keys[1].PrivateKey) != string(priv2) || keys[1].SendAsRetry {
		t.Fatalf("key 1 mismatch: %+v", keys[1])
	}

	// 错误场景
	shortKey := base64.StdEncoding.EncodeToString(make([]byte, 31))
	badVerCfg := append([]byte(nil), cfg1...)
	binary.BigEndian.PutUint16(badVerCfg[0:], 0xfe0c)
	badVerCfgB64 := base64.StdEncoding.EncodeToString(badVerCfg)

	badLenCfg := append([]byte(nil), cfg1...)
	binary.BigEndian.PutUint16(badLenCfg[2:], uint16(len(badLenCfg)-3)) // 长度不符
	badLenCfgB64 := base64.StdEncoding.EncodeToString(badLenCfg)

	tooShortCfgB64 := base64.StdEncoding.EncodeToString([]byte{0xfe, 0x0d, 0x00})

	nineKeys := make([]any, 9)
	for i := range nineKeys {
		nineKeys[i] = map[string]any{"config": cfg1B64, "private_key": priv1B64}
	}

	errorCases := []struct {
		name string
		raw  map[string]any
	}{
		{"not array", map[string]any{"ech_keys": "not-an-array"}},
		{"item not object", map[string]any{"ech_keys": []any{"string-item"}}},
		{"config empty", map[string]any{"ech_keys": []any{map[string]any{"config": "", "private_key": priv1B64}}}},
		{"config bad base64", map[string]any{"ech_keys": []any{map[string]any{"config": "!!!not-b64", "private_key": priv1B64}}}},
		{"config too short", map[string]any{"ech_keys": []any{map[string]any{"config": tooShortCfgB64, "private_key": priv1B64}}}},
		{"config bad version", map[string]any{"ech_keys": []any{map[string]any{"config": badVerCfgB64, "private_key": priv1B64}}}},
		{"config length mismatch", map[string]any{"ech_keys": []any{map[string]any{"config": badLenCfgB64, "private_key": priv1B64}}}},
		{"priv empty", map[string]any{"ech_keys": []any{map[string]any{"config": cfg1B64, "private_key": ""}}}},
		{"priv bad base64", map[string]any{"ech_keys": []any{map[string]any{"config": cfg1B64, "private_key": "bad-b64!"}}}},
		{"priv 31 bytes", map[string]any{"ech_keys": []any{map[string]any{"config": cfg1B64, "private_key": shortKey}}}},
		{"send_as_retry not bool", map[string]any{"ech_keys": []any{map[string]any{"config": cfg1B64, "private_key": priv1B64, "send_as_retry": "yes"}}}},
		{"9 items exceeded max", map[string]any{"ech_keys": nineKeys}},
	}

	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := inboundECHKeys(tc.raw)
			if err == nil {
				t.Fatalf("expected error for case %q, got nil", tc.name)
			}
		})
	}
}

func TestLoadInboundTLSConfigRegressionWithoutECH(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	cfg, ok, err := loadInboundTLSConfig(map[string]any{
		"tls":       true,
		"cert_path": certPath,
		"key_path":  keyPath,
	})
	if err != nil || !ok {
		t.Fatalf("loadInboundTLSConfig failed: ok=%v, err=%v", ok, err)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("expected MinVersion TLS 1.2 without ECH, got %x", cfg.MinVersion)
	}
	if len(cfg.EncryptedClientHelloKeys) != 0 {
		t.Fatalf("expected 0 EncryptedClientHelloKeys, got %d", len(cfg.EncryptedClientHelloKeys))
	}
}

func TestInboundTLSECHEndToEndHandshake(t *testing.T) {
	certPath, keyPath := testXHTTPServerCertFiles(t)
	// 公开名要有两段以上：客户端跳过 localhost 这类单段名，整份配置无效
	cfgBytes, listBytes, privBytes := testECHKey(t, "ech.example.test")

	raw := map[string]any{
		"tls":       true,
		"cert_path": certPath,
		"key_path":  keyPath,
		"ech_keys": []any{
			map[string]any{
				"config":        base64.StdEncoding.EncodeToString(cfgBytes),
				"private_key":   base64.StdEncoding.EncodeToString(privBytes),
				"send_as_retry": true,
			},
		},
	}

	serverCfg, ok, err := loadInboundTLSConfig(raw)
	if err != nil || !ok {
		t.Fatalf("loadInboundTLSConfig: ok=%v, err=%v", ok, err)
	}
	if serverCfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("expected MinVersion TLS 1.3 with ECH, got %x", serverCfg.MinVersion)
	}
	if len(serverCfg.EncryptedClientHelloKeys) != 1 {
		t.Fatalf("expected 1 EncryptedClientHelloKey, got %d", len(serverCfg.EncryptedClientHelloKeys))
	}

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	serverTLS := tls.Server(serverConn, serverCfg)
	clientTLS := tls.Client(clientConn, &tls.Config{
		ServerName:                     "localhost",
		EncryptedClientHelloConfigList: listBytes,
		MinVersion:                     tls.VersionTLS13,
		InsecureSkipVerify:             true, //nolint:gosec // 测试环境自签证书
	})

	errCh := make(chan error, 2)
	go func() {
		errCh <- serverTLS.Handshake()
	}()
	go func() {
		errCh <- clientTLS.Handshake()
	}()

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("handshake error %d: %v", i, err)
		}
	}

	cs := clientTLS.ConnectionState()
	if !cs.ECHAccepted {
		t.Fatalf("expected ECHAccepted to be true, got false")
	}
}

package upgrade

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// helperSign 构造签名文件 JSON 字节。
func helperSign(t *testing.T, priv ed25519.PrivateKey, pub ed25519.PublicKey, data []byte) []byte {
	t.Helper()
	sig := ed25519.Sign(priv, data)
	sf := SignatureFile{
		KeyID: KeyID(pub),
		Sig:   base64.StdEncoding.EncodeToString(sig),
	}
	raw, err := json.Marshal(sf)
	if err != nil {
		t.Fatalf("marshal signature failed: %v", err)
	}
	return raw
}

// 正常情况测试：验签、解析、获取架构 artifact、serial 检查全流程成功。
func TestCheck_Normal(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key failed: %v", err)
	}

	m := Manifest{
		Schema:  1,
		Version: "v2026.10.21",
		Serial:  2000,
		Artifacts: map[string]Artifact{
			"linux-amd64": {
				File:   "pandora-native-linux-amd64",
				SHA256: strings.Repeat("a", 64),
			},
			"linux-arm64": {
				File:   "pandora-native-linux-arm64",
				SHA256: strings.Repeat("b", 64),
			},
		},
	}
	manifestBytes, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal manifest failed: %v", err)
	}

	sigBytes := helperSign(t, priv, pub, manifestBytes)
	ring := NewKeyring(pub)

	parsed, art, err := Check(ring, manifestBytes, sigBytes, 1000, "amd64")
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}

	if parsed.Version != "v2026.10.21" || parsed.Serial != 2000 {
		t.Fatalf("unexpected parsed manifest: %+v", parsed)
	}
	if art.File != "pandora-native-linux-amd64" || art.SHA256 != strings.Repeat("a", 64) {
		t.Fatalf("unexpected artifact: %+v", art)
	}
}

// 签名异常用例：篡改、不同密钥、未知 key_id、非法格式、空钥匙环等。
func TestCheck_SignatureInvalid(t *testing.T) {
	pub1, priv1, _ := ed25519.GenerateKey(rand.Reader)
	_, priv2, _ := ed25519.GenerateKey(rand.Reader)

	validManifest := []byte(`{"schema":1,"version":"v1","serial":200,"artifacts":{"linux-amd64":{"file":"bin","sha256":"` + strings.Repeat("0", 64) + `"}}}`)
	validSig := helperSign(t, priv1, pub1, validManifest)
	ring := NewKeyring(pub1)

	// 构造篡改的 manifest 字节
	tamperedManifest := make([]byte, len(validManifest))
	copy(tamperedManifest, validManifest)
	tamperedManifest[len(tamperedManifest)-1] = ' ' // 虽仍是空白但签名已失效

	// 构造另一把私钥签署的 sig
	wrongKeySig := helperSign(t, priv2, pub1, validManifest)

	// 构造未知的 key_id
	var sf SignatureFile
	_ = json.Unmarshal(validSig, &sf)
	sfUnknown := SignatureFile{
		KeyID: "0123456789abcdef",
		Sig:   sf.Sig,
	}
	unknownKeySig, _ := json.Marshal(sfUnknown)

	// 构造非法的 key_id 格式（大写或非 16 字符）
	sfBadKeyID := SignatureFile{
		KeyID: "0123456789ABCDEF",
		Sig:   sf.Sig,
	}
	badKeyIDSig, _ := json.Marshal(sfBadKeyID)

	// 构造坏 base64
	sfBadB64 := SignatureFile{
		KeyID: KeyID(pub1),
		Sig:   "not-valid-base64!!!",
	}
	badB64Sig, _ := json.Marshal(sfBadB64)

	// 构造长度不合规的签名（非 64 字节）
	sfBadLen := SignatureFile{
		KeyID: KeyID(pub1),
		Sig:   base64.StdEncoding.EncodeToString([]byte("too-short-signature")),
	}
	badLenSig, _ := json.Marshal(sfBadLen)

	tests := []struct {
		name     string
		ring     *Keyring
		manifest []byte
		sig      []byte
	}{
		{
			name:     "tampered manifest byte",
			ring:     ring,
			manifest: tamperedManifest,
			sig:      validSig,
		},
		{
			name:     "signed with another private key",
			ring:     ring,
			manifest: validManifest,
			sig:      wrongKeySig,
		},
		{
			name:     "unknown key_id",
			ring:     ring,
			manifest: validManifest,
			sig:      unknownKeySig,
		},
		{
			name:     "bad key_id hex format",
			ring:     ring,
			manifest: validManifest,
			sig:      badKeyIDSig,
		},
		{
			name:     "sig not json",
			ring:     ring,
			manifest: validManifest,
			sig:      []byte("not json"),
		},
		{
			name:     "sig bad base64",
			ring:     ring,
			manifest: validManifest,
			sig:      badB64Sig,
		},
		{
			name:     "sig wrong length",
			ring:     ring,
			manifest: validManifest,
			sig:      badLenSig,
		},
		{
			name:     "empty keyring",
			ring:     NewKeyring(),
			manifest: validManifest,
			sig:      validSig,
		},
		{
			name:     "nil keyring",
			ring:     nil,
			manifest: validManifest,
			sig:      validSig,
		},
		{
			name:     "sig exceeds max bytes",
			ring:     ring,
			manifest: validManifest,
			sig:      make([]byte, MaxSignatureBytes+1),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Check(tc.ring, tc.manifest, tc.sig, 100, "amd64")
			code, _ := ReasonOf(err, "")
			if code != ReasonSignatureInvalid {
				t.Fatalf("expected code %s, got %q (err: %v)", ReasonSignatureInvalid, code, err)
			}
		})
	}
}

// 清单内容非法用例：格式、schema、版本、hash、多余内容、大小上限、架构缺失等。
// 这些用例均附带合法签名，确保是在 ParseManifest 或 Artifact 检查步被拒绝。
func TestCheck_ManifestInvalid(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ring := NewKeyring(pub)

	tests := []struct {
		name     string
		manifest []byte
		goarch   string
	}{
		{
			name:     "schema is 2",
			manifest: []byte(`{"schema":2,"version":"v1","serial":200,"artifacts":{"linux-amd64":{"file":"bin","sha256":"` + strings.Repeat("0", 64) + `"}}}`),
			goarch:   "amd64",
		},
		{
			name:     "version is empty",
			manifest: []byte(`{"schema":1,"version":"","serial":200,"artifacts":{"linux-amd64":{"file":"bin","sha256":"` + strings.Repeat("0", 64) + `"}}}`),
			goarch:   "amd64",
		},
		{
			name:     "version exceeds 64 chars",
			manifest: []byte(`{"schema":1,"version":"` + strings.Repeat("v", 65) + `","serial":200,"artifacts":{"linux-amd64":{"file":"bin","sha256":"` + strings.Repeat("0", 64) + `"}}}`),
			goarch:   "amd64",
		},
		{
			name:     "serial is 0",
			manifest: []byte(`{"schema":1,"version":"v1","serial":0,"artifacts":{"linux-amd64":{"file":"bin","sha256":"` + strings.Repeat("0", 64) + `"}}}`),
			goarch:   "amd64",
		},
		{
			name:     "artifacts is empty",
			manifest: []byte(`{"schema":1,"version":"v1","serial":200,"artifacts":{}}`),
			goarch:   "amd64",
		},
		{
			name:     "sha256 uppercase",
			manifest: []byte(`{"schema":1,"version":"v1","serial":200,"artifacts":{"linux-amd64":{"file":"bin","sha256":"` + strings.Repeat("A", 64) + `"}}}`),
			goarch:   "amd64",
		},
		{
			name:     "sha256 length 63",
			manifest: []byte(`{"schema":1,"version":"v1","serial":200,"artifacts":{"linux-amd64":{"file":"bin","sha256":"` + strings.Repeat("a", 63) + `"}}}`),
			goarch:   "amd64",
		},
		{
			name:     "sha256 length 65",
			manifest: []byte(`{"schema":1,"version":"v1","serial":200,"artifacts":{"linux-amd64":{"file":"bin","sha256":"` + strings.Repeat("a", 65) + `"}}}`),
			goarch:   "amd64",
		},
		{
			name:     "file name invalid with path separator",
			manifest: []byte(`{"schema":1,"version":"v1","serial":200,"artifacts":{"linux-amd64":{"file":"../bin","sha256":"` + strings.Repeat("0", 64) + `"}}}`),
			goarch:   "amd64",
		},
		{
			name:     "trailing content after json",
			manifest: []byte(`{"schema":1,"version":"v1","serial":200,"artifacts":{"linux-amd64":{"file":"bin","sha256":"` + strings.Repeat("0", 64) + `"}}} extra`),
			goarch:   "amd64",
		},
		{
			name:     "missing target arch linux-amd64",
			manifest: []byte(`{"schema":1,"version":"v1","serial":200,"artifacts":{"linux-arm64":{"file":"bin","sha256":"` + strings.Repeat("0", 64) + `"}}}`),
			goarch:   "amd64",
		},
		{
			name: "exceeds MaxManifestBytes",
			manifest: []byte(`{"schema":1,"version":"v1","serial":200,"comment":"` +
				strings.Repeat("x", int(MaxManifestBytes)) +
				`","artifacts":{"linux-amd64":{"file":"bin","sha256":"` + strings.Repeat("0", 64) + `"}}}`),
			goarch: "amd64",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sig := helperSign(t, priv, pub, tc.manifest)
			_, _, err := Check(ring, tc.manifest, sig, 100, tc.goarch)
			code, _ := ReasonOf(err, "")
			if code != ReasonManifestInvalid {
				t.Fatalf("expected code %s, got %q (err: %v)", ReasonManifestInvalid, code, err)
			}
		})
	}
}

// 检查版本 serial：等于、小于、或 buildSerial 为 0 时均拒绝。
func TestCheck_SerialNotNewer(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ring := NewKeyring(pub)

	manifestBytes := []byte(`{"schema":1,"version":"v1","serial":100,"artifacts":{"linux-amd64":{"file":"bin","sha256":"` + strings.Repeat("0", 64) + `"}}}`)
	sigBytes := helperSign(t, priv, pub, manifestBytes)

	tests := []struct {
		name        string
		buildSerial uint64
	}{
		{
			name:        "serial equal to buildSerial",
			buildSerial: 100,
		},
		{
			name:        "serial less than buildSerial",
			buildSerial: 101,
		},
		{
			name:        "buildSerial is 0 (dev build)",
			buildSerial: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Check(ring, manifestBytes, sigBytes, tc.buildSerial, "amd64")
			code, _ := ReasonOf(err, "")
			if code != ReasonSerialNotNewer {
				t.Fatalf("expected code %s, got %q (err: %v)", ReasonSerialNotNewer, code, err)
			}
		})
	}
}

// LoadKeyring 测试：基于 fstest.MapFS 验证多公钥与非法文件处理。
func TestLoadKeyring(t *testing.T) {
	pub1, priv1, _ := ed25519.GenerateKey(rand.Reader)
	pub2, priv2, _ := ed25519.GenerateKey(rand.Reader)

	goodFS := fstest.MapFS{
		"keys/k1.pub": &fstest.MapFile{
			Data: []byte(base64.StdEncoding.EncodeToString(pub1) + "\n"),
		},
		"keys/k2.pub": &fstest.MapFile{
			Data: []byte("  " + base64.StdEncoding.EncodeToString(pub2) + "  \r\n"),
		},
	}

	ring, err := LoadKeyring(goodFS, "keys/*.pub")
	if err != nil {
		t.Fatalf("LoadKeyring failed: %v", err)
	}
	if ring.Len() != 2 {
		t.Fatalf("expected 2 keys, got %d", ring.Len())
	}

	// 验证两把钥匙均能成功通过 Verify
	data := []byte("manifest-payload")
	sig1 := helperSign(t, priv1, pub1, data)
	sig2 := helperSign(t, priv2, pub2, data)

	if err := ring.Verify(data, sig1); err != nil {
		t.Fatalf("verify key1 failed: %v", err)
	}
	if err := ring.Verify(data, sig2); err != nil {
		t.Fatalf("verify key2 failed: %v", err)
	}

	// 测试坏文件：base64 损坏
	badB64FS := fstest.MapFS{
		"keys/bad.pub": &fstest.MapFile{
			Data: []byte("bad-base64???"),
		},
	}
	if _, err := LoadKeyring(badB64FS, "keys/*.pub"); err == nil {
		t.Fatalf("expected error for bad base64")
	}

	// 测试坏文件：长度为 31 字节
	badLenFS := fstest.MapFS{
		"keys/badlen.pub": &fstest.MapFile{
			Data: []byte(base64.StdEncoding.EncodeToString(make([]byte, 31))),
		},
	}
	if _, err := LoadKeyring(badLenFS, "keys/*.pub"); err == nil {
		t.Fatalf("expected error for invalid key length")
	}
}

// BuiltinKeyring 测试：当前仓库无 .pub 文件时返回空 Keyring 与 nil 错误。
func TestBuiltinKeyring(t *testing.T) {
	ring, err := BuiltinKeyring()
	if err != nil {
		t.Fatalf("BuiltinKeyring failed: %v", err)
	}
	if ring == nil {
		t.Fatalf("expected non-nil keyring")
	}
	// 仓库中当前无 .pub，Len() 应当为 0
	if ring.Len() != 0 {
		t.Fatalf("expected empty keyring in current repo, got %d keys", ring.Len())
	}
}

// ReasonOf 测试：*Error、fmt.Errorf 包过的 *Error、普通 error、nil。
func TestReasonOf(t *testing.T) {
	// *Error
	err1 := Errorf(ReasonBusy, "正在处理")
	c1, d1 := ReasonOf(err1, "fallback")
	if c1 != ReasonBusy || d1 != "正在处理" {
		t.Fatalf("case 1 failed: got %q, %q", c1, d1)
	}

	// 被 fmt.Errorf 包过的 *Error
	err2 := fmt.Errorf("上层包装: %w", err1)
	c2, d2 := ReasonOf(err2, "fallback")
	if c2 != ReasonBusy || d2 != "正在处理" {
		t.Fatalf("case 2 failed: got %q, %q", c2, d2)
	}

	// 普通 error（返回 fallback）
	err3 := errors.New("底层网络故障")
	c3, d3 := ReasonOf(err3, ReasonDownloadFailed)
	if c3 != ReasonDownloadFailed || d3 != "底层网络故障" {
		t.Fatalf("case 3 failed: got %q, %q", c3, d3)
	}

	// nil
	c4, d4 := ReasonOf(nil, "fallback")
	if c4 != "" || d4 != "" {
		t.Fatalf("case 4 failed: got %q, %q", c4, d4)
	}
}

// ValidJobID 测试：UUID、空串、长串、非法字符校验。
func TestValidJobID(t *testing.T) {
	valid := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"job-1",
		"a",
		strings.Repeat("a", 64),
	}
	for _, id := range valid {
		if !ValidJobID(id) {
			t.Errorf("expected ValidJobID(%q) to be true", id)
		}
	}

	invalid := []string{
		"",                       // 空串
		strings.Repeat("a", 65),  // 超长
		"job/1",                  // 斜杠
		"job.1",                  // 点号
		"job 1",                  // 空格
		"job_1",                  // 下划线
	}
	for _, id := range invalid {
		if ValidJobID(id) {
			t.Errorf("expected ValidJobID(%q) to be false", id)
		}
	}
}

// ClampConfirm 测试：0、10、180、1000 以及边界值。
func TestClampConfirm(t *testing.T) {
	cases := []struct {
		input    uint32
		expected time.Duration
	}{
		{0, 180 * time.Second},
		{10, 30 * time.Second},
		{29, 30 * time.Second},
		{30, 30 * time.Second},
		{180, 180 * time.Second},
		{300, 300 * time.Second},
		{301, 300 * time.Second},
		{1000, 300 * time.Second},
	}

	for _, tc := range cases {
		got := ClampConfirm(tc.input)
		if got != tc.expected {
			t.Errorf("ClampConfirm(%d) = %v; want %v", tc.input, got, tc.expected)
		}
	}
}

// Error.Error 格式测试。
func TestErrorFormat(t *testing.T) {
	e1 := &Error{Code: "only_code"}
	if e1.Error() != "only_code" {
		t.Fatalf("unexpected error string: %s", e1.Error())
	}

	e2 := &Error{Code: "code", Detail: "detail"}
	if e2.Error() != "code: detail" {
		t.Fatalf("unexpected error string: %s", e2.Error())
	}
}

// KeyID 格式测试。
func TestKeyID(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key failed: %v", err)
	}
	kid := KeyID(pub)
	if len(kid) != 16 {
		t.Fatalf("expected key_id length 16, got %d (%s)", len(kid), kid)
	}
	if _, err := hex.DecodeString(kid); err != nil {
		t.Fatalf("key_id is not valid hex: %v", err)
	}
	if kid != strings.ToLower(kid) {
		t.Fatalf("key_id is not lowercase hex: %s", kid)
	}
}

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/aegispanel/nodeagent/upgrade"
)

// keyringOf 经生产的 upgrade.LoadKeyring 从内存文件系统装出公钥环（一把公钥一个 .pub 文件）。
func keyringOf(t *testing.T, pubs ...ed25519.PublicKey) *upgrade.Keyring {
	t.Helper()
	fsys := fstest.MapFS{}
	for i, pub := range pubs {
		fsys[fmt.Sprintf("k%d.pub", i)] = &fstest.MapFile{Data: []byte(base64.StdEncoding.EncodeToString(pub) + "\n")}
	}
	ring, err := upgrade.LoadKeyring(fsys, "*.pub")
	if err != nil {
		t.Fatal(err)
	}
	return ring
}

func validManifestJSON() string {
	return `{
  "schema": 1,
  "version": "v2026.10.21",
  "serial": 1792540800,
  "artifacts": {
    "linux-amd64": { "file": "pandora-native-linux-amd64", "sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" },
    "linux-arm64": { "file": "pandora-native-linux-arm64", "sha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" }
  }
}`
}

func TestGenerateKeyPair(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "testkey")

	var out bytes.Buffer
	if err := run([]string{"-gen", prefix}, &out); err != nil {
		t.Fatalf("第一次 -gen: %v", err)
	}

	keyPath := prefix + ".key"
	pubPath := prefix + ".pub"

	keyInfo, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat 私钥: %v", err)
	}
	if runtime.GOOS != "windows" && keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("私钥权限应为 0600，实际 %o", keyInfo.Mode().Perm())
	}

	pubInfo, err := os.Stat(pubPath)
	if err != nil {
		t.Fatalf("stat 公钥: %v", err)
	}
	if runtime.GOOS != "windows" && pubInfo.Mode().Perm() != 0o644 {
		t.Fatalf("公钥权限应为 0644，实际 %o", pubInfo.Mode().Perm())
	}

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), strings.TrimSpace(string(keyData))) {
		t.Fatal("stdout 不应包含私钥内容")
	}
	if !strings.Contains(out.String(), "key_id:") {
		t.Fatalf("stdout 应包含 key_id:，得到 %q", out.String())
	}

	if err := run([]string{"-gen", prefix}, &bytes.Buffer{}); err == nil {
		t.Fatal("同一前缀再次 -gen 应失败")
	}
}

func TestSignAndVerifyWithUpgradeKeyring(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "signkey")
	if err := run([]string{"-gen", prefix}, &bytes.Buffer{}); err != nil {
		t.Fatalf("-gen: %v", err)
	}

	manifestPath := filepath.Join(dir, "pdnd-manifest.json")
	manifest := []byte(validManifestJSON())
	if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
		t.Fatal(err)
	}

	sigPath := filepath.Join(dir, "pdnd-manifest.json.sig")
	if err := run([]string{"-key", prefix + ".key", "-in", manifestPath, "-out", sigPath}, &bytes.Buffer{}); err != nil {
		t.Fatalf("sign: %v", err)
	}

	sigFile, err := os.ReadFile(sigPath)
	if err != nil {
		t.Fatal(err)
	}

	pubBytes, err := os.ReadFile(prefix + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	pub, err := upgrade.ParsePublicKey(pubBytes)
	if err != nil {
		t.Fatalf("ParsePublicKey: %v", err)
	}

	kr := keyringOf(t, pub)
	if err := kr.Verify(manifest, sigFile); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	var sigObj struct {
		KeyID string `json:"key_id"`
		Sig   string `json:"sig"`
	}
	if err := json.Unmarshal(sigFile, &sigObj); err != nil {
		t.Fatal(err)
	}
	if sigObj.KeyID != upgrade.KeyID(pub) {
		t.Fatalf("key_id 不匹配: got %q want %q", sigObj.KeyID, upgrade.KeyID(pub))
	}

	// 改清单一个字节后验签应失败。
	tampered := append([]byte(nil), manifest...)
	tampered[10] ^= 0xff
	verr := kr.Verify(tampered, sigFile)
	if verr == nil {
		t.Fatal("篡改后验签应失败")
	}
	code, _ := upgrade.ReasonOf(verr, "")
	if code != upgrade.ReasonSignatureInvalid {
		t.Fatalf("原因码应为 signature_invalid，得到 %q", code)
	}
}

func TestSignRejectsBadInputs(t *testing.T) {
	dir := t.TempDir()
	prefix := filepath.Join(dir, "badkey")
	if err := run([]string{"-gen", prefix}, &bytes.Buffer{}); err != nil {
		t.Fatalf("-gen: %v", err)
	}

	manifestPath := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifestPath, []byte(validManifestJSON()), 0o644); err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(dir, "manifest.json.sig")

	if runtime.GOOS != "windows" {
		looseKey := filepath.Join(dir, "loose.key")
		keyData, _ := os.ReadFile(prefix + ".key")
		if err := os.WriteFile(looseKey, keyData, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := run([]string{"-key", looseKey, "-in", manifestPath, "-out", sigPath}, &bytes.Buffer{}); err == nil {
			t.Fatal("0644 私钥应拒绝")
		}
	}

	badB64Key := filepath.Join(dir, "badb64.key")
	if err := os.WriteFile(badB64Key, []byte("not-valid-base64!!!\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-key", badB64Key, "-in", manifestPath, "-out", sigPath}, &bytes.Buffer{}); err == nil {
		t.Fatal("坏 base64 私钥应拒绝")
	}

	shortKey := filepath.Join(dir, "short.key")
	short := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := os.WriteFile(shortKey, []byte(short+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-key", shortKey, "-in", manifestPath, "-out", sigPath}, &bytes.Buffer{}); err == nil {
		t.Fatal("长度不对私钥应拒绝")
	}

	goodKey := prefix + ".key"
	cases := []struct {
		name string
		body string
	}{
		{"serial 为 0", strings.Replace(validManifestJSON(), "1792540800", "0", 1)},
		{"schema 为 2", strings.Replace(validManifestJSON(), `"schema": 1`, `"schema": 2`, 1)},
		{"非 JSON", "not json"},
	}
	for _, tc := range cases {
		p := filepath.Join(dir, tc.name+".json")
		out := filepath.Join(dir, tc.name+".sig")
		if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := run([]string{"-key", goodKey, "-in", p, "-out", out}, &bytes.Buffer{}); err == nil {
			t.Fatalf("%s: 应拒绝签名", tc.name)
		}
		if _, err := os.Stat(out); err == nil {
			t.Fatalf("%s: 不应产生 .sig 文件", tc.name)
		} else if !os.IsNotExist(err) {
			t.Fatalf("%s: stat sig: %v", tc.name, err)
		}
	}
}

func TestRunFlagValidation(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "m.json")
	_ = os.WriteFile(manifestPath, []byte(validManifestJSON()), 0o644)
	sigPath := filepath.Join(dir, "m.sig")
	keyPath := filepath.Join(dir, "k.key")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	_ = os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600)

	if err := run([]string{"-key", keyPath, "-in", manifestPath}, &bytes.Buffer{}); err == nil {
		t.Fatal("缺少 -out 应报错")
	}
	if err := run([]string{"-gen", filepath.Join(dir, "x"), "-key", keyPath}, &bytes.Buffer{}); err == nil {
		t.Fatal("-gen 与 -key 混用应报错")
	}

	// 三个齐全时应能签（用临时钥）。
	if err := run([]string{"-key", keyPath, "-in", manifestPath, "-out", sigPath}, &bytes.Buffer{}); err != nil {
		t.Fatalf("合法参数应成功: %v", err)
	}
}

func TestVerifyAgainstKeyring(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "m.json")
	if err := os.WriteFile(manifestPath, []byte(validManifestJSON()), 0o644); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keyPath := filepath.Join(dir, "k.key")
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(priv)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sigPath := filepath.Join(dir, "m.sig")
	if err := run([]string{"-key", keyPath, "-in", manifestPath, "-out", sigPath}, &bytes.Buffer{}); err != nil {
		t.Fatalf("签名失败: %v", err)
	}

	if err := verify(manifestPath, sigPath, keyringOf(t, pub), &bytes.Buffer{}); err != nil {
		t.Fatalf("对得上的公钥应通过: %v", err)
	}
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := verify(manifestPath, sigPath, keyringOf(t, otherPub), &bytes.Buffer{}); err == nil {
		t.Fatal("别的公钥不应通过")
	}
	if err := verify(manifestPath, sigPath, keyringOf(t), &bytes.Buffer{}); err == nil {
		t.Fatal("空公钥环不应通过")
	}
	if err := os.WriteFile(manifestPath, []byte(validManifestJSON()+" "), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verify(manifestPath, sigPath, keyringOf(t, pub), &bytes.Buffer{}); err == nil {
		t.Fatal("清单改过一个字节不应通过")
	}

	if err := run([]string{"-verify", sigPath, "-in", manifestPath, "-key", keyPath}, &bytes.Buffer{}); err == nil {
		t.Fatal("-verify 与 -key 混用应报错")
	}
	if err := run([]string{"-verify", sigPath}, &bytes.Buffer{}); err == nil {
		t.Fatal("-verify 缺 -in 应报错")
	}
}

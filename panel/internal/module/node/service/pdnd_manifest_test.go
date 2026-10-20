package service

// 发布清单与目标版本（pdnd_manifest.go）的单测：不连库，用临时目录。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func writeDist(t *testing.T, dir string, files map[string][]byte) {
	t.Helper()
	for name, data := range files {
		if err := os.WriteFile(dir+"/"+name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func distSum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func manifestJSON(version string, serial uint64, arts map[string][2]string) []byte {
	artifacts := map[string]map[string]string{}
	for k, v := range arts {
		artifacts[k] = map[string]string{"file": v[0], "sha256": v[1]}
	}
	doc := map[string]interface{}{
		"schema":    1,
		"version":   version,
		"serial":    serial,
		"artifacts": artifacts,
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		panic(err)
	}
	return raw
}

func validSig() []byte {
	return []byte(`{"key_id":"test","sig":"abc"}`)
}

func twoArchManifest(amd64, arm64 []byte) []byte {
	return manifestJSON("v2026.10.21", 1792540800, map[string][2]string{
		"linux-amd64": {PdndArtifactAMD64, distSum(amd64)},
		"linux-arm64": {PdndArtifactARM64, distSum(arm64)},
	})
}

func TestPdndTarget_missingManifest(t *testing.T) {
	dir := t.TempDir()
	s := New(nil, nil, nil, nil)
	s.UsePdndDist(dir, "https://panel.example.com")
	got := s.PdndTarget(context.Background())
	if got.Available || got.Reason != UpgradeBlockManifestMissing {
		t.Fatalf("空目录: %+v", got)
	}

	s2 := New(nil, nil, nil, nil)
	s2.UsePdndDist("", "https://panel.example.com")
	got2 := s2.PdndTarget(context.Background())
	if got2.Reason != UpgradeBlockManifestMissing {
		t.Fatalf("空 dir: %+v", got2)
	}
}

func TestPdndTarget_missingSignature(t *testing.T) {
	dir := t.TempDir()
	writeDist(t, dir, map[string][]byte{
		PdndManifestFile: manifestJSON("v1", 1, map[string][2]string{
			"linux-amd64": {PdndArtifactAMD64, strings.Repeat("a", 64)},
		}),
	})
	s := New(nil, nil, nil, nil)
	s.UsePdndDist(dir, "https://panel.example.com")
	if got := s.PdndTarget(context.Background()); got.Reason != UpgradeBlockSignatureMissing {
		t.Fatalf("只有清单: %+v", got)
	}

	dir2 := t.TempDir()
	writeDist(t, dir2, map[string][]byte{
		PdndManifestFile: manifestJSON("v1", 1, map[string][2]string{
			"linux-amd64": {PdndArtifactAMD64, strings.Repeat("a", 64)},
		}),
		PdndSignatureFile: []byte("  \n\t"),
	})
	s2 := New(nil, nil, nil, nil)
	s2.UsePdndDist(dir2, "https://panel.example.com")
	if got := s2.PdndTarget(context.Background()); got.Reason != UpgradeBlockSignatureMissing {
		t.Fatalf("空白签名: %+v", got)
	}
}

func TestPdndTarget_manifestInvalid(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"not json", []byte("not-json")},
		{"schema 2", manifestJSON("v1", 1, map[string][2]string{"linux-amd64": {PdndArtifactAMD64, strings.Repeat("a", 64)}})},
	}
	// schema 2 override
	cases[1].body = func() []byte {
		raw := manifestJSON("v1", 1, map[string][2]string{
			"linux-amd64": {PdndArtifactAMD64, strings.Repeat("a", 64)},
		})
		var m map[string]interface{}
		_ = json.Unmarshal(raw, &m)
		m["schema"] = 2
		b, _ := json.Marshal(m)
		return b
	}()

	invalid := []struct {
		name string
		mk   func() []byte
	}{
		{"empty version", func() []byte {
			return manifestJSON("", 1, map[string][2]string{"linux-amd64": {PdndArtifactAMD64, strings.Repeat("a", 64)}})
		}},
		{"serial 0", func() []byte {
			return manifestJSON("v1", 0, map[string][2]string{"linux-amd64": {PdndArtifactAMD64, strings.Repeat("a", 64)}})
		}},
		{"sha upper", func() []byte {
			return manifestJSON("v1", 1, map[string][2]string{"linux-amd64": {PdndArtifactAMD64, strings.ToUpper(strings.Repeat("a", 64))}})
		}},
		{"file mismatch", func() []byte {
			return manifestJSON("v1", 1, map[string][2]string{"linux-amd64": {PdndArtifactARM64, strings.Repeat("a", 64)}})
		}},
		{"only riscv", func() []byte {
			return manifestJSON("v1", 1, map[string][2]string{"linux-riscv64": {"pandora-native-linux-riscv64", strings.Repeat("a", 64)}})
		}},
	}

	s := New(nil, nil, nil, nil)
	for _, tc := range cases {
		dir := t.TempDir()
		writeDist(t, dir, map[string][]byte{PdndManifestFile: tc.body, PdndSignatureFile: validSig()})
		s.UsePdndDist(dir, "https://panel.example.com")
		if got := s.PdndTarget(context.Background()); got.Reason != UpgradeBlockManifestInvalid {
			t.Fatalf("%s: %+v", tc.name, got)
		}
	}
	for _, tc := range invalid {
		dir := t.TempDir()
		writeDist(t, dir, map[string][]byte{PdndManifestFile: tc.mk(), PdndSignatureFile: validSig()})
		s.UsePdndDist(dir, "https://panel.example.com")
		if got := s.PdndTarget(context.Background()); got.Reason != UpgradeBlockManifestInvalid {
			t.Fatalf("%s: %+v", tc.name, got)
		}
	}
}

func TestPdndTarget_bothAvailable(t *testing.T) {
	dir := t.TempDir()
	amd64 := []byte("amd64-bin")
	arm64 := []byte("arm64-bin")
	manifest := twoArchManifest(amd64, arm64)
	sig := validSig()
	writeDist(t, dir, map[string][]byte{
		PdndManifestFile:  manifest,
		PdndSignatureFile: sig,
		PdndArtifactAMD64: amd64,
		PdndArtifactARM64: arm64,
	})
	s := New(nil, nil, nil, nil)
	s.UsePdndDist(dir, "https://panel.example.com")
	got := s.PdndTarget(context.Background())
	if !got.Available || got.Reason != "" || got.Version != "v2026.10.21" || got.Serial != 1792540800 {
		t.Fatalf("汇总: %+v", got)
	}
	if !bytes.Equal(got.Manifest, manifest) || !bytes.Equal(got.Signature, sig) {
		t.Fatal("原字节应一致")
	}
	for _, key := range []string{"linux-amd64", "linux-arm64"} {
		a := got.Artifacts[key]
		if !a.Available || a.Reason != "" {
			t.Fatalf("%s: %+v", key, a)
		}
	}
}

func TestPdndTarget_skipUnknownArch(t *testing.T) {
	dir := t.TempDir()
	amd64 := []byte("amd64-bin")
	arm64 := []byte("arm64-bin")
	manifest := twoArchManifest(amd64, arm64)
	var doc map[string]interface{}
	_ = json.Unmarshal(manifest, &doc)
	arts := doc["artifacts"].(map[string]interface{})
	arts["linux-riscv64"] = map[string]string{
		"file":   "pandora-native-linux-riscv64",
		"sha256": strings.Repeat("b", 64),
	}
	manifest, _ = json.Marshal(doc)
	writeDist(t, dir, map[string][]byte{
		PdndManifestFile:  manifest,
		PdndSignatureFile: validSig(),
		PdndArtifactAMD64: amd64,
		PdndArtifactARM64: arm64,
	})
	s := New(nil, nil, nil, nil)
	s.UsePdndDist(dir, "https://panel.example.com")
	got := s.PdndTarget(context.Background())
	if _, ok := got.Artifacts["linux-riscv64"]; ok || !got.Available || len(got.Artifacts) != 2 {
		t.Fatalf("应跳过 riscv: %+v", got)
	}
}

func TestPdndTarget_partialBinary(t *testing.T) {
	dir := t.TempDir()
	amd64 := []byte("amd64-bin")
	manifest := twoArchManifest(amd64, []byte("arm64-bin"))
	writeDist(t, dir, map[string][]byte{
		PdndManifestFile:  manifest,
		PdndSignatureFile: validSig(),
		PdndArtifactAMD64: amd64,
	})
	s := New(nil, nil, nil, nil)
	s.UsePdndDist(dir, "https://panel.example.com")
	got := s.PdndTarget(context.Background())
	if !got.Available {
		t.Fatalf("amd64 对上应整体可用: %+v", got)
	}
	arm := got.Artifacts["linux-arm64"]
	if arm.Available || arm.Reason != UpgradeBlockBinaryMismatch {
		t.Fatalf("arm64: %+v", arm)
	}
}

func TestPdndTarget_binaryMismatchAll(t *testing.T) {
	dir := t.TempDir()
	amd64 := []byte("wrong-amd64")
	arm64 := []byte("wrong-arm64")
	manifest := twoArchManifest([]byte("right-amd64"), []byte("right-arm64"))
	writeDist(t, dir, map[string][]byte{
		PdndManifestFile:  manifest,
		PdndSignatureFile: validSig(),
		PdndArtifactAMD64: amd64,
		PdndArtifactARM64: arm64,
	})
	s := New(nil, nil, nil, nil)
	s.UsePdndDist(dir, "https://panel.example.com")
	got := s.PdndTarget(context.Background())
	if got.Available || got.Reason != UpgradeBlockBinaryMismatch || got.Version != "v2026.10.21" {
		t.Fatalf("%+v", got)
	}
}

func TestPdndTarget_fileCacheRefresh(t *testing.T) {
	dir := t.TempDir()
	amd64 := []byte("bin")
	writeDist(t, dir, map[string][]byte{
		PdndManifestFile:  manifestJSON("v1", 100, map[string][2]string{"linux-amd64": {PdndArtifactAMD64, distSum(amd64)}}),
		PdndSignatureFile: validSig(),
		PdndArtifactAMD64: amd64,
	})
	s := New(nil, nil, nil, nil)
	s.UsePdndDist(dir, "https://panel.example.com")
	ctx := context.Background()
	if got := s.PdndTarget(ctx); got.Version != "v1" {
		t.Fatalf("v1: %+v", got)
	}
	manifest2 := manifestJSON("v2", 200, map[string][2]string{"linux-amd64": {PdndArtifactAMD64, distSum(amd64)}})
	path := dir + "/" + PdndManifestFile
	if err := os.WriteFile(path, manifest2, 0o600); err != nil {
		t.Fatal(err)
	}
	newTime := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(path, newTime, newTime); err != nil {
		t.Fatal(err)
	}
	if got := s.PdndTarget(ctx); got.Version != "v2" {
		t.Fatalf("v2: %+v", got)
	}
}

func TestPdndTarget_manifestTooLarge(t *testing.T) {
	dir := t.TempDir()
	amd64 := []byte("x")
	base := manifestJSON("v1", 1, map[string][2]string{"linux-amd64": {PdndArtifactAMD64, distSum(amd64)}})
	padding := make([]byte, pdndManifestMaxBytes-int64(len(base))+1)
	for i := range padding {
		padding[i] = ' '
	}
	writeDist(t, dir, map[string][]byte{
		PdndManifestFile:  append(base, padding...),
		PdndSignatureFile: validSig(),
		PdndArtifactAMD64: amd64,
	})
	s := New(nil, nil, nil, nil)
	s.UsePdndDist(dir, "https://panel.example.com")
	got := s.PdndTarget(context.Background())
	if got.Reason != UpgradeBlockManifestMissing {
		t.Fatalf("过大: %+v", got)
	}
}

func TestPdndArchKey(t *testing.T) {
	if pdndArchKey(" AMD64 ") != "linux-amd64" {
		t.Fatal(pdndArchKey(" AMD64 "))
	}
}

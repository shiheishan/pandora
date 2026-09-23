package dbbackup

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadWebDAVRuntimeConfigIncludesManifestState(t *testing.T) {
	dir := secureTempDir(t)
	withCheckpointHookRoot(t, dir)
	passwordPath := filepath.Join(dir, "webdav.password")
	if err := os.WriteFile(passwordPath, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "manifest.seed")
	checkpointPath := filepath.Join(dir, "latest.manifest.json")
	cfg := WebDAVFileConfig{
		Endpoint: "https://public.example", BasePath: "/pandora", Username: "backup", PasswordFile: passwordPath,
		ManifestSigningKeyFile: keyPath, ManifestCheckpointFile: checkpointPath,
		CheckpointReplicationHook: filepath.Join(dir, "replicate-checkpoint"),
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "backup-webdav.json")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}}
	got, err := loadWebDAVRuntimeConfig(context.Background(), resolver, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got.ManifestSigningKeyFile != keyPath || got.ManifestCheckpointFile != checkpointPath || got.Target.BasePath != "/pandora" {
		t.Fatalf("config=%+v", got)
	}
}

func TestLoadWebDAVRuntimeConfigRejectsPasswordAndSigningKeyAlias(t *testing.T) {
	dir := secureTempDir(t)
	shared := filepath.Join(dir, "shared-secret")
	if err := os.WriteFile(shared, []byte("AEPB-ED25519-SEED-V1 invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := WebDAVFileConfig{
		Endpoint: "https://public.example", BasePath: "/pandora", PasswordFile: shared,
		ManifestSigningKeyFile: shared, ManifestCheckpointFile: filepath.Join(dir, "checkpoint"),
		CheckpointReplicationHook: filepath.Join(dir, "replicate-checkpoint"),
	}
	raw, _ := json.Marshal(cfg)
	configPath := filepath.Join(dir, "backup-webdav.json")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := staticResolver{"public.example": {netip.MustParseAddr("8.8.8.8")}}
	if _, err := loadWebDAVRuntimeConfig(context.Background(), resolver, configPath); err == nil {
		t.Fatal("password file and signing seed alias succeeded")
	}
}

func TestDistinctSecurePathsRejectsHardLinkedSecrets(t *testing.T) {
	dir := secureTempDir(t)
	password := filepath.Join(dir, "password")
	seed := filepath.Join(dir, "seed")
	if err := os.WriteFile(password, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(password, seed); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if err := ensureDistinctSecurePaths(password, seed); err == nil {
		t.Fatal("hard-linked secrets were accepted")
	}
}

func TestVerifyLocalPair(t *testing.T) {
	dir := secureTempDir(t)
	archive := filepath.Join(dir, "aegis-postgres-20260801T031700Z.dump.age")
	payload := []byte("encrypted")
	if err := os.WriteFile(archive, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	checksum := archive + ".sha256"
	if err := os.WriteFile(checksum, []byte(fmt.Sprintf("%x  %s\n", sum, filepath.Base(archive))), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := VerifyLocalPair(archive, checksum)
	if err != nil || got.ArchiveSHA256 != fmt.Sprintf("%x", sum) || len(got.ChecksumSHA256) != 64 {
		t.Fatalf("pair=%+v err=%v", got, err)
	}
	if err := os.WriteFile(checksum, []byte(fmt.Sprintf("%064x  %s\n", 1, filepath.Base(archive))), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyLocalPair(archive, checksum); err == nil {
		t.Fatal("mismatched checksum succeeded")
	}
}

func TestReadPrivateFileRejectsLooseModeAndSymlink(t *testing.T) {
	dir := secureTempDir(t)
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(secret, 0o644); err == nil && runtime.GOOS == "linux" {
		if _, err := readPrivateFile(secret, 32); err == nil {
			t.Fatal("loose private file mode succeeded")
		}
	}
	link := filepath.Join(dir, "secret-link")
	if err := os.Symlink(secret, link); err == nil {
		if _, err := readPrivateFile(link, 32); err == nil {
			t.Fatal("private file symlink succeeded")
		}
	}
}

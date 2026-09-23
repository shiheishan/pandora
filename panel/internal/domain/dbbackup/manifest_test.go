package dbbackup

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func manifestTestPair(backupID string) LocalPair {
	archive := []byte("encrypted-archive-" + backupID)
	checksum := []byte("checksum-" + backupID)
	archiveSum := sha256.Sum256(archive)
	checksumSum := sha256.Sum256(checksum)
	archiveName := "aegis-postgres-" + backupID + ".dump.age"
	return LocalPair{
		BackupID: backupID,
		Archive:  ArtifactRef{Name: archiveName, Bytes: int64(len(archive)), SHA256: hex.EncodeToString(archiveSum[:])},
		Checksum: ArtifactRef{Name: archiveName + ".sha256", Bytes: int64(len(checksum)), SHA256: hex.EncodeToString(checksumSum[:])},
	}
}

func manifestTestPrivateKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
}

func TestSignedManifestIsDeterministicCanonicalAndVerifiable(t *testing.T) {
	pair := manifestTestPair("20260801T031700Z")
	privateKey := manifestTestPrivateKey()
	first, manifest, err := BuildSignedManifest(pair, nil, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := BuildSignedManifest(pair, nil, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || first[len(first)-1] != '\n' {
		t.Fatal("manifest is not deterministic canonical JSON")
	}
	verified, err := ParseAndVerifyManifest(first, privateKey.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if verified.Payload.Sequence != 1 || manifest.Payload.PreviousManifestSHA256 != zeroManifestHash {
		t.Fatalf("manifest=%+v", manifest.Payload)
	}
	modified := append([]byte(nil), first...)
	modified[bytes.Index(modified, []byte(pair.BackupID))] = '1'
	if _, err := ParseAndVerifyManifest(modified, privateKey.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("modified manifest verified")
	}
	if _, err := ParseAndVerifyManifest(append([]byte(" "), first...), privateKey.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("non-canonical manifest verified")
	}
}

func TestSignedManifestChainsAndSameBackupResumesExactly(t *testing.T) {
	privateKey := manifestTestPrivateKey()
	first, _, err := BuildSignedManifest(manifestTestPair("20260801T031700Z"), nil, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	second, secondManifest, err := BuildSignedManifest(manifestTestPair("20260802T031700Z"), first, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	wantPrevious := sha256.Sum256(first)
	if secondManifest.Payload.Sequence != 2 || secondManifest.Payload.PreviousManifestSHA256 != hex.EncodeToString(wantPrevious[:]) {
		t.Fatalf("second=%+v", secondManifest.Payload)
	}
	resumed, _, err := BuildSignedManifest(manifestTestPair("20260802T031700Z"), second, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(resumed, second) {
		t.Fatal("same backup did not resume with exact manifest bytes")
	}
	conflict := manifestTestPair("20260802T031700Z")
	conflict.Archive.Bytes++
	if _, _, err := BuildSignedManifest(conflict, second, privateKey); err == nil {
		t.Fatal("same backup ID with changed artifact succeeded")
	}
}

func TestManifestTransactionCommitsAndResumesCheckpoint(t *testing.T) {
	dir := secureTempDir(t)
	keyPath := filepath.Join(dir, "manifest.seed")
	seedLine := "AEPB-ED25519-SEED-V1 " + base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, ed25519.SeedSize)) + "\n"
	if err := os.WriteFile(keyPath, []byte(seedLine), 0o600); err != nil {
		t.Fatal(err)
	}
	checkpoint := filepath.Join(dir, "latest.manifest.json")
	first, err := BeginManifestTransaction(manifestTestPair("20260801T031700Z"), keyPath, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes := append([]byte(nil), first.Bytes...)
	if err := first.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := BeginManifestTransaction(manifestTestPair("20260801T031700Z"), keyPath, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if !bytes.Equal(firstBytes, second.Bytes) {
		t.Fatal("checkpoint resume changed manifest bytes")
	}
}

func TestVerifyRecoveryBundleRequiresExactTrustedCheckpoint(t *testing.T) {
	dir := secureTempDir(t)
	backupID := "20260801T031700Z"
	archive := filepath.Join(dir, "aegis-postgres-"+backupID+".dump.age")
	payload := []byte("encrypted-archive")
	if err := os.WriteFile(archive, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	archiveSum := sha256.Sum256(payload)
	checksum := archive + ".sha256"
	if err := os.WriteFile(checksum, []byte(fmt.Sprintf("%x  %s\n", archiveSum, filepath.Base(archive))), 0o600); err != nil {
		t.Fatal(err)
	}
	pair, err := VerifyLocalPair(archive, checksum)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := manifestTestPrivateKey()
	manifestRaw, _, err := BuildSignedManifest(pair, nil, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "aegis-postgres-"+backupID+".manifest.json")
	checkpointPath := filepath.Join(dir, "latest.manifest.json")
	publicKeyPath := filepath.Join(dir, "manifest.public")
	publicLine := "AEPB-ED25519-PUBLIC-V1 " + base64.RawStdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)) + "\n"
	for path, data := range map[string][]byte{manifestPath: manifestRaw, checkpointPath: manifestRaw, publicKeyPath: []byte(publicLine)} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := VerifyRecoveryBundle(archive, checksum, manifestPath, publicKeyPath, checkpointPath); err != nil {
		t.Fatal(err)
	}
	changed := append([]byte(nil), manifestRaw...)
	changed[len(changed)-2] ^= 1
	if err := os.WriteFile(checkpointPath, changed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRecoveryBundle(archive, checksum, manifestPath, publicKeyPath, checkpointPath); err == nil {
		t.Fatal("modified checkpoint accepted")
	}
}

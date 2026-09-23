//go:build linux && (amd64 || arm64)

package ca42credential

import (
	"bytes"
	"crypto/sha256"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSealedCommitmentKeyFDVerifiesWithoutExposingBytes(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned sealed key FD requires EUID 0")
	}
	now := time.Unix(1_700_000_100, 0).UTC()
	secret := bytes.Repeat([]byte{'s'}, 39)
	commitmentKey := bytes.Repeat([]byte{0x6a}, sha256.Size)
	descriptor := validCredentialDescriptorForKeyFD(t, secret, commitmentKey, now)
	key, err := newSealedCommitmentKeyFD(descriptor.CommitmentKeyID, commitmentKey)
	if err != nil {
		t.Fatal(err)
	}
	defer key.Close()
	if err := key.VerifyCredential(secret, descriptor, now); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.Pwrite(int(key.state.file.Fd()), bytes.Repeat([]byte{0x7b}, sha256.Size), 0); err == nil {
		t.Fatal("sealed commitment key FD accepted mutation")
	}
	otherDescriptor := validCredentialDescriptorForNamedKeyFD(t, secret, commitmentKey, "other-key", now)
	if err := key.VerifyCredential(secret, otherDescriptor, now); err == nil {
		t.Fatal("commitment key FD accepted another key ID")
	}
}

func TestCommitmentKeyFDShallowCopySharesCloseState(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root-owned sealed key FD requires EUID 0")
	}
	now := time.Unix(1_700_000_100, 0).UTC()
	secret := bytes.Repeat([]byte{'s'}, 39)
	commitmentKey := bytes.Repeat([]byte{0x6a}, sha256.Size)
	descriptor := validCredentialDescriptorForKeyFD(t, secret, commitmentKey, now)
	key, err := newSealedCommitmentKeyFD(descriptor.CommitmentKeyID, commitmentKey)
	if err != nil {
		t.Fatal(err)
	}
	copyKey := *key
	if err := key.Close(); err != nil {
		t.Fatal(err)
	}
	if err := copyKey.Close(); err != nil {
		t.Fatalf("shallow-copy close was not idempotent: %v", err)
	}
	if err := copyKey.VerifyCredential(secret, descriptor, now); err == nil {
		t.Fatal("shallow copy remained usable after shared close")
	}
}

func TestKernelKeyDescriptionRequiresRootPrivateUserKey(t *testing.T) {
	valid := "user;0;0;03010000;" + commitmentKeyDescriptionPrefix + "keyring-ca42"
	if err := validateKernelKeyDescription(valid, commitmentKeyDescriptionPrefix+"keyring-ca42"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"user;1;0;03010000;" + commitmentKeyDescriptionPrefix + "keyring-ca42",
		"user;0;0;3f010000;" + commitmentKeyDescriptionPrefix + "keyring-ca42",
		"user;0;0;03010001;" + commitmentKeyDescriptionPrefix + "keyring-ca42",
		"logon;0;0;03010000;" + commitmentKeyDescriptionPrefix + "keyring-ca42",
		"user;0;0;03010000;" + commitmentKeyDescriptionPrefix + "other",
	} {
		if err := validateKernelKeyDescription(value, commitmentKeyDescriptionPrefix+"keyring-ca42"); err == nil {
			t.Fatalf("accepted invalid key metadata %q", value)
		}
	}
}

func validCredentialDescriptorForKeyFD(t *testing.T, secret, key []byte, now time.Time) Descriptor {
	return validCredentialDescriptorForNamedKeyFD(t, secret, key, "keyring-ca42", now)
}

func validCredentialDescriptorForNamedKeyFD(t *testing.T, secret, key []byte, keyID string, now time.Time) Descriptor {
	t.Helper()
	raw := Descriptor{
		CommitmentAlgorithm: CommitmentHMACKeyringV1, CommitmentKeyID: keyID,
		ReleaseID: "release-ca42-1", ReleaseRunID: "run-ca42-1", AttemptID: "attempt-ca42-1",
		SourceContainerID: strings.Repeat("1", 64), SourceSystemIdentifier: "101",
		SourceDatabase: "pandora", SourceDatabaseOID: "102", SourceDatabaseOwner: "pandora_owner",
		SourceDatabaseOwnerOID: "103", CredentialSizeBytes: uint64(len(secret)),
	}
	commitment, err := HMACCommitment(secret, key, raw)
	if err != nil {
		t.Fatal(err)
	}
	values := []string{
		Format, Kind, CredentialName, CredentialFormat, CommitmentHMACKeyringV1, raw.CommitmentKeyID,
		commitment, Mode, "39", Delivery, raw.ReleaseID, raw.ReleaseRunID, raw.AttemptID,
		raw.SourceContainerID, raw.SourceSystemIdentifier, raw.SourceDatabase, raw.SourceDatabaseOID,
		raw.SourceDatabaseOwner, raw.SourceDatabaseOwnerOID, "1700000000", "1700003600",
	}
	data, err := CanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	descriptor, err := Parse(data, digest, now)
	if err != nil {
		t.Fatal(err)
	}
	return descriptor
}

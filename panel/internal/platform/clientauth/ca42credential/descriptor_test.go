package ca42credential

import (
	"bytes"
	"crypto/sha256"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCredentialDescriptorAndHMACCommitment(t *testing.T) {
	secret := bytes.Repeat([]byte{'s'}, 39)
	key := bytes32(42)
	descriptor := descriptorFixture()
	commitment, err := HMACCommitment(secret, key, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	data, err := CanonicalBytes(descriptorValues(descriptor, commitment))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	parsed, err := Parse(data, digest, time.Unix(1700000100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyHMACCredential(secret, key, parsed, time.Unix(1700000100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	mutatedProjection := parsed
	mutatedProjection.SourceDatabaseOID = "999"
	mutatedProjection.Commitment = "f"
	if err := VerifyHMACCredential(secret, key, mutatedProjection, time.Unix(1700000100, 0).UTC()); err != nil {
		t.Fatal("mutable descriptor projection influenced canonical verification")
	}
	secret[0] = 'A'
	if err := VerifyHMACCredential(secret, key, parsed, time.Unix(1700000100, 0).UTC()); err == nil {
		t.Fatal("mutated credential accepted")
	}
}

func TestCredentialDescriptorRejectsWeakOrMixedContracts(t *testing.T) {
	descriptor := descriptorFixture()
	secret := bytes.Repeat([]byte{'p'}, 39)
	commitment, err := HMACCommitment(secret, bytes32(7), descriptor)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := CanonicalBytes(descriptorValues(descriptor, commitment))
	tests := map[string]func([]byte) []byte{
		"legacy_format": func(value []byte) []byte { return []byte(strings.Replace(string(value), Format, "legacy", 1)) },
		"weak_format": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), CredentialFormat, "utf8-password-v1", 1))
		},
		"wrong_size": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "credential_size_bytes=39", "credential_size_bytes=12", 1))
		},
		"unkeyed_commitment": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), CommitmentHMACKeyringV1, "sha256-csprng-256-v1", 1))
		},
		"hmac_without_key_id": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "keyring-ca42", "none", 1))
		},
		"missing_database_oid": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "source_database_oid=16384", "source_database_oid=0", 1))
		},
		"path_delivery": func(value []byte) []byte { return []byte(strings.Replace(string(value), Delivery, "path-v1", 1)) },
		"old_namespace_attempt": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "attempt_id=attempt-1", "attempt_id=ATTEMPT", 1))
		},
		"crlf": func(value []byte) []byte { return []byte(strings.ReplaceAll(string(value), "\n", "\r\n")) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := mutate(append([]byte(nil), data...))
			digest := sha256.Sum256(candidate)
			if _, err := Parse(candidate, digest, time.Unix(1700000100, 0).UTC()); err == nil {
				t.Fatal("invalid descriptor accepted")
			}
		})
	}
}

func TestCredentialVerificationRejectsInvalidManualDescriptorWithoutPanic(t *testing.T) {
	descriptor := descriptorFixture()
	descriptor.Commitment = "f"
	if err := VerifyHMACCredential(bytes.Repeat([]byte{'x'}, 39), bytes32(1), descriptor, time.Unix(1700000100, 0).UTC()); err == nil {
		t.Fatal("short commitment accepted")
	}
	if _, err := HMACCommitment(bytes.Repeat([]byte{'x'}, 39), []byte("short"), descriptor); err == nil {
		t.Fatal("short HMAC key accepted")
	}
}

func TestCredentialVerificationRequiresParsedCurrentCapability(t *testing.T) {
	secret := bytes.Repeat([]byte{'s'}, 39)
	key := bytes32(42)
	raw := descriptorFixture()
	commitment, err := HMACCommitment(secret, key, raw)
	if err != nil {
		t.Fatal(err)
	}
	raw.Commitment = commitment
	if err := VerifyHMACCredential(secret, key, raw, time.Unix(1700000100, 0).UTC()); err == nil {
		t.Fatal("manually constructed descriptor accepted")
	}
	data, _ := CanonicalBytes(descriptorValues(raw, commitment))
	digest := sha256.Sum256(data)
	parsed, err := Parse(data, digest, time.Unix(1700000100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	for name, now := range map[string]time.Time{
		"before":  time.Unix(1699999999, 0).UTC(),
		"expired": time.Unix(1700003600, 0).UTC(),
	} {
		t.Run(name, func(t *testing.T) {
			if err := VerifyHMACCredential(secret, key, parsed, now); err == nil {
				t.Fatal("credential verified outside descriptor window")
			}
		})
	}
}

func TestCredentialVerificationBindsKeyAndSizeBoundaries(t *testing.T) {
	for _, size := range []int{MinCredentialBytes, MaxCredentialBytes} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			secret := bytes.Repeat([]byte{'q'}, size)
			key := bytes32(3)
			raw := descriptorFixture()
			raw.CredentialSizeBytes = uint64(size)
			commitment, err := HMACCommitment(secret, key, raw)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := CanonicalBytes(descriptorValues(raw, commitment))
			digest := sha256.Sum256(data)
			parsed, err := Parse(data, digest, time.Unix(1700000100, 0).UTC())
			if err != nil {
				t.Fatal(err)
			}
			if err := VerifyHMACCredential(secret, key, parsed, time.Unix(1700000100, 0).UTC()); err != nil {
				t.Fatal(err)
			}
			wrongKey := append([]byte(nil), key...)
			wrongKey[0] ^= 0xff
			if err := VerifyHMACCredential(secret, wrongKey, parsed, time.Unix(1700000100, 0).UTC()); err == nil {
				t.Fatal("wrong commitment key accepted")
			}
		})
	}
}

func descriptorFixture() Descriptor {
	return Descriptor{
		CommitmentAlgorithm: CommitmentHMACKeyringV1, CommitmentKeyID: "keyring-ca42",
		ReleaseID: "release-1", ReleaseRunID: "run-1", AttemptID: "attempt-1",
		SourceContainerID: strings.Repeat("a", 64), SourceSystemIdentifier: "100",
		SourceDatabase: "pandora", SourceDatabaseOID: "16384",
		SourceDatabaseOwner: "pandora_owner", SourceDatabaseOwnerOID: "16385", CredentialSizeBytes: 39,
		NotBefore: time.Unix(1700000000, 0).UTC(), NotAfter: time.Unix(1700003600, 0).UTC(),
	}
}

func descriptorValues(descriptor Descriptor, commitment string) []string {
	return []string{
		Format, Kind, CredentialName, CredentialFormat, descriptor.CommitmentAlgorithm,
		descriptor.CommitmentKeyID, commitment, Mode, strconv.FormatUint(descriptor.CredentialSizeBytes, 10), Delivery,
		descriptor.ReleaseID, descriptor.ReleaseRunID, descriptor.AttemptID, descriptor.SourceContainerID,
		descriptor.SourceSystemIdentifier, descriptor.SourceDatabase, descriptor.SourceDatabaseOID,
		descriptor.SourceDatabaseOwner, descriptor.SourceDatabaseOwnerOID,
		"1700000000", "1700003600",
	}
}

func bytes32(value byte) []byte {
	result := make([]byte, 32)
	for index := range result {
		result[index] = value + byte(index)
	}
	return result
}

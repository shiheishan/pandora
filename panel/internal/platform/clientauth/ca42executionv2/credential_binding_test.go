package ca42executionv2

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42credential"
)

func TestBindCredentialDescriptor(t *testing.T) {
	descriptor, digest := parsedCredentialFixture(t)
	data, _ := planV2Fixture(t)
	data = []byte(strings.Replace(string(data), "credential_source_descriptor_sha256="+strings.Repeat("1", 64), "credential_source_descriptor_sha256="+fmt.Sprintf("%x", digest), 1))
	plan, err := Parse(data, sha256.Sum256(data), "amd64", time.Unix(1700000100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := BindCredentialDescriptor(plan, descriptor, time.Unix(1700000100, 0).UTC()); err != nil {
		t.Fatal(err)
	}
	mutatedPlan := plan
	mutatedPlan.SourceDatabaseOID = "999"
	if err := BindCredentialDescriptor(mutatedPlan, descriptor, time.Unix(1700000100, 0).UTC()); err != nil {
		t.Fatal("mutable plan projection influenced canonical binding")
	}
	corruptPlanIdentity := plan
	corruptPlanIdentity.SHA256[0] ^= 0xff
	if err := BindCredentialDescriptor(corruptPlanIdentity, descriptor, time.Unix(1700000100, 0).UTC()); err == nil {
		t.Fatal("corrupt plan projection identity accepted")
	}

	mutations := map[string]func(ca42credential.Descriptor) ca42credential.Descriptor{
		"release": func(value ca42credential.Descriptor) ca42credential.Descriptor {
			value.ReleaseID = "release-2"
			return value
		},
		"database_oid": func(value ca42credential.Descriptor) ca42credential.Descriptor {
			value.SourceDatabaseOID = "999"
			return value
		},
		"owner_oid": func(value ca42credential.Descriptor) ca42credential.Descriptor {
			value.SourceDatabaseOwnerOID = "998"
			return value
		},
		"window": func(value ca42credential.Descriptor) ca42credential.Descriptor {
			value.NotAfter = plan.NotAfter.Add(time.Second)
			return value
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if err := BindCredentialDescriptor(plan, mutate(descriptor), time.Unix(1700000100, 0).UTC()); err != nil {
				t.Fatal("mutable projection influenced canonical binding")
			}
		})
	}
	if err := BindCredentialDescriptor(Plan{}, descriptor, time.Unix(1700000100, 0).UTC()); err == nil {
		t.Fatal("manually constructed plan accepted")
	}
}

func parsedCredentialFixture(t *testing.T) (ca42credential.Descriptor, [sha256.Size]byte) {
	t.Helper()
	secret := bytes.Repeat([]byte{'s'}, 39)
	key := bytes.Repeat([]byte{'k'}, 32)
	raw := ca42credential.Descriptor{
		CommitmentAlgorithm: ca42credential.CommitmentHMACKeyringV1, CommitmentKeyID: "keyring-ca42",
		ReleaseID: "release-1", ReleaseRunID: "run-1", AttemptID: "attempt-1",
		SourceContainerID: fmt.Sprintf("%x", sha256.Sum256([]byte("plan-v2-fixture:source_container_id"))), SourceSystemIdentifier: "100",
		SourceDatabase: "pandora", SourceDatabaseOID: "101", SourceDatabaseOwner: "pandora_owner",
		SourceDatabaseOwnerOID: "102", CredentialSizeBytes: uint64(len(secret)),
		NotBefore: time.Unix(1700000000, 0).UTC(), NotAfter: time.Unix(1700003600, 0).UTC(),
	}
	commitment, err := ca42credential.HMACCommitment(secret, key, raw)
	if err != nil {
		t.Fatal(err)
	}
	values := []string{
		ca42credential.Format, ca42credential.Kind, ca42credential.CredentialName, ca42credential.CredentialFormat,
		raw.CommitmentAlgorithm, raw.CommitmentKeyID, commitment, ca42credential.Mode,
		strconv.FormatUint(raw.CredentialSizeBytes, 10), ca42credential.Delivery,
		raw.ReleaseID, raw.ReleaseRunID, raw.AttemptID, raw.SourceContainerID, raw.SourceSystemIdentifier,
		raw.SourceDatabase, raw.SourceDatabaseOID, raw.SourceDatabaseOwner, raw.SourceDatabaseOwnerOID,
		"1700000000", "1700003600",
	}
	data, err := ca42credential.CanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	parsed, err := ca42credential.Parse(data, digest, time.Unix(1700000100, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	return parsed, digest
}

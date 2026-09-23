package ca42manifest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestVerifyValidManifestBindsCompleteBytesAndIsolatedIdentity(t *testing.T) {
	manifest := validManifest(t)
	receipt, err := Verify(manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(manifest)
	if receipt.Decision != "STRUCTURALLY_VALID" || receipt.Authorization != "NONE" || receipt.ExternalManifestSHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("receipt = %#v", receipt)
	}
	if receipt.IsolatedContainerID != strings.Repeat("b", 64) || receipt.IsolatedSystemIdentifier != "2222222222222222222" || receipt.IsolatedDatabaseOID != "24576" {
		t.Fatalf("isolated identity was not preserved: %#v", receipt)
	}
}

func TestVerifyDeniesMalformedAndDriftedManifests(t *testing.T) {
	base := validManifest(t)
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"duplicate top key", func(v []byte) []byte {
			return bytes.Replace(v, []byte(`{"format":`), []byte(`{"format":"duplicate","format":`), 1)
		}},
		{"unknown top key", func(v []byte) []byte {
			return bytes.Replace(v, []byte(`{"format":`), []byte(`{"unknown":true,"format":`), 1)
		}},
		{"missing status", func(v []byte) []byte { return bytes.Replace(v, []byte(`,"status":"READY"`), nil, 1) }},
		{"wrong PostgreSQL", func(v []byte) []byte {
			return bytes.Replace(v, []byte(`"postgresql_major":18`), []byte(`"postgresql_major":17`), 1)
		}},
		{"wrong frozen migration", func(v []byte) []byte {
			return bytes.Replace(v, []byte(FrozenMigrationSHA256), []byte(strings.Repeat("0", 64)), 1)
		}},
		{"object digest mismatch", func(v []byte) []byte {
			return bytes.Replace(v, []byte(`"role_creation_mask":3`), []byte(`"role_creation_mask":2`), 1)
		}},
		{"duplicate nested key", func(v []byte) []byte {
			return bytes.Replace(v, []byte(`"schema":"app"`), []byte(`"schema":"app","schema":"app"`), 1)
		}},
		{"unknown relation field", func(v []byte) []byte {
			return bytes.Replace(v, []byte(`"schema":"app"`), []byte(`"unknown":0,"schema":"app"`), 1)
		}},
		{"source container invalid", func(v []byte) []byte { return bytes.Replace(v, []byte(strings.Repeat("b", 64)), []byte("short"), 1) }},
		{"CRLF", func(v []byte) []byte { return bytes.ReplaceAll(v, []byte("\n"), []byte("\r\n")) }},
		{"trailing JSON", func(v []byte) []byte { return append(v, []byte("{}\n")...) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if receipt, err := Verify(test.mutate(append([]byte(nil), base...))); err == nil || receipt.Decision != "" {
				t.Fatalf("accepted malformed manifest: receipt=%#v err=%v", receipt, err)
			}
		})
	}
}

func TestVerifyDeniesWrongRelationSetAndRoleOIDAlias(t *testing.T) {
	base := validManifest(t)
	for _, candidate := range [][]byte{
		bytes.Replace(base, []byte(`"name":"devices"`), []byte(`"name":"not_devices"`), 1),
		bytes.Replace(base, []byte(`"aegis_client_auth_gc_owner":11`), []byte(`"aegis_client_auth_gc_owner":10`), 1),
	} {
		if _, err := Verify(candidate); err == nil {
			t.Fatal("accepted relation or role OID identity drift")
		}
	}
}

func validManifest(t *testing.T) []byte {
	t.Helper()
	relations := make([]map[string]any, 0, len(expectedRelations))
	identities := make([]string, 0, len(expectedRelations))
	for identity := range expectedRelations {
		identities = append(identities, identity)
	}
	sortStrings(identities)
	for _, identity := range identities {
		parts := strings.SplitN(identity, ".", 2)
		relation := map[string]any{
			"schema": parts[0], "name": parts[1], "kind": "r", "persistence": "p",
			"owner": "aegis_client_auth_owner", "rls": false, "force_rls": false,
			"replica_identity": "d", "comment": nil, "columns": []any{},
			"constraints": []any{}, "indexes": []any{}, "policies": []any{},
			"triggers": []any{}, "acl": []any{}, "sequence": nil,
		}
		if strings.HasSuffix(parts[1], "_seq") {
			relation["kind"] = "S"
			relation["sequence"] = []any{1, 1, 9223372036854775807, 1, 1, false, []any{}}
		}
		relations = append(relations, relation)
	}
	provenanceRelations := make([]any, 15)
	for i := range provenanceRelations {
		provenanceRelations[i] = tuple(9)
	}
	provenanceRoles := make([]any, 4)
	for i := range provenanceRoles {
		provenanceRoles[i] = tuple(13)
	}
	object := map[string]any{
		"format": ObjectFormat, "contract_sha256": ContractSHA256, "role_creation_mask": 3,
		"role_oid_manifest": map[string]any{
			"aegis_client_auth_owner": 10, "aegis_client_auth_gc_owner": 11,
			"aegis_client_auth_gc": 12, "aegis_client_keyring_preflight_owner": 13,
		},
		"portable_catalog_manifest": map[string]any{"format": CatalogFormat, "relations": relations},
		"source_object_provenance": map[string]any{
			"namespaces": []any{tuple(5), tuple(5)}, "relations": provenanceRelations, "attributes": []any{},
			"constraints": []any{}, "indexes": []any{}, "policies": []any{},
			"triggers": []any{}, "roles": provenanceRoles, "role_descriptions": []any{},
		},
	}
	objectBytes, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	objectDigest := sha256.Sum256(objectBytes)
	outer := map[string]any{
		"format": Format, "status": "READY", "frozen_migration_sha256": FrozenMigrationSHA256,
		"postgresql_major": 18, "barrier": Barrier,
		"exact_object_manifest_sha256": hex.EncodeToString(objectDigest[:]),
		"source": map[string]any{
			"system_identifier": "2222222222222222222", "container_id": strings.Repeat("b", 64),
			"network_id": strings.Repeat("d", 64), "database": "aegis", "database_oid": "24576",
			"image_id": "sha256:" + strings.Repeat("c", 64), "run_id": "pandoraisolatedpg18ABC123-1700000000",
		},
		"object_manifest": json.RawMessage(objectBytes),
	}
	encoded, err := json.Marshal(outer)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func tuple(width int) []any {
	value := make([]any, width)
	for i := range value {
		value[i] = 0
	}
	return value
}

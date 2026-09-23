package ca42release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
)

func TestParseAndVerifyBindsProductionAndIsolatedIdentities(t *testing.T) {
	publicKey, privateKey := testKey()
	external, receipt := testExternalManifest(t)
	data := signedManifest(t, privateKey, receipt, nil)
	manifestDigest := sha256.Sum256(data)
	keyDigest := sha256.Sum256(publicKey)
	authority := testAuthorityBinding(publicKey, manifestDigest, keyDigest)
	manifest, err := ParseAndVerify(data, authority, "amd64", time.Unix(1700000100, 0), external)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.ProductionSourceContainerID != strings.Repeat("a", 64) || manifest.IsolatedTargetContainerID != strings.Repeat("b", 64) || manifest.ProductionSourceSystemIdentifier == manifest.IsolatedTargetSystemIdentifier || manifest.ProductionSourceGooseWaterline != 41 {
		t.Fatalf("dual identity binding lost: %#v", manifest)
	}
	core, err := ContractCoreSHA256(manifest)
	if err != nil || core == ([sha256.Size]byte{}) {
		t.Fatalf("release contract core failed: hash=%x err=%v", core, err)
	}
	coreBytes, err := ContractCoreBytes(manifest)
	if err != nil || !bytes.Contains(coreBytes, []byte("authorized_target_goose_waterline=42\n")) ||
		!bytes.Contains(coreBytes, []byte("postgresql_major=18\n")) ||
		!bytes.Contains(coreBytes, []byte("release_manifest_format="+Format+"\n")) {
		t.Fatalf("release contract core omitted fixed protocol identity: err=%v core=%q", err, coreBytes)
	}
	coreHex, err := ContractCoreSHA256Hex(manifest)
	if err != nil || coreHex != hex.EncodeToString(core[:]) {
		t.Fatalf("release contract core hex mismatch: got=%q err=%v", coreHex, err)
	}
	changedPins := manifest
	changedPins.ReleaseJournalHeadSHA256 = strings.Repeat("8", 64)
	changedPins.ExecutionPlanSHA256 = strings.Repeat("7", 64)
	stable, err := ContractCoreSHA256(changedPins)
	if err != nil || stable != core {
		t.Fatalf("journal/plan pins changed stable core: base=%x changed=%x err=%v", core, stable, err)
	}
	changedArtifact := manifest
	changedArtifact.DatabaseDumpSHA256 = strings.Repeat("8", 64)
	drifted, err := ContractCoreSHA256(changedArtifact)
	if err != nil || drifted == core {
		t.Fatalf("artifact drift did not change stable core: base=%x changed=%x err=%v", core, drifted, err)
	}
}

func TestParseAndVerifyRejectsSemanticAndAuthorityDrift(t *testing.T) {
	publicKey, privateKey := testKey()
	external, receipt := testExternalManifest(t)
	tests := []struct {
		name     string
		mutate   func([]string)
		external func([]byte) []byte
		now      int64
	}{
		{"authority sequence", func(v []string) { v[4] = "2" }, nil, 1700000100},
		{"authority binding", func(v []string) { v[5] = strings.Repeat("8", 64) }, nil, 1700000100},
		{"release signer key ID", func(v []string) { v[6] = "other-signer" }, nil, 1700000100},
		{"source waterline", func(v []string) { v[26] = "40" }, nil, 1700000100},
		{"zero execution plan hash", func(v []string) { v[38] = strings.Repeat("0", 64) }, nil, 1700000100},
		{"identity conflation", func(v []string) { v[27] = v[20] }, nil, 1700000100},
		{"external full hash", nil, func(raw []byte) []byte { return append(raw, ' ') }, 1700000100},
		{"external object hash", nil, func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"role_creation_mask":3`), []byte(`"role_creation_mask":2`), 1)
		}, 1700000100},
		{"end boundary expired", nil, nil, 1700000300},
		{"oversize OID", func(v []string) { v[23] = "4294967296" }, nil, 1700000100},
		{"duration overflow", func(v []string) { v[39] = "1"; v[40] = "18446744074" }, nil, 1700000100},
		{"epoch over MaxInt64", func(v []string) { v[40] = "9223372036854775808" }, nil, 1700000100},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidateExternal := append([]byte(nil), external...)
			if test.external != nil {
				candidateExternal = test.external(candidateExternal)
			}
			data := signedManifest(t, privateKey, receipt, test.mutate)
			manifestDigest := sha256.Sum256(data)
			keyDigest := sha256.Sum256(publicKey)
			authority := testAuthorityBinding(publicKey, manifestDigest, keyDigest)
			if result, err := ParseAndVerify(data, authority, "amd64", time.Unix(test.now, 0), candidateExternal); err == nil || result.ReleaseID != "" {
				t.Fatalf("accepted drift: result=%#v err=%v", result, err)
			}
		})
	}
	data := signedManifest(t, privateKey, receipt, nil)
	manifestDigest := sha256.Sum256(data)
	wrongKeyDigest := sha256.Sum256(bytes.Repeat([]byte{9}, ed25519.PublicKeySize))
	authority := testAuthorityBinding(publicKey, manifestDigest, wrongKeyDigest)
	if _, err := ParseAndVerify(data, authority, "amd64", time.Unix(1700000100, 0), external); err == nil {
		t.Fatal("accepted wrong authority pin")
	}
	badManifestPin := sha256.Sum256([]byte("different"))
	keyDigest := sha256.Sum256(publicKey)
	authority = testAuthorityBinding(publicKey, badManifestPin, keyDigest)
	if _, err := ParseAndVerify(data, authority, "amd64", time.Unix(1700000100, 0), external); err == nil {
		t.Fatal("accepted wrong manifest pin")
	}
}

func TestExecutionPlanPinIsInsideReleaseSignature(t *testing.T) {
	publicKey, privateKey := testKey()
	external, receipt := testExternalManifest(t)
	signed := signedManifest(t, privateKey, receipt, nil)
	tampered := bytes.Replace(signed,
		[]byte("execution_plan_sha256="+strings.Repeat("9", 64)),
		[]byte("execution_plan_sha256="+strings.Repeat("8", 64)), 1)
	if bytes.Equal(tampered, signed) {
		t.Fatal("test did not modify execution plan pin")
	}
	manifestDigest := sha256.Sum256(tampered)
	keyDigest := sha256.Sum256(publicKey)
	authority := testAuthorityBinding(publicKey, manifestDigest, keyDigest)
	if _, err := ParseAndVerify(tampered, authority, "amd64", time.Unix(1700000100, 0), external); err == nil || !strings.Contains(err.Error(), "signature denied") {
		t.Fatalf("tampered signed execution pin was not rejected by signature boundary: %v", err)
	}
}

func testKey() (ed25519.PublicKey, ed25519.PrivateKey) {
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	return privateKey.Public().(ed25519.PublicKey), privateKey
}

func testAuthorityBinding(publicKey ed25519.PublicKey, manifestDigest, keyDigest [sha256.Size]byte) AuthorityBinding {
	return AuthorityBinding{
		PublicKey: publicKey, ManifestSHA256: manifestDigest, SignerSHA256: keyDigest,
		Epoch: 1, Sequence: 1, BindingSHA256: sha256.Sum256([]byte("authority-binding")),
		ReleaseSignerKeyID: "release-signer-1",
	}
}

func signedManifest(t *testing.T, privateKey ed25519.PrivateKey, external ca42manifest.Receipt, mutate func([]string)) []byte {
	t.Helper()
	authorityBindingDigest := sha256.Sum256([]byte("authority-binding"))
	values := []string{
		Format, SignatureAlgorithm, Status, "1", "1", hex.EncodeToString(authorityBindingDigest[:]), "release-signer-1",
		"amd64", "release-ca42-1", "run-ca42-1", "attempt-ca42-1",
		strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64), strings.Repeat("5", 64),
		ca42manifest.FrozenMigrationSHA256, strings.Repeat("6", 64), strings.Repeat("7", 64), strings.Repeat("c", 64),
		strings.Repeat("a", 64), "1111111111111111111", "aegis", "16384", "10", "aegis_owner", "41",
		strings.Repeat("b", 64), "2222222222222222222", strings.Repeat("d", 64), "aegis", "24576",
		"sha256:" + strings.Repeat("c", 64), "pandoraisolatedpg18ABC123-1700000000", external.ExternalManifestSHA256, external.ExactObjectManifestSHA256,
		strings.Repeat("e", 64), strings.Repeat("f", 64), strings.Repeat("9", 64), "1700000000", "1700000300",
	}
	if mutate != nil {
		mutate(values)
	}
	signed, err := SignatureInput(values)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, signed)
	return append(append(signed, []byte("signature_b64="+base64.StdEncoding.EncodeToString(signature)+"\n")...), nil...)
}

func testExternalManifest(t *testing.T) ([]byte, ca42manifest.Receipt) {
	t.Helper()
	identities := []string{
		"app.client_auth_00042_meta", "public.devices", "public.device_authorizations", "public.config_bundles", "public.refresh_tokens",
		"public.config_bundle_nodes", "public.refresh_families", "public.device_proof_nonces", "public.client_access_token_jtis",
		"public.device_issuance_response_replays", "public.client_refresh_response_replays", "public.device_issuance_replay_uses",
		"public.client_refresh_replay_uses", "public.device_proof_nonces_id_seq", "public.client_access_token_jtis_id_seq",
	}
	sort.Strings(identities)
	relations := make([]map[string]any, 0, len(identities))
	for _, identity := range identities {
		parts := strings.SplitN(identity, ".", 2)
		relation := map[string]any{"schema": parts[0], "name": parts[1], "kind": "r", "persistence": "p", "owner": "aegis_client_auth_owner", "rls": false, "force_rls": false, "replica_identity": "d", "comment": nil, "columns": []any{}, "constraints": []any{}, "indexes": []any{}, "policies": []any{}, "triggers": []any{}, "acl": []any{}, "sequence": nil}
		if strings.HasSuffix(parts[1], "_seq") {
			relation["kind"] = "S"
			relation["sequence"] = []any{1, 1, 9223372036854775807, 1, 1, false, []any{}}
		}
		relations = append(relations, relation)
	}
	tuple := func(width int) []any {
		value := make([]any, width)
		for i := range value {
			value[i] = 0
		}
		return value
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
		"format": ca42manifest.ObjectFormat, "contract_sha256": ca42manifest.ContractSHA256, "role_creation_mask": 3,
		"role_oid_manifest":         map[string]any{"aegis_client_auth_owner": 10, "aegis_client_auth_gc_owner": 11, "aegis_client_auth_gc": 12, "aegis_client_keyring_preflight_owner": 13},
		"portable_catalog_manifest": map[string]any{"format": ca42manifest.CatalogFormat, "relations": relations},
		"source_object_provenance":  map[string]any{"namespaces": []any{tuple(5), tuple(5)}, "relations": provenanceRelations, "attributes": []any{}, "constraints": []any{}, "indexes": []any{}, "policies": []any{}, "triggers": []any{}, "roles": provenanceRoles, "role_descriptions": []any{}},
	}
	objectBytes, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	objectDigest := sha256.Sum256(objectBytes)
	outer := map[string]any{
		"format": ca42manifest.Format, "status": "READY", "frozen_migration_sha256": ca42manifest.FrozenMigrationSHA256,
		"postgresql_major": 18, "barrier": ca42manifest.Barrier, "exact_object_manifest_sha256": hex.EncodeToString(objectDigest[:]),
		"source":          map[string]any{"system_identifier": "2222222222222222222", "container_id": strings.Repeat("b", 64), "network_id": strings.Repeat("d", 64), "database": "aegis", "database_oid": "24576", "image_id": "sha256:" + strings.Repeat("c", 64), "run_id": "pandoraisolatedpg18ABC123-1700000000"},
		"object_manifest": json.RawMessage(objectBytes),
	}
	data, err := json.Marshal(outer)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	receipt, err := ca42manifest.Verify(data)
	if err != nil {
		t.Fatal(err)
	}
	return data, receipt
}

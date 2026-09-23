package ca42releasev3

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42protocolv2"
)

type v3Fixture struct {
	architecture string
	now          time.Time
	values       []string
	planValues   []string
	plan         ca42executionv2.Plan
	external     []byte
	receipt      ca42manifest.Receipt
	public       ed25519.PublicKey
	private      ed25519.PrivateKey
	authority    AuthorityBinding
	signed       []byte
	manifest     Manifest
}

func newV3Fixture(t *testing.T, architecture string) v3Fixture {
	t.Helper()
	now := time.Unix(1700000100, 0).UTC()
	external := externalManifestFixture(t)
	receipt, err := ca42manifest.Verify(external)
	if err != nil {
		t.Fatal(err)
	}
	seed := sha256.Sum256([]byte("release-v3-ed25519-fixture"))
	private := ed25519.NewKeyFromSeed(seed[:])
	public := append(ed25519.PublicKey(nil), private.Public().(ed25519.PublicKey)...)
	values := validManifestValues(t, architecture, receipt)
	planValues := planValuesForManifest(t, values)
	planBytes, err := ca42executionv2.CanonicalBytes(planValues)
	if err != nil {
		t.Fatal(err)
	}
	planDigest := sha256.Sum256(planBytes)
	setManifestValue(t, values, FieldExecutionPlanSHA256, hex.EncodeToString(planDigest[:]))
	plan, err := ca42executionv2.Parse(planBytes, planDigest, architecture, now)
	if err != nil {
		t.Fatal(err)
	}
	authority := AuthorityBinding{
		PublicKey: public, SignerSHA256: sha256.Sum256(public), Epoch: 7, Sequence: 11,
		BindingSHA256: hashFor("authority-binding"), ReleaseSignerKeyID: "release-key-7",
	}
	setManifestValue(t, values, FieldAuthorityEpoch, "7")
	setManifestValue(t, values, FieldAuthoritySequence, "11")
	setManifestValue(t, values, FieldAuthorityBindingSHA256, hex.EncodeToString(authority.BindingSHA256[:]))
	setManifestValue(t, values, FieldReleaseSignerKeyID, authority.ReleaseSignerKeyID)
	signed := signManifestValues(t, values, private)
	authority.ManifestSHA256 = sha256.Sum256(signed)
	manifest, err := ParseAndVerify(signed, authority, architecture, now, external)
	if err != nil {
		t.Fatal(err)
	}
	return v3Fixture{
		architecture: architecture, now: now, values: values, planValues: planValues, plan: plan,
		external: external, receipt: receipt, public: public, private: private,
		authority: authority, signed: signed, manifest: manifest,
	}
}

func validManifestValues(t *testing.T, architecture string, receipt ca42manifest.Receipt) []string {
	t.Helper()
	values := make([]string, int(fieldCount)-1)
	for index := range values {
		values[index] = "fixture"
	}
	set := func(field Field, value string) { setManifestValue(t, values, field, value) }
	set(FieldFormat, Format)
	set(FieldSignatureAlgorithm, SignatureAlgorithm)
	set(FieldStatus, Status)
	set(FieldProfileID, ca42protocolv2.ProfileID)
	profileSHA, err := ca42protocolv2.Strict().SHA256()
	if err != nil {
		t.Fatal(err)
	}
	set(FieldProfileSHA256, hex.EncodeToString(profileSHA[:]))
	set(FieldArchitecture, architecture)
	set(FieldReleaseID, "release-1")
	set(FieldReleaseRunID, "release-run-1")
	set(FieldAttemptID, "attempt-1")
	set(FieldTransition, ca42executionv2.Transition)
	for _, field := range []Field{
		FieldCredentialSourceDescriptorSHA256, FieldPathtrustBinarySHA256, FieldPathtrustChainSHA256,
		FieldTrustCapsuleSHA256, FieldAttestationCoreSHA256, FieldAttestationCoreChainSHA256,
		FieldAttestationSHA256, FieldExpectedSHA256, FieldAttestationPublicKeySHA256,
		FieldExternalManifestSHA256, FieldExternalObjectManifestSHA256, FieldBashBinarySHA256,
		FieldBashChainSHA256, FieldDockerClientSHA256, FieldDockerClientChainSHA256,
		FieldRuntimeClosureManifestSHA256, FieldManifestVerifierSHA256, FieldPreflightRunnerSHA256,
		FieldMigrationRunnerSHA256, FieldGooseBinarySHA256, FieldGooseBuildInfoSHA256,
		FieldMigrationSetSHA256, FieldClientAuth00042SHA256, FieldArtifactStorageDescriptorSHA256,
		FieldGlobalsDumpSHA256, FieldDatabaseDumpSHA256, FieldPostgresImageSHA256,
		FieldProductionSourceContainerID, FieldIsolatedTargetContainerID, FieldIsolatedTargetNetworkID,
		FieldLedgerDirectorySHA256, FieldReleaseJournalHeadSHA256, FieldReleaseJournalSnapshotSHA256,
		FieldExecutionPlanSHA256,
	} {
		set(field, fmt.Sprintf("%x", hashFor("manifest:"+FieldName(field))))
	}
	set(FieldClientAuth00042SHA256, ca42manifest.FrozenMigrationSHA256)
	set(FieldExternalManifestSHA256, receipt.ExternalManifestSHA256)
	set(FieldExternalObjectManifestSHA256, receipt.ExactObjectManifestSHA256)
	set(FieldPostgresImageSHA256, strings.Repeat("c", 64))
	set(FieldProductionSourceContainerID, strings.Repeat("a", 64))
	set(FieldIsolatedTargetContainerID, receipt.IsolatedContainerID)
	set(FieldIsolatedTargetNetworkID, receipt.IsolatedNetworkID)
	set(FieldLedgerDirectorySHA256, ca42executionv2.LedgerDirectorySHA256)
	set(FieldPathtrustDevice, "10")
	set(FieldPathtrustMode, ca42executionv2.RequiredAttemptExecutableMode)
	set(FieldAttestationCoreDevice, "10")
	set(FieldAttestationCoreMode, ca42executionv2.RequiredAttemptExecutableMode)
	set(FieldAttestationFormat, ca42protocolv2.AttestationFormat)
	set(FieldExpectedFormat, ca42protocolv2.ExpectedFormat)
	set(FieldBashDevice, "20")
	set(FieldBashMode, ca42executionv2.RequiredSystemExecutableMode)
	set(FieldDockerClientDevice, "20")
	set(FieldDockerClientMode, ca42executionv2.RequiredSystemExecutableMode)
	set(FieldGooseVersion, ca42executionv2.RequiredGooseVersion)
	set(FieldArtifactStorageProfile, ca42executionv2.RequiredStorageProfile)
	set(FieldGlobalsDumpSizeBytes, "1024")
	set(FieldDatabaseDumpSizeBytes, "1073741824")
	set(FieldProductionSourceSystemIdentifier, "1111111111111111111")
	set(FieldProductionSourceDatabase, "aegis")
	set(FieldProductionSourceDatabaseOID, "16384")
	set(FieldProductionSourceDatabaseOwnerOID, "10")
	set(FieldProductionSourceDatabaseOwnerName, "aegis_owner")
	set(FieldProductionSourceGooseWaterline, "41")
	set(FieldIsolatedTargetSystemIdentifier, receipt.IsolatedSystemIdentifier)
	set(FieldIsolatedTargetDatabase, receipt.IsolatedDatabase)
	set(FieldIsolatedTargetDatabaseOID, receipt.IsolatedDatabaseOID)
	set(FieldIsolatedTargetImageID, receipt.IsolatedImageID)
	set(FieldIsolatedTargetRunID, receipt.IsolatedRunID)
	set(FieldLedgerNamespace, ca42protocolv2.LedgerNamespace)
	set(FieldReleaseJournalFormat, ca42protocolv2.ReleaseJournalFormat)
	set(FieldReleaseJournalManifestFormat, ca42protocolv2.ReleaseJournalManifestFormat)
	set(FieldReleaseJournalNamespace, ca42protocolv2.ReleaseJournalNamespace)
	set(FieldExecutionPlanFormat, ca42protocolv2.ExecutionPlanFormat)
	set(FieldNotBeforeEpoch, "1700000000")
	set(FieldNotAfterEpoch, "1700003600")
	return values
}

func planValuesForManifest(t *testing.T, manifestValues []string) []string {
	t.Helper()
	names := ca42executionv2.CanonicalFieldNames()
	values := make([]string, len(names))
	for index, name := range names {
		if name == "format" {
			values[index] = ca42executionv2.Format
			continue
		}
		releaseName := name
		switch name {
		case "source_container_id", "source_system_identifier", "source_database", "source_database_oid", "source_database_owner_oid", "source_database_owner_name", "source_goose_waterline":
			releaseName = "production_" + name
		case "isolated_container_id", "isolated_system_identifier", "isolated_network_id", "isolated_database", "isolated_database_oid", "isolated_image_id", "isolated_run_id":
			releaseName = "isolated_target_" + strings.TrimPrefix(name, "isolated_")
		}
		field := manifestFieldNamed(releaseName)
		if field >= fieldCount || field == FieldSignatureB64 || field == FieldExecutionPlanSHA256 {
			t.Fatalf("plan fixture field has no release source: %s", name)
		}
		values[index] = manifestValues[field]
	}
	return values
}

func manifestFieldNamed(name string) Field {
	for field, candidate := range fieldNames {
		if candidate == name {
			return Field(field)
		}
	}
	return fieldCount
}

func setManifestValue(t *testing.T, values []string, field Field, value string) {
	t.Helper()
	if field >= FieldSignatureB64 || int(field) >= len(values) {
		t.Fatalf("invalid unsigned manifest field: %d", field)
	}
	values[field] = value
}

func signManifestValues(t *testing.T, values []string, private ed25519.PrivateKey) []byte {
	t.Helper()
	unsigned, err := UnsignedCanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	message, err := SignatureMessage(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := SignedCanonicalBytes(unsigned, ed25519.Sign(private, message))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func parseSignedValues(t *testing.T, fixture v3Fixture, values []string) Manifest {
	t.Helper()
	signed := signManifestValues(t, values, fixture.private)
	authority := fixture.authority
	authority.ManifestSHA256 = sha256.Sum256(signed)
	manifest, err := ParseAndVerify(signed, authority, fixture.architecture, fixture.now, fixture.external)
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func hashFor(label string) [sha256.Size]byte {
	return sha256.Sum256([]byte("release-v3-fixture:" + label))
}

func externalManifestFixture(t *testing.T) []byte {
	t.Helper()
	identities := []string{
		"app.client_auth_00042_meta", "public.devices", "public.device_authorizations",
		"public.config_bundles", "public.refresh_tokens", "public.config_bundle_nodes",
		"public.refresh_families", "public.device_proof_nonces", "public.client_access_token_jtis",
		"public.device_issuance_response_replays", "public.client_refresh_response_replays",
		"public.device_issuance_replay_uses", "public.client_refresh_replay_uses",
		"public.device_proof_nonces_id_seq", "public.client_access_token_jtis_id_seq",
	}
	sort.Strings(identities)
	relations := make([]map[string]any, 0, len(identities))
	for _, identity := range identities {
		parts := strings.SplitN(identity, ".", 2)
		relation := map[string]any{
			"schema": parts[0], "name": parts[1], "kind": "r", "persistence": "p",
			"owner": "aegis_client_auth_owner", "rls": false, "force_rls": false,
			"replica_identity": "d", "comment": nil, "columns": []any{}, "constraints": []any{},
			"indexes": []any{}, "policies": []any{}, "triggers": []any{}, "acl": []any{}, "sequence": nil,
		}
		if strings.HasSuffix(parts[1], "_seq") {
			relation["kind"] = "S"
			relation["sequence"] = []any{1, 1, 9223372036854775807, 1, 1, false, []any{}}
		}
		relations = append(relations, relation)
	}
	tuple := func(width int) []any { return make([]any, width) }
	provenanceRelations := make([]any, 15)
	for index := range provenanceRelations {
		provenanceRelations[index] = tuple(9)
	}
	provenanceRoles := make([]any, 4)
	for index := range provenanceRoles {
		provenanceRoles[index] = tuple(13)
	}
	object := map[string]any{
		"format": ca42manifest.ObjectFormat, "contract_sha256": ca42manifest.ContractSHA256, "role_creation_mask": 3,
		"role_oid_manifest":         map[string]any{"aegis_client_auth_owner": 10, "aegis_client_auth_gc_owner": 11, "aegis_client_auth_gc": 12, "aegis_client_keyring_preflight_owner": 13},
		"portable_catalog_manifest": map[string]any{"format": ca42manifest.CatalogFormat, "relations": relations},
		"source_object_provenance": map[string]any{
			"namespaces": []any{tuple(5), tuple(5)}, "relations": provenanceRelations, "attributes": []any{},
			"constraints": []any{}, "indexes": []any{}, "policies": []any{}, "triggers": []any{},
			"roles": provenanceRoles, "role_descriptions": []any{},
		},
	}
	objectBytes, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	objectDigest := sha256.Sum256(objectBytes)
	outer := map[string]any{
		"format": ca42manifest.Format, "status": "READY", "frozen_migration_sha256": ca42manifest.FrozenMigrationSHA256,
		"postgresql_major": 18, "barrier": ca42manifest.Barrier,
		"exact_object_manifest_sha256": fmt.Sprintf("%x", objectDigest),
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

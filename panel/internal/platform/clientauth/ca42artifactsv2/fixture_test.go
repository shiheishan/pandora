package ca42artifactsv2

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42attestationv3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42capsulev2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42expectedv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42protocolv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42releasev3"
)

type graphFixture struct {
	now               time.Time
	external          []byte
	inputs            Inputs
	releaseRaw        []byte
	planRaw           []byte
	capsuleRaw        []byte
	expectedRaw       []byte
	attestationRaw    []byte
	attestationPublic []byte
	releasePrivate    ed25519.PrivateKey
	sidecars          sidecarFixture
}

type graphFixtureOptions struct {
	now          time.Time
	prepareBase  func(*testing.T, map[string]string, []byte, []byte)
	buildStorage storageFixtureBuilder
}

func newGraphFixture(t *testing.T, architecture, label string) graphFixture {
	return newGraphFixtureWithOptions(t, architecture, label, graphFixtureOptions{})
}

func newGraphFixtureWithOptions(t *testing.T, architecture, label string, options graphFixtureOptions) graphFixture {
	t.Helper()
	dynamicWindow := !options.now.IsZero()
	now := options.now.UTC()
	if now.IsZero() {
		now = time.Unix(1700000100, 0).UTC()
	}
	external := graphExternalManifest(t, label)
	receipt, err := ca42manifest.Verify(external)
	if err != nil {
		t.Fatal(err)
	}
	profileSHA, err := ca42protocolv2.Strict().SHA256()
	if err != nil {
		t.Fatal(err)
	}
	seed := sha256.Sum256([]byte("artifact-set-attestation:" + label))
	attestationPrivate := ed25519.NewKeyFromSeed(seed[:])
	attestationPublic := attestationPrivate.Public().(ed25519.PublicKey)
	der, err := x509.MarshalPKIXPublicKey(attestationPublic)
	if err != nil {
		t.Fatal(err)
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	publicDigest := sha256.Sum256(publicPEM)

	notBefore, notAfter := "1700000000", "1700003600"
	if dynamicWindow {
		notBefore = strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)
		notAfter = strconv.FormatInt(now.Add(50*time.Minute).Unix(), 10)
	}
	base := map[string]string{
		"profile_id": ca42protocolv2.ProfileID, "profile_sha256": hex.EncodeToString(profileSHA[:]),
		"attestation_format": ca42attestationv3.Format, "attestation_signature_algorithm": ca42attestationv3.SignatureAlgorithm,
		"expected_format": ca42expectedv2.Format,
		"release_id":      "release-" + label, "release_run_id": "release-run-" + label,
		"attempt_id": "attempt-" + label, "architecture": architecture, "transition": ca42executionv2.Transition,
		"attestation_public_key_sha256": hex.EncodeToString(publicDigest[:]),
		"pathtrust_device":              "10", "pathtrust_mode": ca42executionv2.RequiredAttemptExecutableMode,
		"attestation_core_device": "10", "attestation_core_mode": ca42executionv2.RequiredAttemptExecutableMode,
		"bash_device": "20", "bash_mode": ca42executionv2.RequiredSystemExecutableMode,
		"docker_client_device": "20", "docker_client_mode": ca42executionv2.RequiredSystemExecutableMode,
		"goose_version":            ca42executionv2.RequiredGooseVersion,
		"artifact_storage_profile": ca42executionv2.RequiredStorageProfile,
		"globals_dump_size_bytes":  "1024", "database_dump_size_bytes": "1073741824",
		"external_manifest_sha256":        receipt.ExternalManifestSHA256,
		"external_object_manifest_sha256": receipt.ExactObjectManifestSHA256,
		"source_system_identifier":        "1111111111111111111", "source_database_name": "aegis",
		"source_database_oid": "16384", "source_database_owner_oid": "10", "source_database_owner_name": "aegis_owner",
		"source_goose_waterline": "41", "isolated_container_id": receipt.IsolatedContainerID,
		"isolated_system_identifier": receipt.IsolatedSystemIdentifier, "isolated_network_id": receipt.IsolatedNetworkID,
		"isolated_database_name": receipt.IsolatedDatabase, "isolated_database_oid": receipt.IsolatedDatabaseOID,
		"isolated_image_id": receipt.IsolatedImageID, "isolated_run_id": receipt.IsolatedRunID,
		"ledger_namespace": ca42protocolv2.LedgerNamespace, "ledger_directory_sha256": ca42executionv2.LedgerDirectorySHA256,
		"not_before_epoch": notBefore, "not_after_epoch": notAfter,
	}
	base["source_container_id"] = graphHash("source-container:" + label)
	base["postgres_image_sha256"] = strings.TrimPrefix(receipt.IsolatedImageID, "sha256:")
	base["client_auth_00042_sha256"] = ca42manifest.FrozenMigrationSHA256
	for _, names := range [][]string{
		ca42expectedv2.FieldNames(), ca42capsulev2.FieldNames(), ca42executionv2.CanonicalFieldNames(),
	} {
		for _, name := range names {
			if _, ok := base[name]; !ok && strings.HasSuffix(name, "_sha256") {
				base[name] = graphHash(label + ":" + name)
			}
		}
	}
	if options.prepareBase != nil {
		options.prepareBase(t, base, external, publicPEM)
	}
	sidecars := newSidecarFixtureWithStorage(t, base, architecture, label, now, dynamicWindow, options.buildStorage)

	expectedValues := valuesForNames(t, ca42expectedv2.FieldNames(), func(name string) string {
		if name == "format" {
			return ca42expectedv2.Format
		}
		return base[name]
	})
	expectedBytes, err := ca42expectedv2.CanonicalBytes(expectedValues)
	if err != nil {
		t.Fatal(err)
	}
	expectedDigest := sha256.Sum256(expectedBytes)
	expected, err := ca42expectedv2.Parse(expectedBytes, expectedDigest, architecture, now)
	if err != nil {
		t.Fatal(err)
	}
	expectedSnapshot, err := expected.SnapshotAt(now)
	if err != nil {
		t.Fatal(err)
	}

	attestationNames := ca42attestationv3.FieldNames()
	issuedAt, expiresAt := "1700000050", "1700003500"
	if dynamicWindow {
		issuedAt = strconv.FormatInt(now.Add(-30*time.Second).Unix(), 10)
		expiresAt = strconv.FormatInt(now.Add(48*time.Minute).Unix(), 10)
	}
	attestationValues := valuesForNames(t, attestationNames[:len(attestationNames)-1], func(name string) string {
		switch name {
		case "format":
			return ca42attestationv3.Format
		case "signature_algorithm":
			return ca42attestationv3.SignatureAlgorithm
		case "profile_id":
			return ca42protocolv2.ProfileID
		case "profile_sha256":
			return hex.EncodeToString(profileSHA[:])
		case "expected_format":
			return ca42expectedv2.Format
		case "expected_sha256":
			return hex.EncodeToString(expectedDigest[:])
		case "nonce":
			return graphHash("nonce:" + label)
		case "issued_at":
			return issuedAt
		case "expires_at":
			return expiresAt
		default:
			return expectedSnapshot.ValueName(name)
		}
	})
	unsignedAttestation, err := ca42attestationv3.UnsignedCanonicalBytes(attestationValues)
	if err != nil {
		t.Fatal(err)
	}
	attestationMessage, err := ca42attestationv3.SignatureMessage(unsignedAttestation)
	if err != nil {
		t.Fatal(err)
	}
	attestationBytes, err := ca42attestationv3.SignedCanonicalBytes(unsignedAttestation, ed25519.Sign(attestationPrivate, attestationMessage))
	if err != nil {
		t.Fatal(err)
	}
	attestationDigest := sha256.Sum256(attestationBytes)
	attestation, err := ca42attestationv3.ParseAndVerify(attestationBytes, attestationDigest, expected, publicPEM, now)
	if err != nil {
		t.Fatal(err)
	}
	attestationSnapshot, err := attestation.SnapshotAt(now)
	if err != nil {
		t.Fatal(err)
	}

	capsuleValues := valuesForNames(t, ca42capsulev2.FieldNames(), func(name string) string {
		switch name {
		case "format":
			return ca42capsulev2.Format
		case "attestation_format":
			return ca42attestationv3.Format
		case "attestation_sha256":
			return hex.EncodeToString(attestationDigest[:])
		case "expected_sha256":
			return hex.EncodeToString(expectedDigest[:])
		default:
			return attestationSnapshot.Value(name)
		}
	})
	capsuleBytes, err := ca42capsulev2.CanonicalBytes(capsuleValues)
	if err != nil {
		t.Fatal(err)
	}
	capsuleDigest := sha256.Sum256(capsuleBytes)
	capsule, err := ca42capsulev2.Parse(capsuleBytes, capsuleDigest)
	if err != nil {
		t.Fatal(err)
	}

	planValues := valuesForNames(t, ca42executionv2.CanonicalFieldNames(), func(name string) string {
		switch name {
		case "format":
			return ca42executionv2.Format
		case "status":
			return ca42executionv2.Status
		case "trust_capsule_sha256":
			return hex.EncodeToString(capsuleDigest[:])
		case "attestation_sha256":
			return hex.EncodeToString(attestationDigest[:])
		case "expected_sha256":
			return hex.EncodeToString(expectedDigest[:])
		case "source_database":
			return base["source_database_name"]
		case "source_database_owner_name":
			return base["source_database_owner_name"]
		case "isolated_database":
			return base["isolated_database_name"]
		default:
			return base[name]
		}
	})
	planBytes, err := ca42executionv2.CanonicalBytes(planValues)
	if err != nil {
		t.Fatal(err)
	}
	planDigest := sha256.Sum256(planBytes)
	plan, err := ca42executionv2.Parse(planBytes, planDigest, architecture, now)
	if err != nil {
		t.Fatal(err)
	}

	releaseSeed := sha256.Sum256([]byte("artifact-set-release:" + label))
	releasePrivate := ed25519.NewKeyFromSeed(releaseSeed[:])
	releasePublic := releasePrivate.Public().(ed25519.PublicKey)
	authority := ca42releasev3.AuthorityBinding{
		PublicKey: releasePublic, SignerSHA256: sha256.Sum256(releasePublic), Epoch: 7, Sequence: 11,
		BindingSHA256: sha256.Sum256([]byte("authority-binding:" + label)), ReleaseSignerKeyID: "release-key-" + label,
	}
	releaseNames := releaseFieldNames(t)
	releaseValues := valuesForNames(t, releaseNames[:len(releaseNames)-1], func(name string) string {
		switch name {
		case "format":
			return ca42releasev3.Format
		case "signature_algorithm":
			return ca42releasev3.SignatureAlgorithm
		case "status":
			return ca42releasev3.Status
		case "authority_epoch":
			return "7"
		case "authority_sequence":
			return "11"
		case "authority_binding_sha256":
			return hex.EncodeToString(authority.BindingSHA256[:])
		case "release_signer_key_id":
			return authority.ReleaseSignerKeyID
		case "trust_capsule_sha256":
			return hex.EncodeToString(capsuleDigest[:])
		case "attestation_sha256":
			return hex.EncodeToString(attestationDigest[:])
		case "expected_sha256":
			return hex.EncodeToString(expectedDigest[:])
		case "execution_plan_format":
			return ca42executionv2.Format
		case "execution_plan_sha256":
			return hex.EncodeToString(planDigest[:])
		case "release_journal_format":
			return ca42protocolv2.ReleaseJournalFormat
		case "release_journal_manifest_format":
			return ca42protocolv2.ReleaseJournalManifestFormat
		case "release_journal_namespace":
			return ca42protocolv2.ReleaseJournalNamespace
		case "release_journal_head_sha256", "release_journal_snapshot_sha256":
			return graphHash(label + ":" + name)
		default:
			if strings.HasPrefix(name, "production_") {
				mapped := strings.TrimPrefix(name, "production_")
				if mapped == "source_database" {
					mapped = "source_database_name"
				}
				return base[mapped]
			}
			if strings.HasPrefix(name, "isolated_target_") {
				mapped := "isolated_" + strings.TrimPrefix(name, "isolated_target_")
				if mapped == "isolated_database" {
					mapped = "isolated_database_name"
				}
				return base[mapped]
			}
			return base[name]
		}
	})
	unsignedRelease, err := ca42releasev3.UnsignedCanonicalBytes(releaseValues)
	if err != nil {
		t.Fatal(err)
	}
	releaseMessage, err := ca42releasev3.SignatureMessage(unsignedRelease)
	if err != nil {
		t.Fatal(err)
	}
	releaseBytes, err := ca42releasev3.SignedCanonicalBytes(unsignedRelease, ed25519.Sign(releasePrivate, releaseMessage))
	if err != nil {
		t.Fatal(err)
	}
	authority.ManifestSHA256 = sha256.Sum256(releaseBytes)
	release, err := ca42releasev3.ParseAndVerify(releaseBytes, authority, architecture, now, external)
	if err != nil {
		t.Fatal(err)
	}
	return graphFixture{
		now: now, external: external,
		inputs: Inputs{Release: release, Plan: plan, Capsule: capsule, Expected: expected, Attestation: attestation, ExternalManifest: external,
			CredentialDescriptor: sidecars.credential, RuntimeClosure: sidecars.runtime,
			GooseBuildInfo: sidecars.goose, StorageDescriptor: sidecars.storage},
		releaseRaw: releaseBytes, planRaw: planBytes, capsuleRaw: capsuleBytes, expectedRaw: expectedBytes,
		attestationRaw: attestationBytes, attestationPublic: publicPEM, releasePrivate: releasePrivate, sidecars: sidecars,
	}
}

func TestGraphFixtureOptionsExposeCanonicalRawGraphAtCurrentTime(t *testing.T) {
	now := time.Unix(1785805200, 0).UTC()
	fixture := newGraphFixtureWithOptions(t, "amd64", "dynamic", graphFixtureOptions{now: now})
	set, err := New(fixture.inputs, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := set.SnapshotAt(now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.EffectiveNotBefore != now || snapshot.EffectiveNotAfter != now.Add(45*time.Minute) {
		t.Fatalf("dynamic graph validity mismatch: %+v", snapshot)
	}
	for name, raw := range map[string][]byte{
		"release": fixture.releaseRaw, "plan": fixture.planRaw, "capsule": fixture.capsuleRaw,
		"expected": fixture.expectedRaw, "attestation": fixture.attestationRaw,
		"credential": fixture.sidecars.credentialRaw, "runtime": fixture.sidecars.runtimeRaw,
		"goose": fixture.sidecars.gooseRaw, "storage": fixture.sidecars.storageRaw,
	} {
		if len(raw) == 0 {
			t.Fatalf("dynamic graph raw artifact missing: %s", name)
		}
	}
	if len(fixture.attestationPublic) == 0 || len(fixture.releasePrivate) != ed25519.PrivateKeySize {
		t.Fatal("dynamic graph signing material missing")
	}
}

func valuesForNames(t *testing.T, names []string, value func(string) string) []string {
	t.Helper()
	values := make([]string, len(names))
	for index, name := range names {
		values[index] = value(name)
		if values[index] == "" {
			t.Fatalf("fixture value missing: %s", name)
		}
	}
	return values
}

func releaseFieldNames(t *testing.T) []string {
	t.Helper()
	var names []string
	for field := ca42releasev3.Field(0); ; field++ {
		name := ca42releasev3.FieldName(field)
		if name == "" {
			break
		}
		names = append(names, name)
	}
	if len(names) == 0 || names[len(names)-1] != "signature_b64" {
		t.Fatal("release v3 field inventory unavailable")
	}
	return names
}

func graphHash(label string) string {
	digest := sha256.Sum256([]byte("ca42-artifact-set-v2:" + label))
	return hex.EncodeToString(digest[:])
}

func graphExternalManifest(t *testing.T, label string) []byte {
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
	image := graphHash("postgres-image:" + label)
	runSuffix := "ABC123"
	if label != "a" {
		runSuffix = "DEF456"
	}
	outer := map[string]any{
		"format": ca42manifest.Format, "status": "READY", "frozen_migration_sha256": ca42manifest.FrozenMigrationSHA256,
		"postgresql_major": 18, "barrier": ca42manifest.Barrier,
		"exact_object_manifest_sha256": fmt.Sprintf("%x", objectDigest),
		"source": map[string]any{
			"system_identifier": func() string {
				if label == "a" {
					return "2222222222222222222"
				}
				return "3333333333333333333"
			}(),
			"container_id": graphHash("isolated-container:" + label), "network_id": graphHash("isolated-network:" + label),
			"database": "aegis", "database_oid": "24576", "image_id": "sha256:" + image,
			"run_id": "pandoraisolatedpg18" + runSuffix + "-1700000000",
		},
		"object_manifest": json.RawMessage(objectBytes),
	}
	encoded, err := json.Marshal(outer)
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

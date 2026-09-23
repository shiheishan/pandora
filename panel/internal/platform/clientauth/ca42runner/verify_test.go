package ca42runner

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

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42capsule"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42execution"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42release"
)

func TestVerifyBundleAcceptsOneWayAuthorityChainAndRejectsDrift(t *testing.T) {
	fixture := newBundleFixture(t)
	verified, err := VerifyBundle(
		fixture.authority, fixture.release, fixture.execution, fixture.capsule, fixture.external, fixture.roots, "amd64",
		fixture.host, fixture.runner, "attempt-ca42-1", nil, fixture.now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if verified.Authority.BindingSHA256 == ([sha256.Size]byte{}) ||
		verified.Release.AuthorityBindingSHA256 != hex.EncodeToString(verified.Authority.BindingSHA256[:]) ||
		verified.PreviousLedger != nil || verified.Ledger.AuthoritySequence != 1 || verified.ExactRetry {
		t.Fatalf("verified bundle lost authority chain: %#v", verified)
	}
	retry, err := VerifyBundle(
		fixture.authority, fixture.release, fixture.execution, fixture.capsule, fixture.external, fixture.roots, "amd64",
		fixture.host, fixture.runner, "attempt-ca42-1", &verified.Ledger, fixture.now.Add(time.Second),
	)
	if err != nil || !retry.ExactRetry || retry.PreviousLedger == nil ||
		retry.PreviousLedger.RecordSHA256 != verified.Ledger.RecordSHA256 {
		t.Fatalf("exact bundle retry lost CAS snapshot: retry=%#v err=%v", retry, err)
	}
	wrongRunner := sha256.Sum256([]byte("wrong-runner"))
	if _, err := VerifyBundle(fixture.authority, fixture.release, fixture.execution, fixture.capsule, fixture.external, fixture.roots,
		"amd64", fixture.host, wrongRunner, "attempt-ca42-1", nil, fixture.now); err == nil {
		t.Fatal("accepted wrong running root runner")
	}
	if _, err := VerifyBundle(fixture.authority, fixture.release, fixture.execution, fixture.capsule, fixture.external, fixture.roots,
		"amd64", fixture.host, fixture.runner, "attempt-other", nil, fixture.now); err == nil {
		t.Fatal("accepted caller attempt mismatch")
	}
	mutatedExecution := append([]byte(nil), fixture.execution...)
	mutatedExecution[len(mutatedExecution)-2] ^= 1
	if _, err := VerifyBundle(fixture.authority, fixture.release, mutatedExecution, fixture.capsule, fixture.external, fixture.roots,
		"amd64", fixture.host, fixture.runner, "attempt-ca42-1", nil, fixture.now); err == nil {
		t.Fatal("accepted execution plan not pinned by the signed release")
	}
	mutatedCapsule := append([]byte(nil), fixture.capsule...)
	mutatedCapsule[len(mutatedCapsule)-2] ^= 1
	if _, err := VerifyBundle(fixture.authority, fixture.release, fixture.execution, mutatedCapsule, fixture.external, fixture.roots,
		"amd64", fixture.host, fixture.runner, "attempt-ca42-1", nil, fixture.now); err == nil {
		t.Fatal("accepted trust capsule not pinned by the execution plan")
	}
}

type bundleFixture struct {
	authority []byte
	release   []byte
	execution []byte
	capsule   []byte
	external  []byte
	roots     ca42authority.RootKeyset
	host      [sha256.Size]byte
	runner    [sha256.Size]byte
	now       time.Time
}

func newBundleFixture(t *testing.T) bundleFixture {
	t.Helper()
	rootPrivate := make([]ed25519.PrivateKey, 3)
	rootKeys := make([]ca42authority.RootKey, 3)
	for index := range rootPrivate {
		rootPrivate[index] = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(index + 21)}, ed25519.SeedSize))
		rootKeys[index] = ca42authority.RootKey{
			ID:        "root-" + string(rune('a'+index)),
			PublicKey: rootPrivate[index].Public().(ed25519.PublicKey),
		}
	}
	roots := ca42authority.RootKeyset{ID: "pandora-ca42-roots-v1", Quorum: 2, Keys: rootKeys}
	releasePrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{55}, ed25519.SeedSize))
	releasePublic := releasePrivate.Public().(ed25519.PublicKey)
	host := sha256.Sum256([]byte("host-identity"))
	runner := sha256.Sum256([]byte("root-runner-binary"))
	now := time.Unix(1700000100, 0).UTC()

	placeholderAuthority := buildAuthority(t, roots, rootPrivate, releasePublic, host, runner, [sha256.Size]byte{})
	parsed, err := ca42authority.ParseAndVerify(placeholderAuthority, roots, "amd64", host, now)
	if err != nil {
		t.Fatal(err)
	}
	external, receipt := bundleExternalManifest(t)
	capsule := buildTrustCapsule(t, receipt)
	capsuleDigest := sha256.Sum256(capsule)
	execution := buildExecutionPlan(t, receipt, capsuleDigest)
	executionDigest := sha256.Sum256(execution)
	release := buildRelease(t, releasePrivate, parsed.BindingSHA256, receipt, executionDigest)
	releaseDigest := sha256.Sum256(release)
	authority := buildAuthority(t, roots, rootPrivate, releasePublic, host, runner, releaseDigest)
	return bundleFixture{authority: authority, release: release, execution: execution, capsule: capsule, external: external, roots: roots, host: host, runner: runner, now: now}
}

func buildAuthority(t *testing.T, roots ca42authority.RootKeyset, private []ed25519.PrivateKey,
	releasePublic ed25519.PublicKey, host, runner, manifest [sha256.Size]byte) []byte {
	return buildAuthorityForArchitecture(t, roots, private, releasePublic, host, runner, manifest, "amd64")
}

func buildAuthorityForArchitecture(t *testing.T, roots ca42authority.RootKeyset, private []ed25519.PrivateKey,
	releasePublic ed25519.PublicKey, host, runner, manifest [sha256.Size]byte, architecture string) []byte {
	t.Helper()
	signerDigest := sha256.Sum256(releasePublic)
	values := []string{
		ca42authority.Format, ca42authority.SignatureAlgorithm, ca42authority.SignatureDomain,
		ca42authority.Purpose, ca42authority.Status, roots.ID, strings.Repeat("a", 64),
		"1", "1", strings.Repeat("0", 64), ca42authority.ModeNormal, architecture,
		hex.EncodeToString(host[:]), "release-ca42-1", "run-ca42-1", "attempt-ca42-1",
		hex.EncodeToString(manifest[:]), "release-signer-1", base64.StdEncoding.EncodeToString(releasePublic),
		hex.EncodeToString(signerDigest[:]), hex.EncodeToString(runner[:]),
		"1700000000", "1700000300", "1699999999",
	}
	keyIDs := [ca42authority.RequiredQuorum]string{"root-a", "root-b"}
	signed, err := ca42authority.SignatureInput(values, keyIDs)
	if err != nil {
		t.Fatal(err)
	}
	var result strings.Builder
	for index, value := range values {
		result.WriteString(authorityFieldNames[index] + "=" + value + "\n")
	}
	for index := 0; index < 2; index++ {
		result.WriteString("root_signature_" + string(rune('1'+index)) + "_key_id=root-" + string(rune('a'+index)) + "\n")
		result.WriteString("root_signature_" + string(rune('1'+index)) + "_b64=" +
			base64.StdEncoding.EncodeToString(ed25519.Sign(private[index], signed)) + "\n")
	}
	return []byte(result.String())
}

var authorityFieldNames = [...]string{
	"format", "signature_algorithm", "signature_domain", "purpose", "status", "root_keyset_id",
	"ledger_id", "authority_epoch", "authority_sequence", "previous_descriptor_sha256",
	"authorization_mode", "architecture", "host_identity_sha256", "release_id", "release_run_id",
	"attempt_id", "release_manifest_sha256", "release_signer_key_id", "release_signer_public_key_b64",
	"release_signer_sha256", "root_runner_sha256", "not_before_epoch", "not_after_epoch", "clock_floor_epoch",
}

func buildRelease(t *testing.T, private ed25519.PrivateKey, binding [sha256.Size]byte, external ca42manifest.Receipt, execution [sha256.Size]byte) []byte {
	t.Helper()
	values := []string{
		ca42release.Format, ca42release.SignatureAlgorithm, ca42release.Status, "1", "1",
		hex.EncodeToString(binding[:]), "release-signer-1", "amd64", "release-ca42-1", "run-ca42-1", "attempt-ca42-1",
		strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64), strings.Repeat("5", 64),
		ca42manifest.FrozenMigrationSHA256, strings.Repeat("6", 64), strings.Repeat("7", 64), strings.Repeat("c", 64),
		strings.Repeat("a", 64), "1111111111111111111", "aegis", "16384", "10", "aegis_owner", "41",
		strings.Repeat("b", 64), "2222222222222222222", strings.Repeat("d", 64), "aegis", "24576",
		"sha256:" + strings.Repeat("c", 64), "pandoraisolatedpg18ABC123-1700000000",
		external.ExternalManifestSHA256, external.ExactObjectManifestSHA256,
		strings.Repeat("e", 64), strings.Repeat("f", 64), hex.EncodeToString(execution[:]), "1700000000", "1700000300",
	}
	signed, err := ca42release.SignatureInput(values)
	if err != nil {
		t.Fatal(err)
	}
	return append(signed, []byte("signature_b64="+base64.StdEncoding.EncodeToString(ed25519.Sign(private, signed))+"\n")...)
}

func buildExecutionPlan(t *testing.T, external ca42manifest.Receipt, capsule [sha256.Size]byte) []byte {
	t.Helper()
	values := []string{
		ca42execution.Format, ca42execution.Status, ca42execution.Transition,
		"release-ca42-1", "run-ca42-1", "attempt-ca42-1", "amd64",
		strings.Repeat("8", 64), strings.Repeat("9", 64), "2049", ca42execution.RequiredExecutableMode,
		hex.EncodeToString(capsule[:]), strings.Repeat("2", 64), strings.Repeat("3", 64), "2050", ca42execution.RequiredExecutableMode,
		strings.Repeat("4", 64), strings.Repeat("5", 64), strings.Repeat("e", 64), external.ExternalManifestSHA256,
		strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("1", 64), strings.Repeat("4", 64),
		strings.Repeat("5", 64), ca42manifest.FrozenMigrationSHA256, strings.Repeat("6", 64), strings.Repeat("7", 64),
		strings.Repeat("c", 64), strings.Repeat("f", 64), strings.Repeat("d", 64), ca42execution.LedgerNamespace,
		ca42execution.LedgerDirectorySHA256, strings.Repeat("a", 64), "1111111111111111111", "aegis", "16384",
		"10", "aegis_owner", "41", strings.Repeat("b", 64), "2222222222222222222", strings.Repeat("d", 64),
		"aegis", "24576", "sha256:" + strings.Repeat("c", 64), "pandoraisolatedpg18ABC123-1700000000",
		"1700000000", "1700000300",
	}
	data, err := ca42execution.CanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func buildTrustCapsule(t *testing.T, external ca42manifest.Receipt) []byte {
	t.Helper()
	values := []string{
		ca42capsule.Format, ca42capsule.Transition, "release-ca42-1", "run-ca42-1",
		strings.Repeat("2", 64), strings.Repeat("3", 64), "2050", ca42capsule.CoreMode,
		strings.Repeat("4", 64), strings.Repeat("5", 64), strings.Repeat("e", 64), external.ExternalManifestSHA256,
		"1111111111111111111", "aegis", "16384", ca42execution.LedgerNamespace,
		ca42execution.LedgerDirectorySHA256,
	}
	data, err := ca42capsule.CanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func bundleExternalManifest(t *testing.T) ([]byte, ca42manifest.Receipt) {
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

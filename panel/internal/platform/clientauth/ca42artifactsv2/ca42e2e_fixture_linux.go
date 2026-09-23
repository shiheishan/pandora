//go:build ca42e2e && linux && (amd64 || arm64)

package ca42artifactsv2

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42attestationv3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42capsulev2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42controlv3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42credential"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42expectedv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42gooseinfo"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42protocolv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42releasev3"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42runtimeclosure"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42storage"
)

const CA42E2EGraphMarker = "pandora-ca42-e2e-canonical-graph-v1"

type CA42E2ERawInput struct {
	Now               time.Time
	Label             string
	ReleaseID         string
	ReleaseRunID      string
	AttemptID         string
	FrozenMigration42 []byte
}

type CA42E2ERuntimeContent struct {
	Spec ca42storage.CA42E2ERuntimeFile
	Data []byte
}

// CA42E2ERawInventory contains only fixture bytes and deterministic test
// signing material. It conveys no production authority and is excluded from
// ordinary builds.
type CA42E2ERawInventory struct {
	Now                  time.Time
	Architecture         string
	ReleaseID            string
	ReleaseRunID         string
	AttemptID            string
	Files                map[string][]byte
	Runtime              []CA42E2ERuntimeContent
	ExternalManifest     []byte
	CredentialDescriptor []byte
	RuntimeClosure       []byte
	GooseBuildInfo       []byte
	AttestationPublicKey []byte
	AttestationCore      []byte
	ReleasePrivate       ed25519.PrivateKey
	ReleasePublic        ed25519.PublicKey
	base                 map[string]string
	attestationPrivate   ed25519.PrivateKey
	credential           ca42credential.Descriptor
	runtimeClosure       ca42runtimeclosure.Manifest
	goose                ca42gooseinfo.BuildInfo
}

type CA42E2EMeasurements struct {
	PathtrustDevice       uint64
	AttestationCoreDevice uint64
	BashDevice            uint64
	DockerDevice          uint64
	PathtrustChainSHA256  string
	AttestationCoreChain  string
	BashChainSHA256       string
	DockerChainSHA256     string
}

type CA42E2EPreJournalGraph struct {
	ReleaseContractCoreSHA256 string
	raw                       CA42E2ERawInventory
	storage                   ca42storage.CA42E2EStorageResult
	authority                 ca42releasev3.AuthorityBinding
	expected                  ca42expectedv2.Expected
	attestation               ca42attestationv3.Attestation
	capsule                   ca42capsulev2.Capsule
	expectedRaw               []byte
	attestationRaw            []byte
	capsuleRaw                []byte
	base                      map[string]string
}

type CA42E2EControlDatum struct {
	Entry ca42controlv3.Entry
	Data  []byte
}

type CA42E2EGraphResult struct {
	Control                   [7]CA42E2EControlDatum
	Inputs                    Inputs
	ReleasePrivate            ed25519.PrivateKey
	ReleasePublic             ed25519.PublicKey
	ReleaseManifestSHA256     [sha256.Size]byte
	ReleaseContractCoreSHA256 string
	PlanSHA256                [sha256.Size]byte
}

// BuildCA42E2EPreJournalGraph closes the storage graph, builds the immutable
// expected/attestation/capsule subgraph, and derives the release contract core
// using provisional journal and plan identities that the core excludes.
func BuildCA42E2EPreJournalGraph(raw CA42E2ERawInventory, storage ca42storage.CA42E2EStorageResult,
	measurements CA42E2EMeasurements, authority ca42releasev3.AuthorityBinding) (CA42E2EPreJournalGraph, error) {
	var empty CA42E2EPreJournalGraph
	if raw.Now.IsZero() || raw.Architecture != runtime.GOARCH || storage.Tainted || !storage.Descriptor.IsParsed() ||
		len(storage.Raw) == 0 || storage.SealAttempts == 0 || storage.SealAttempts != storage.VerifiedSealedCount ||
		len(authority.PublicKey) != ed25519.PublicKeySize || authority.BindingSHA256 == ([sha256.Size]byte{}) ||
		authority.SignerSHA256 == ([sha256.Size]byte{}) || authority.ReleaseSignerKeyID == "" {
		return empty, errors.New("CA42 E2E pre-journal graph input invalid")
	}
	if measurements.PathtrustDevice == 0 || measurements.AttestationCoreDevice == 0 || measurements.BashDevice == 0 ||
		measurements.DockerDevice == 0 || !ca42E2EHex64(measurements.PathtrustChainSHA256) ||
		!ca42E2EHex64(measurements.AttestationCoreChain) || !ca42E2EHex64(measurements.BashChainSHA256) ||
		!ca42E2EHex64(measurements.DockerChainSHA256) {
		return empty, errors.New("CA42 E2E production measurements invalid")
	}
	base := make(map[string]string, len(raw.base)+16)
	for key, value := range raw.base {
		base[key] = value
	}
	coreSHA := sha256.Sum256(raw.AttestationCore)
	base["pathtrust_device"] = strconv.FormatUint(measurements.PathtrustDevice, 10)
	base["attestation_core_device"] = strconv.FormatUint(measurements.AttestationCoreDevice, 10)
	base["bash_device"] = strconv.FormatUint(measurements.BashDevice, 10)
	base["docker_client_device"] = strconv.FormatUint(measurements.DockerDevice, 10)
	base["pathtrust_chain_sha256"] = measurements.PathtrustChainSHA256
	base["attestation_core_chain_sha256"] = measurements.AttestationCoreChain
	base["bash_chain_sha256"] = measurements.BashChainSHA256
	base["docker_client_chain_sha256"] = measurements.DockerChainSHA256
	base["attestation_core_sha256"] = hex.EncodeToString(coreSHA[:])
	base["artifact_storage_descriptor_sha256"] = hex.EncodeToString(storage.Descriptor.SHA256[:])

	expectedValues, err := ca42E2EValues(ca42expectedv2.FieldNames(), func(name string) string {
		if name == "format" {
			return ca42expectedv2.Format
		}
		return base[name]
	})
	if err != nil {
		return empty, err
	}
	expectedRaw, err := ca42expectedv2.CanonicalBytes(expectedValues)
	if err != nil {
		return empty, err
	}
	expectedSHA := sha256.Sum256(expectedRaw)
	expected, err := ca42expectedv2.Parse(expectedRaw, expectedSHA, raw.Architecture, raw.Now)
	if err != nil {
		return empty, err
	}
	expectedSnapshot, err := expected.SnapshotAt(raw.Now)
	if err != nil {
		return empty, err
	}
	attestationNames := ca42attestationv3.FieldNames()
	attestationValues, err := ca42E2EValues(attestationNames[:len(attestationNames)-1], func(name string) string {
		switch name {
		case "format":
			return ca42attestationv3.Format
		case "signature_algorithm":
			return ca42attestationv3.SignatureAlgorithm
		case "profile_id":
			return ca42protocolv2.ProfileID
		case "profile_sha256":
			return base["profile_sha256"]
		case "expected_format":
			return ca42expectedv2.Format
		case "expected_sha256":
			return hex.EncodeToString(expectedSHA[:])
		case "nonce":
			return ca42E2EHash("nonce:" + raw.AttemptID)
		case "issued_at":
			return strconv.FormatInt(raw.Now.Add(-30*time.Second).Unix(), 10)
		case "expires_at":
			return strconv.FormatInt(raw.Now.Add(20*time.Minute).Unix(), 10)
		default:
			return expectedSnapshot.ValueName(name)
		}
	})
	if err != nil {
		return empty, err
	}
	unsignedAttestation, err := ca42attestationv3.UnsignedCanonicalBytes(attestationValues)
	if err != nil {
		return empty, err
	}
	attestationMessage, err := ca42attestationv3.SignatureMessage(unsignedAttestation)
	if err != nil {
		return empty, err
	}
	attestationRaw, err := ca42attestationv3.SignedCanonicalBytes(unsignedAttestation, ed25519.Sign(raw.attestationPrivate, attestationMessage))
	if err != nil {
		return empty, err
	}
	attestationSHA := sha256.Sum256(attestationRaw)
	attestation, err := ca42attestationv3.ParseAndVerify(attestationRaw, attestationSHA, expected, raw.AttestationPublicKey, raw.Now)
	if err != nil {
		return empty, err
	}
	attestationSnapshot, err := attestation.SnapshotAt(raw.Now)
	if err != nil {
		return empty, err
	}
	capsuleValues, err := ca42E2EValues(ca42capsulev2.FieldNames(), func(name string) string {
		switch name {
		case "format":
			return ca42capsulev2.Format
		case "attestation_format":
			return ca42attestationv3.Format
		case "attestation_sha256":
			return hex.EncodeToString(attestationSHA[:])
		case "expected_sha256":
			return hex.EncodeToString(expectedSHA[:])
		default:
			return attestationSnapshot.Value(name)
		}
	})
	if err != nil {
		return empty, err
	}
	capsuleRaw, err := ca42capsulev2.CanonicalBytes(capsuleValues)
	if err != nil {
		return empty, err
	}
	capsuleSHA := sha256.Sum256(capsuleRaw)
	capsule, err := ca42capsulev2.Parse(capsuleRaw, capsuleSHA)
	if err != nil {
		return empty, err
	}
	base["expected_sha256"] = hex.EncodeToString(expectedSHA[:])
	base["attestation_sha256"] = hex.EncodeToString(attestationSHA[:])
	base["trust_capsule_sha256"] = hex.EncodeToString(capsuleSHA[:])
	planPlaceholder := ca42E2EHash("provisional-plan:" + raw.AttemptID)
	journalHeadPlaceholder := ca42E2EHash("provisional-journal-head:" + raw.AttemptID)
	journalManifestPlaceholder := ca42E2EHash("provisional-journal-manifest:" + raw.AttemptID)
	provisional, _, err := ca42E2ERelease(raw, base, authority, planPlaceholder, journalHeadPlaceholder, journalManifestPlaceholder)
	if err != nil {
		return empty, err
	}
	core, err := ca42releasev3.ContractCoreSHA256Hex(provisional, raw.Now)
	if err != nil {
		return empty, err
	}
	return CA42E2EPreJournalGraph{ReleaseContractCoreSHA256: core, raw: raw, storage: storage, authority: authority,
		expected: expected, attestation: attestation, capsule: capsule, expectedRaw: expectedRaw,
		attestationRaw: attestationRaw, capsuleRaw: capsuleRaw, base: base}, nil
}

// FinalizeCA42E2EGraph binds the real Journal head and manifest to the final
// plan and release, then re-verifies the complete aggregate before returning
// the exact seven-file control layout.
func FinalizeCA42E2EGraph(pre CA42E2EPreJournalGraph, journalHeadSHA256, journalManifestSHA256 string) (CA42E2EGraphResult, error) {
	var empty CA42E2EGraphResult
	if !ca42E2EHex64(journalHeadSHA256) || !ca42E2EHex64(journalManifestSHA256) || pre.ReleaseContractCoreSHA256 == "" {
		return empty, errors.New("CA42 E2E final journal identity invalid")
	}
	base := make(map[string]string, len(pre.base)+4)
	for key, value := range pre.base {
		base[key] = value
	}
	base["release_journal_head_sha256"] = journalHeadSHA256
	base["release_journal_snapshot_sha256"] = journalManifestSHA256
	planValues, err := ca42E2EValues(ca42executionv2.CanonicalFieldNames(), func(name string) string {
		switch name {
		case "format":
			return ca42executionv2.Format
		case "status":
			return ca42executionv2.Status
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
	if err != nil {
		return empty, err
	}
	planRaw, err := ca42executionv2.CanonicalBytes(planValues)
	if err != nil {
		return empty, err
	}
	planSHA := sha256.Sum256(planRaw)
	plan, err := ca42executionv2.Parse(planRaw, planSHA, pre.raw.Architecture, pre.raw.Now)
	if err != nil {
		return empty, err
	}
	release, releaseRaw, err := ca42E2ERelease(pre.raw, base, pre.authority, hex.EncodeToString(planSHA[:]), journalHeadSHA256, journalManifestSHA256)
	if err != nil {
		return empty, err
	}
	finalCore, err := ca42releasev3.ContractCoreSHA256Hex(release, pre.raw.Now)
	if err != nil || finalCore != pre.ReleaseContractCoreSHA256 {
		return empty, errors.New("CA42 E2E provisional/final release contract core mismatch")
	}
	set, err := New(Inputs{Release: release, Plan: plan, Capsule: pre.capsule, Expected: pre.expected, Attestation: pre.attestation,
		ExternalManifest: pre.raw.ExternalManifest, CredentialDescriptor: pre.raw.credential,
		RuntimeClosure: pre.raw.runtimeClosure, GooseBuildInfo: pre.raw.goose, StorageDescriptor: pre.storage.Descriptor}, pre.raw.Now)
	if err != nil {
		return empty, fmt.Errorf("CA42 E2E final aggregate invalid: %w", err)
	}
	if _, err := set.SnapshotAt(pre.raw.Now); err != nil {
		return empty, err
	}
	entries := ca42controlv3.Entries()
	data := [7][]byte{releaseRaw, planRaw, pre.capsuleRaw, pre.raw.AttestationCore, pre.attestationRaw, pre.expectedRaw, pre.storage.Raw}
	var control [7]CA42E2EControlDatum
	for index := range entries {
		control[index] = CA42E2EControlDatum{Entry: entries[index], Data: append([]byte(nil), data[index]...)}
	}
	releaseSHA := sha256.Sum256(releaseRaw)
	return CA42E2EGraphResult{Control: control, Inputs: Inputs{Release: release, Plan: plan, Capsule: pre.capsule,
		Expected: pre.expected, Attestation: pre.attestation, ExternalManifest: append([]byte(nil), pre.raw.ExternalManifest...),
		CredentialDescriptor: pre.raw.credential, RuntimeClosure: pre.raw.runtimeClosure, GooseBuildInfo: pre.raw.goose,
		StorageDescriptor: pre.storage.Descriptor}, ReleasePrivate: append(ed25519.PrivateKey(nil), pre.raw.ReleasePrivate...),
		ReleasePublic: append(ed25519.PublicKey(nil), pre.raw.ReleasePublic...), ReleaseManifestSHA256: releaseSHA,
		ReleaseContractCoreSHA256: finalCore, PlanSHA256: planSHA}, nil
}

func ca42E2ERelease(raw CA42E2ERawInventory, base map[string]string, authority ca42releasev3.AuthorityBinding,
	planSHA256, journalHeadSHA256, journalManifestSHA256 string) (ca42releasev3.Manifest, []byte, error) {
	var empty ca42releasev3.Manifest
	base["release_journal_head_sha256"] = journalHeadSHA256
	base["release_journal_snapshot_sha256"] = journalManifestSHA256
	names := ca42E2EReleaseFieldNames()
	values, err := ca42E2EValues(names[:len(names)-1], func(name string) string {
		switch name {
		case "format":
			return ca42releasev3.Format
		case "signature_algorithm":
			return ca42releasev3.SignatureAlgorithm
		case "status":
			return ca42releasev3.Status
		case "authority_epoch":
			return strconv.FormatUint(authority.Epoch, 10)
		case "authority_sequence":
			return strconv.FormatUint(authority.Sequence, 10)
		case "authority_binding_sha256":
			return hex.EncodeToString(authority.BindingSHA256[:])
		case "release_signer_key_id":
			return authority.ReleaseSignerKeyID
		case "execution_plan_format":
			return ca42executionv2.Format
		case "execution_plan_sha256":
			return planSHA256
		case "release_journal_format":
			return ca42protocolv2.ReleaseJournalFormat
		case "release_journal_manifest_format":
			return ca42protocolv2.ReleaseJournalManifestFormat
		case "release_journal_namespace":
			return ca42protocolv2.ReleaseJournalNamespace
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
	if err != nil {
		return empty, nil, err
	}
	seenHash := make(map[string]string)
	for index, value := range values {
		if !ca42E2EHex64(value) {
			continue
		}
		if previous, duplicate := seenHash[value]; duplicate {
			return empty, nil, fmt.Errorf("CA42 E2E release fixture hash alias: %s=%s", previous, names[index])
		}
		seenHash[value] = names[index]
	}
	unsigned, err := ca42releasev3.UnsignedCanonicalBytes(values)
	if err != nil {
		return empty, nil, err
	}
	message, err := ca42releasev3.SignatureMessage(unsigned)
	if err != nil {
		return empty, nil, err
	}
	signed, err := ca42releasev3.SignedCanonicalBytes(unsigned, ed25519.Sign(raw.ReleasePrivate, message))
	if err != nil {
		return empty, nil, err
	}
	bound := authority
	bound.ManifestSHA256 = sha256.Sum256(signed)
	parsed, err := ca42releasev3.ParseAndVerify(signed, bound, raw.Architecture, raw.Now, raw.ExternalManifest)
	return parsed, signed, err
}

func ca42E2EValues(names []string, value func(string) string) ([]string, error) {
	values := make([]string, len(names))
	for index, name := range names {
		values[index] = value(name)
		if values[index] == "" {
			return nil, fmt.Errorf("CA42 E2E graph value missing: %s", name)
		}
	}
	return values, nil
}

func ca42E2EReleaseFieldNames() []string {
	var names []string
	for field := ca42releasev3.Field(0); ; field++ {
		name := ca42releasev3.FieldName(field)
		if name == "" {
			return names
		}
		names = append(names, name)
	}
}

func ca42E2EHex64(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(value) == sha256.Size*2 && len(decoded) == sha256.Size && value != strings.Repeat("0", sha256.Size*2)
}

// PrepareCA42E2ERawInventory builds every pre-storage byte through the same
// canonical sidecar parsers used by the production graph. The caller publishes
// Files and Runtime to the isolated ext4 image before requesting a descriptor.
func PrepareCA42E2ERawInventory(input CA42E2ERawInput) (CA42E2ERawInventory, error) {
	var empty CA42E2ERawInventory
	now := input.Now.UTC().Truncate(time.Second)
	if now.IsZero() || input.Label == "" || input.ReleaseID == "" || input.ReleaseRunID == "" || input.AttemptID == "" ||
		runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return empty, errors.New("CA42 E2E raw inventory identity invalid")
	}
	frozenSHA := sha256.Sum256(input.FrozenMigration42)
	if len(input.FrozenMigration42) == 0 || hex.EncodeToString(frozenSHA[:]) != ca42manifest.FrozenMigrationSHA256 {
		return empty, errors.New("CA42 E2E frozen migration 00042 identity invalid")
	}
	external, receipt, err := ca42E2EExternalManifest(input.Label)
	if err != nil {
		return empty, err
	}
	profileSHA, err := ca42protocolv2.Strict().SHA256()
	if err != nil {
		return empty, err
	}
	attestationSeed := sha256.Sum256([]byte("ca42-e2e-attestation:" + input.Label))
	attestationPrivate := ed25519.NewKeyFromSeed(attestationSeed[:])
	attestationPublic := attestationPrivate.Public().(ed25519.PublicKey)
	der, err := x509.MarshalPKIXPublicKey(attestationPublic)
	if err != nil {
		return empty, err
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	publicSHA := sha256.Sum256(publicPEM)

	content := map[string][]byte{
		"pathtrust_binary":   []byte("ELF-ca42-e2e-pathtrust-" + input.Label + "\n"),
		"manifest_verifier":  []byte("ELF-ca42-e2e-manifest-verifier-" + input.Label + "\n"),
		"preflight_runner":   []byte("ELF-ca42-e2e-preflight-runner-" + input.Label + "\n"),
		"migration_runner":   []byte("ELF-ca42-e2e-migration-runner-" + input.Label + "\n"),
		"goose_binary":       bytes.Repeat([]byte("G"), 4096),
		"migration_manifest": []byte("pandora-ca42-e2e-migration-set=" + input.Label + "\n"),
		"globals_dump":       bytes.Repeat([]byte("g"), 1024),
		"database_dump":      bytes.Repeat([]byte("d"), 4096),
		"bash":               []byte("ELF-ca42-e2e-bash-" + input.Label + "\n"),
		"docker":             []byte("ELF-ca42-e2e-docker-" + input.Label + "\n"),
		"runtime_loader":     []byte("ELF-ca42-e2e-loader-" + input.Label + "\n"),
		"runtime_library":    []byte("ELF-ca42-e2e-libc-" + input.Label + "\n"),
	}
	base := map[string]string{
		"profile_id": ca42protocolv2.ProfileID, "profile_sha256": hex.EncodeToString(profileSHA[:]),
		"attestation_format": ca42attestationv3.Format, "attestation_signature_algorithm": ca42attestationv3.SignatureAlgorithm,
		"expected_format": ca42expectedv2.Format,
		"release_id":      input.ReleaseID, "release_run_id": input.ReleaseRunID, "attempt_id": input.AttemptID,
		"architecture": runtime.GOARCH, "transition": ca42executionv2.Transition,
		"attestation_public_key_sha256":   hex.EncodeToString(publicSHA[:]),
		"pathtrust_mode":                  ca42executionv2.RequiredAttemptExecutableMode,
		"attestation_core_mode":           ca42executionv2.RequiredAttemptExecutableMode,
		"bash_mode":                       ca42executionv2.RequiredSystemExecutableMode,
		"docker_client_mode":              ca42executionv2.RequiredSystemExecutableMode,
		"goose_version":                   ca42executionv2.RequiredGooseVersion,
		"artifact_storage_profile":        ca42executionv2.RequiredStorageProfile,
		"external_manifest_sha256":        receipt.ExternalManifestSHA256,
		"external_object_manifest_sha256": receipt.ExactObjectManifestSHA256,
		"source_system_identifier":        "1111111111111111111", "source_database_name": "aegis",
		"source_database_oid": "16384", "source_database_owner_oid": "10", "source_database_owner_name": "aegis_owner",
		"source_goose_waterline": "41", "isolated_container_id": receipt.IsolatedContainerID,
		"isolated_system_identifier": receipt.IsolatedSystemIdentifier, "isolated_network_id": receipt.IsolatedNetworkID,
		"isolated_database_name": receipt.IsolatedDatabase, "isolated_database_oid": receipt.IsolatedDatabaseOID,
		"isolated_image_id": receipt.IsolatedImageID, "isolated_run_id": receipt.IsolatedRunID,
		"ledger_namespace": ca42protocolv2.LedgerNamespace, "ledger_directory_sha256": ca42executionv2.LedgerDirectorySHA256,
		"not_before_epoch":         strconv.FormatInt(now.Add(-time.Minute).Unix(), 10),
		"not_after_epoch":          strconv.FormatInt(now.Add(30*time.Minute).Unix(), 10),
		"client_auth_00042_sha256": ca42manifest.FrozenMigrationSHA256,
	}
	base["source_container_id"] = ca42E2EHash("source-container:" + input.Label)
	base["postgres_image_sha256"] = strings.TrimPrefix(receipt.IsolatedImageID, "sha256:")
	for role, field := range map[string]string{
		"pathtrust_binary": "pathtrust_binary_sha256", "manifest_verifier": "manifest_verifier_sha256",
		"preflight_runner": "preflight_runner_sha256", "migration_runner": "migration_runner_sha256",
		"goose_binary": "goose_binary_sha256", "migration_manifest": "migration_set_sha256",
		"globals_dump": "globals_dump_sha256", "database_dump": "database_dump_sha256",
		"bash": "bash_binary_sha256", "docker": "docker_client_sha256",
	} {
		digest := sha256.Sum256(content[role])
		base[field] = hex.EncodeToString(digest[:])
	}
	base["globals_dump_size_bytes"] = strconv.Itoa(len(content["globals_dump"]))
	base["database_dump_size_bytes"] = strconv.Itoa(len(content["database_dump"]))

	credential, credentialRaw, err := ca42E2ECredential(base, input.Label, now)
	if err != nil {
		return empty, err
	}
	base["credential_source_descriptor_sha256"] = hex.EncodeToString(credential.SHA256[:])
	goose, gooseRaw, err := ca42E2EGoose(base, input.Label, content["goose_binary"])
	if err != nil {
		return empty, err
	}
	base["goose_build_info_sha256"] = hex.EncodeToString(goose.SHA256[:])
	runtimeClosure, runtimeRaw, err := ca42E2ERuntime(base, input.Label)
	if err != nil {
		return empty, err
	}
	base["runtime_closure_manifest_sha256"] = hex.EncodeToString(runtimeClosure.SHA256[:])

	for _, names := range [][]string{ca42expectedv2.FieldNames(), ca42capsulev2.FieldNames(), ca42executionv2.CanonicalFieldNames()} {
		for _, name := range names {
			if _, exists := base[name]; !exists && strings.HasSuffix(name, "_sha256") {
				base[name] = ca42E2EHash(input.Label + ":" + name)
			}
		}
	}
	loaderPath := map[string]string{"amd64": "/lib64/ld-linux-x86-64.so.2", "arm64": "/lib/ld-linux-aarch64.so.1"}[runtime.GOARCH]
	runtimeContent := []CA42E2ERuntimeContent{
		{Spec: ca42storage.CA42E2ERuntimeFile{Role: "bash", Path: "/usr/bin/bash", Mode: 0o755}, Data: content["bash"]},
		{Spec: ca42storage.CA42E2ERuntimeFile{Role: "docker", Path: "/usr/bin/docker", Mode: 0o755}, Data: content["docker"]},
		{Spec: ca42storage.CA42E2ERuntimeFile{Role: "runtime_loader", Path: loaderPath, Mode: 0o755}, Data: content["runtime_loader"]},
		{Spec: ca42storage.CA42E2ERuntimeFile{Role: "runtime_library", Path: "/usr/lib/libc.so.6", Mode: 0o644}, Data: content["runtime_library"]},
	}
	runtimeSpecs := make([]ca42storage.CA42E2ERuntimeFile, len(runtimeContent))
	for index := range runtimeContent {
		runtimeSpecs[index] = runtimeContent[index].Spec
	}
	layout, err := ca42storage.CA42E2ERequiredInventoryFiles(input.AttemptID, runtime.GOARCH, runtimeSpecs)
	if err != nil {
		return empty, err
	}
	files := make(map[string][]byte, len(layout))
	for index, file := range layout {
		switch file.Scope {
		case "attempt":
			switch file.Role {
			case "external_manifest":
				files[file.Path] = append([]byte(nil), external...)
			case "attestation_public_key":
				files[file.Path] = append([]byte(nil), publicPEM...)
			case "credential_source_descriptor":
				files[file.Path] = append([]byte(nil), credentialRaw...)
			case "runtime_closure_manifest":
				files[file.Path] = append([]byte(nil), runtimeRaw...)
			case "goose_build_info":
				files[file.Path] = append([]byte(nil), gooseRaw...)
			default:
				files[file.Path] = append([]byte(nil), content[file.Role]...)
			}
		case "migration":
			if filepath.Base(file.Path) == "00042_client_auth_expand.sql" {
				files[file.Path] = append([]byte(nil), input.FrozenMigration42...)
			} else {
				files[file.Path] = []byte(fmt.Sprintf("-- CA42 E2E %s\nSELECT %d;\n", filepath.Base(file.Path), index+1))
			}
		case "system":
			// Runtime bytes are published through Runtime so the caller can bind
			// them onto the exact system paths inside the private mount namespace.
		default:
			return empty, errors.New("CA42 E2E inventory layout scope invalid")
		}
	}
	releaseSeed := sha256.Sum256([]byte("ca42-e2e-release:" + input.Label))
	releasePrivate := ed25519.NewKeyFromSeed(releaseSeed[:])
	return CA42E2ERawInventory{
		Now: now, Architecture: runtime.GOARCH, ReleaseID: input.ReleaseID, ReleaseRunID: input.ReleaseRunID, AttemptID: input.AttemptID,
		Files: files, Runtime: runtimeContent, ExternalManifest: external, CredentialDescriptor: credentialRaw,
		RuntimeClosure: runtimeRaw, GooseBuildInfo: gooseRaw, AttestationPublicKey: publicPEM,
		AttestationCore: []byte("ELF-ca42-e2e-attestation-core-" + input.Label + "\n"),
		ReleasePrivate:  releasePrivate, ReleasePublic: releasePrivate.Public().(ed25519.PublicKey),
		base: base, attestationPrivate: attestationPrivate, credential: credential, runtimeClosure: runtimeClosure, goose: goose,
	}, nil
}

func ca42E2ECredential(base map[string]string, label string, now time.Time) (ca42credential.Descriptor, []byte, error) {
	secret := bytes.Repeat([]byte("s"), ca42credential.MinCredentialBytes)
	key := sha256.Sum256([]byte("ca42-e2e-credential-key:" + label))
	raw := ca42credential.Descriptor{CommitmentAlgorithm: ca42credential.CommitmentHMACKeyringV1, CommitmentKeyID: "keyring-" + label,
		ReleaseID: base["release_id"], ReleaseRunID: base["release_run_id"], AttemptID: base["attempt_id"],
		SourceContainerID: base["source_container_id"], SourceSystemIdentifier: base["source_system_identifier"],
		SourceDatabase: base["source_database_name"], SourceDatabaseOID: base["source_database_oid"],
		SourceDatabaseOwner: base["source_database_owner_name"], SourceDatabaseOwnerOID: base["source_database_owner_oid"],
		CredentialSizeBytes: uint64(len(secret)), NotBefore: now, NotAfter: now.Add(20 * time.Minute)}
	commitment, err := ca42credential.HMACCommitment(secret, key[:], raw)
	if err != nil {
		return ca42credential.Descriptor{}, nil, err
	}
	values := []string{ca42credential.Format, ca42credential.Kind, ca42credential.CredentialName, ca42credential.CredentialFormat,
		raw.CommitmentAlgorithm, raw.CommitmentKeyID, commitment, ca42credential.Mode, strconv.FormatUint(raw.CredentialSizeBytes, 10),
		ca42credential.Delivery, raw.ReleaseID, raw.ReleaseRunID, raw.AttemptID, raw.SourceContainerID, raw.SourceSystemIdentifier,
		raw.SourceDatabase, raw.SourceDatabaseOID, raw.SourceDatabaseOwner, raw.SourceDatabaseOwnerOID,
		strconv.FormatInt(raw.NotBefore.Unix(), 10), strconv.FormatInt(raw.NotAfter.Unix(), 10)}
	data, err := ca42credential.CanonicalBytes(values)
	if err != nil {
		return ca42credential.Descriptor{}, nil, err
	}
	digest := sha256.Sum256(data)
	parsed, err := ca42credential.Parse(data, digest, now)
	return parsed, data, err
}

func ca42E2EGoose(base map[string]string, label string, binary []byte) (ca42gooseinfo.BuildInfo, []byte, error) {
	machine := map[string]string{"amd64": "EM_X86_64", "arm64": "EM_AARCH64"}[runtime.GOARCH]
	values := []string{ca42gooseinfo.Format, "goose", base["goose_binary_sha256"], strconv.Itoa(len(binary)), runtime.GOARCH,
		ca42gooseinfo.RequiredGoVersion, ca42gooseinfo.RequiredCommandPath, ca42gooseinfo.RequiredMainModulePath,
		ca42gooseinfo.RequiredGooseVersion, ca42gooseinfo.RequiredMainModuleSum, "none", "1", ca42E2EHash("goose-deps:" + label),
		"1", ca42E2EHash("goose-settings:" + label), "false", "exe", "true", "git", ca42gooseinfo.RequiredVCSRevision,
		ca42gooseinfo.RequiredVCSTime, "false", "ELFCLASS64", "ELFDATA2LSB", "ELFOSABI_NONE", "ET_EXEC", machine,
		"absent", "0", "absent", "absent"}
	data, err := ca42gooseinfo.CanonicalBytes(values)
	if err != nil {
		return ca42gooseinfo.BuildInfo{}, nil, err
	}
	digest := sha256.Sum256(data)
	parsed, err := ca42gooseinfo.Parse(data, digest, base["goose_binary_sha256"], runtime.GOARCH)
	return parsed, data, err
}

func ca42E2ERuntime(base map[string]string, label string) (ca42runtimeclosure.Manifest, []byte, error) {
	values := []string{ca42runtimeclosure.Format, base["release_id"], base["release_run_id"], base["attempt_id"], runtime.GOARCH,
		ca42runtimeclosure.EntryCount, ca42E2EHash("root-runner:" + label), ca42E2EHash("root-runner-info:" + label),
		base["pathtrust_binary_sha256"], ca42E2EHash("pathtrust-info:" + label), base["manifest_verifier_sha256"],
		ca42E2EHash("manifest-verifier-info:" + label), base["preflight_runner_sha256"], ca42E2EHash("preflight-info:" + label),
		base["migration_runner_sha256"], ca42E2EHash("migration-info:" + label), base["goose_binary_sha256"],
		base["goose_build_info_sha256"], base["docker_client_sha256"], "none"}
	data, err := ca42runtimeclosure.CanonicalBytes(values)
	if err != nil {
		return ca42runtimeclosure.Manifest{}, nil, err
	}
	digest := sha256.Sum256(data)
	parsed, err := ca42runtimeclosure.Parse(data, digest, runtime.GOARCH)
	return parsed, data, err
}

func ca42E2EHash(label string) string {
	digest := sha256.Sum256([]byte("ca42-e2e-canonical-graph:" + label))
	return hex.EncodeToString(digest[:])
}

func ca42E2EExternalManifest(label string) ([]byte, ca42manifest.Receipt, error) {
	identities := []string{"app.client_auth_00042_meta", "public.devices", "public.device_authorizations", "public.config_bundles",
		"public.refresh_tokens", "public.config_bundle_nodes", "public.refresh_families", "public.device_proof_nonces",
		"public.client_access_token_jtis", "public.device_issuance_response_replays", "public.client_refresh_response_replays",
		"public.device_issuance_replay_uses", "public.client_refresh_replay_uses", "public.device_proof_nonces_id_seq",
		"public.client_access_token_jtis_id_seq"}
	sort.Strings(identities)
	relations := make([]map[string]any, 0, len(identities))
	for _, identity := range identities {
		parts := strings.SplitN(identity, ".", 2)
		relation := map[string]any{"schema": parts[0], "name": parts[1], "kind": "r", "persistence": "p",
			"owner": "aegis_client_auth_owner", "rls": false, "force_rls": false, "replica_identity": "d", "comment": nil,
			"columns": []any{}, "constraints": []any{}, "indexes": []any{}, "policies": []any{}, "triggers": []any{}, "acl": []any{}, "sequence": nil}
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
	object := map[string]any{"format": ca42manifest.ObjectFormat, "contract_sha256": ca42manifest.ContractSHA256, "role_creation_mask": 3,
		"role_oid_manifest":         map[string]any{"aegis_client_auth_owner": 10, "aegis_client_auth_gc_owner": 11, "aegis_client_auth_gc": 12, "aegis_client_keyring_preflight_owner": 13},
		"portable_catalog_manifest": map[string]any{"format": ca42manifest.CatalogFormat, "relations": relations},
		"source_object_provenance": map[string]any{"namespaces": []any{tuple(5), tuple(5)}, "relations": provenanceRelations,
			"attributes": []any{}, "constraints": []any{}, "indexes": []any{}, "policies": []any{}, "triggers": []any{},
			"roles": provenanceRoles, "role_descriptions": []any{}}}
	objectBytes, err := json.Marshal(object)
	if err != nil {
		return nil, ca42manifest.Receipt{}, err
	}
	objectDigest := sha256.Sum256(objectBytes)
	outer := map[string]any{"format": ca42manifest.Format, "status": "READY", "frozen_migration_sha256": ca42manifest.FrozenMigrationSHA256,
		"postgresql_major": 18, "barrier": ca42manifest.Barrier, "exact_object_manifest_sha256": fmt.Sprintf("%x", objectDigest),
		"source": map[string]any{"system_identifier": "2222222222222222222", "container_id": ca42E2EHash("isolated-container:" + label),
			"network_id": ca42E2EHash("isolated-network:" + label), "database": "aegis", "database_oid": "24576",
			"image_id": "sha256:" + ca42E2EHash("postgres-image:"+label), "run_id": "pandoraisolatedpg18ABC123-1700000000"},
		"object_manifest": json.RawMessage(objectBytes)}
	encoded, err := json.Marshal(outer)
	if err != nil {
		return nil, ca42manifest.Receipt{}, err
	}
	encoded = append(encoded, '\n')
	receipt, err := ca42manifest.Verify(encoded)
	return encoded, receipt, err
}

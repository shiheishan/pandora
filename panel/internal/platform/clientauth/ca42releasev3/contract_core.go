package ca42releasev3

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42protocolv2"
)

var contractCoreDomain = []byte("PANDORA\x00CA42-RELEASE-CONTRACT-CORE\x00V2\x00")

type coreField struct {
	name  string
	field Field
	value string
}

// ContractCoreBytes produces the stable journal-facing contract. Journal
// heads/snapshot, the execution-plan digest and the release signature are
// deliberately excluded to keep the release -> plan -> journal graph acyclic.
func ContractCoreBytes(manifest Manifest, now time.Time) ([]byte, error) {
	verified, err := manifest.VerifiedCopyAt(now)
	if err != nil {
		return nil, err
	}
	snapshot := verified.snapshot
	fields := []coreField{
		{name: "format", value: ca42protocolv2.ReleaseContractCoreFormat},
		{name: "profile_id", field: FieldProfileID},
		{name: "profile_sha256", field: FieldProfileSHA256},
		{name: "release_manifest_format", value: Format},
		{name: "signature_algorithm", field: FieldSignatureAlgorithm},
		{name: "release_status", field: FieldStatus},
		{name: "execution_plan_format", value: ca42protocolv2.ExecutionPlanFormat},
		{name: "trust_capsule_format", value: ca42protocolv2.TrustCapsuleFormat},
		{name: "attestation_format", value: ca42protocolv2.AttestationFormat},
		{name: "expected_format", value: ca42protocolv2.ExpectedFormat},
		{name: "release_journal_format", value: ca42protocolv2.ReleaseJournalFormat},
		{name: "release_journal_manifest_format", value: ca42protocolv2.ReleaseJournalManifestFormat},
		{name: "release_journal_namespace", value: ca42protocolv2.ReleaseJournalNamespace},
		{name: "controller_contract", value: ca42protocolv2.ControllerContract},
		{name: "authority_epoch", field: FieldAuthorityEpoch},
		{name: "authority_sequence", field: FieldAuthoritySequence},
		{name: "authority_binding_sha256", field: FieldAuthorityBindingSHA256},
		{name: "release_signer_key_id", field: FieldReleaseSignerKeyID},
		{name: "architecture", field: FieldArchitecture},
		{name: "release_id", field: FieldReleaseID},
		{name: "release_run_id", field: FieldReleaseRunID},
		{name: "attempt_id", field: FieldAttemptID},
		{name: "transition", field: FieldTransition},
	}
	for field := FieldCredentialSourceDescriptorSHA256; field <= FieldReleaseJournalNamespace; field++ {
		if field == FieldAttestationFormat || field == FieldExpectedFormat ||
			field == FieldReleaseJournalFormat || field == FieldReleaseJournalManifestFormat ||
			field == FieldReleaseJournalNamespace {
			continue
		}
		fields = append(fields, coreField{name: FieldName(field), field: field})
	}
	fields = append(fields,
		coreField{name: "authorized_target_goose_waterline", value: TargetWaterline},
		coreField{name: "postgresql_major", value: PostgreSQLMajor},
		coreField{name: "external_manifest_format", value: ca42manifest.Format},
		coreField{name: "external_object_manifest_format", value: ca42manifest.ObjectFormat},
		coreField{name: "external_catalog_format", value: ca42manifest.CatalogFormat},
		coreField{name: "external_contract_sha256", value: ca42manifest.ContractSHA256},
		coreField{name: "external_barrier", value: ca42manifest.Barrier},
		coreField{name: "not_before_epoch", field: FieldNotBeforeEpoch},
		coreField{name: "not_after_epoch", field: FieldNotAfterEpoch},
	)
	var output strings.Builder
	for _, item := range fields {
		value := item.value
		if item.field != 0 {
			value = snapshot.Value(item.field)
		}
		if item.name == "" || value == "" || strings.ContainsAny(item.name+value, "\r\n\x00") {
			return nil, errors.New("release contract core v2 field invalid")
		}
		fmt.Fprintf(&output, "%s=%s\n", item.name, value)
	}
	return []byte(output.String()), nil
}

func ContractCoreSHA256(manifest Manifest, now time.Time) ([sha256.Size]byte, error) {
	data, err := ContractCoreBytes(manifest, now)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	hasher := sha256.New()
	_, _ = hasher.Write(contractCoreDomain)
	_, _ = hasher.Write(data)
	var result [sha256.Size]byte
	copy(result[:], hasher.Sum(nil))
	return result, nil
}

func ContractCoreSHA256Hex(manifest Manifest, now time.Time) (string, error) {
	digest, err := ContractCoreSHA256(manifest, now)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(digest[:]), nil
}

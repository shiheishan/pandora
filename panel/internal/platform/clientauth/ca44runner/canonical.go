package ca44runner

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type expectations struct {
	ExpectationsFormat     string `json:"expectations_format"`
	ReleaseID              string `json:"release_id"`
	ContractSHA256         string `json:"contract_sha256"`
	ClassifierSHA256       string `json:"classifier_sha256"`
	VerifierSHA256         string `json:"verifier_sha256"`
	RuleVersion            string `json:"rule_version"`
	SourceFormat           string `json:"source_format"`
	SourceLength           uint64 `json:"source_length"`
	SourceSHA256           string `json:"source_sha256"`
	ArtifactFormat         string `json:"artifact_format"`
	ArtifactLength         uint64 `json:"artifact_length"`
	ArtifactSHA256         string `json:"artifact_sha256"`
	ArtifactHMACVersion    string `json:"artifact_hmac_version"`
	ArtifactHMACKeyID      string `json:"artifact_hmac_key_id"`
	ArtifactHMACSHA256     string `json:"artifact_hmac_sha256"`
	EvidenceHMACKeyID      string `json:"evidence_hmac_key_id"`
	InputRows              uint64 `json:"input_rows"`
	OutputRows             uint64 `json:"output_rows"`
	Provable               uint64 `json:"provable"`
	Orphan                 uint64 `json:"orphan"`
	CrossTenant            uint64 `json:"cross_tenant"`
	UnprovableKey          uint64 `json:"unprovable_key"`
	DetachedManifestFormat string `json:"detached_manifest_format"`
	ReceiptFormat          string `json:"receipt_format"`
}

type detachedManifest struct {
	ManifestFormat      string `json:"manifest_format"`
	ArtifactHMACVersion string `json:"artifact_hmac_version"`
	ArtifactHMACKeyID   string `json:"artifact_hmac_key_id"`
	ArtifactFormat      string `json:"artifact_format"`
	SourceFormat        string `json:"source_format"`
	SourceLength        uint64 `json:"source_length"`
	ArtifactLength      uint64 `json:"artifact_length"`
	SourceSHA256        string `json:"source_sha256"`
	ArtifactSHA256      string `json:"artifact_sha256"`
	ArtifactHMACSHA256  string `json:"artifact_hmac_sha256"`
	InputRows           uint64 `json:"input_rows"`
	OutputRows          uint64 `json:"output_rows"`
	Provable            uint64 `json:"provable"`
	Orphan              uint64 `json:"orphan"`
	CrossTenant         uint64 `json:"cross_tenant"`
	UnprovableKey       uint64 `json:"unprovable_key"`
}

type verifierReceipt struct {
	ReceiptFormat             string `json:"receipt_format"`
	Decision                  string `json:"decision"`
	ReleaseID                 string `json:"release_id"`
	ContractSHA256            string `json:"contract_sha256"`
	ClassifierSHA256          string `json:"classifier_sha256"`
	VerifierSHA256            string `json:"verifier_sha256"`
	RuleVersion               string `json:"rule_version"`
	SourceFormat              string `json:"source_format"`
	SourceLength              uint64 `json:"source_length"`
	SourceSHA256              string `json:"source_sha256"`
	ArtifactFormat            string `json:"artifact_format"`
	ArtifactLength            uint64 `json:"artifact_length"`
	ArtifactSHA256            string `json:"artifact_sha256"`
	ArtifactHMACVersion       string `json:"artifact_hmac_version"`
	ArtifactHMACKeyID         string `json:"artifact_hmac_key_id"`
	ArtifactHMACSHA256        string `json:"artifact_hmac_sha256"`
	EvidenceHMACKeyID         string `json:"evidence_hmac_key_id"`
	InputRows                 uint64 `json:"input_rows"`
	OutputRows                uint64 `json:"output_rows"`
	Provable                  uint64 `json:"provable"`
	Orphan                    uint64 `json:"orphan"`
	CrossTenant               uint64 `json:"cross_tenant"`
	UnprovableKey             uint64 `json:"unprovable_key"`
	DetachedManifestSHA256    string `json:"detached_manifest_sha256"`
	ReleaseExpectationsSHA256 string `json:"release_expectations_sha256"`
}

func marshalCanonical(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	if len(encoded) == 0 || len(encoded) > MaxEnvelopeBytes {
		return nil, errors.New("canonical envelope size")
	}
	return encoded, nil
}

func (m Manifest) ExpectationsBytes() ([]byte, error) {
	if err := m.validate(m.Architecture); err != nil {
		return nil, err
	}
	return marshalCanonical(expectations{
		ExpectationsFormat: ExpectationsFormat, ReleaseID: m.ReleaseID,
		ContractSHA256: m.ContractSHA256, ClassifierSHA256: m.ClassifierSHA256,
		VerifierSHA256: m.VerifierSHA256, RuleVersion: RuleVersion,
		SourceFormat: m.SourceFormat, SourceLength: m.SourceLength, SourceSHA256: m.SourceSHA256,
		ArtifactFormat: ArtifactFormat, ArtifactLength: m.ArtifactLength,
		ArtifactSHA256: m.ArtifactSHA256, ArtifactHMACVersion: ArtifactHMACVersion,
		ArtifactHMACKeyID: m.ArtifactHMACKeyID, ArtifactHMACSHA256: m.ArtifactHMACSHA256,
		EvidenceHMACKeyID: m.EvidenceHMACKeyID, InputRows: m.InputRows,
		OutputRows: m.OutputRows, Provable: m.Provable, Orphan: m.Orphan,
		CrossTenant: m.CrossTenant, UnprovableKey: m.UnprovableKey,
		DetachedManifestFormat: DetachedFormat, ReceiptFormat: ReceiptFormat,
	})
}

func (m Manifest) DetachedBytes() ([]byte, error) {
	if err := m.validate(m.Architecture); err != nil {
		return nil, err
	}
	return marshalCanonical(detachedManifest{
		ManifestFormat: DetachedFormat, ArtifactHMACVersion: ArtifactHMACVersion,
		ArtifactHMACKeyID: m.ArtifactHMACKeyID, ArtifactFormat: ArtifactFormat,
		SourceFormat: m.SourceFormat, SourceLength: m.SourceLength,
		ArtifactLength: m.ArtifactLength, SourceSHA256: m.SourceSHA256,
		ArtifactSHA256: m.ArtifactSHA256, ArtifactHMACSHA256: m.ArtifactHMACSHA256,
		InputRows: m.InputRows, OutputRows: m.OutputRows, Provable: m.Provable,
		Orphan: m.Orphan, CrossTenant: m.CrossTenant, UnprovableKey: m.UnprovableKey,
	})
}

func (m Manifest) ExpectedReceiptBytes() ([]byte, error) {
	expectationsBytes, err := m.ExpectationsBytes()
	if err != nil {
		return nil, err
	}
	detachedBytes, err := m.DetachedBytes()
	if err != nil {
		return nil, err
	}
	expectationsDigest := sha256.Sum256(expectationsBytes)
	detachedDigest := sha256.Sum256(detachedBytes)
	return marshalCanonical(verifierReceipt{
		ReceiptFormat: ReceiptFormat, Decision: "VERIFIED", ReleaseID: m.ReleaseID,
		ContractSHA256: m.ContractSHA256, ClassifierSHA256: m.ClassifierSHA256,
		VerifierSHA256: m.VerifierSHA256, RuleVersion: RuleVersion,
		SourceFormat: m.SourceFormat, SourceLength: m.SourceLength, SourceSHA256: m.SourceSHA256,
		ArtifactFormat: ArtifactFormat, ArtifactLength: m.ArtifactLength,
		ArtifactSHA256: m.ArtifactSHA256, ArtifactHMACVersion: ArtifactHMACVersion,
		ArtifactHMACKeyID: m.ArtifactHMACKeyID, ArtifactHMACSHA256: m.ArtifactHMACSHA256,
		EvidenceHMACKeyID: m.EvidenceHMACKeyID, InputRows: m.InputRows,
		OutputRows: m.OutputRows, Provable: m.Provable, Orphan: m.Orphan,
		CrossTenant: m.CrossTenant, UnprovableKey: m.UnprovableKey,
		DetachedManifestSHA256:    hex.EncodeToString(detachedDigest[:]),
		ReleaseExpectationsSHA256: hex.EncodeToString(expectationsDigest[:]),
	})
}

func ValidateSeparatedKeys(artifactKey, evidenceKey []byte) error {
	if len(artifactKey) != 32 || len(evidenceKey) != 32 || hmac.Equal(artifactKey, evidenceKey) {
		return errors.New("key material separation")
	}
	return nil
}

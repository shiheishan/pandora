package ca44runner

import (
	"errors"
	"math/bits"
	"regexp"
)

const (
	ReleaseManifestFormat = "pandora-client-auth-00044-artifact-release-manifest-v1"
	SignatureAlgorithm    = "ed25519"
	TrustedStatus         = "TRUSTED"
	ExpectationsFormat    = "pandora-client-auth-00044-artifact-expectations-v1"
	RuleVersion           = "pandora-client-auth-00044-classification-v1"
	ArtifactFormat        = "pandora-device-key-classification-v1"
	ArtifactHMACVersion   = "pandora-client-auth-00044-classification-artifact-hmac-v1"
	DetachedFormat        = "pandora-client-auth-00044-classifier-detached-v1"
	ReceiptFormat         = "pandora-client-auth-00044-artifact-verifier-receipt-v1"
	ContractSHA256        = "78fd468074f8ad528c2e406ae9fc83c8ae1f2da8c36b81eb386e8eeee38595c4"

	MaxManifestBytes = 64 << 10
	MaxEnvelopeBytes = 64 << 10
	MaxStderrBytes   = 4 << 10
	MaxSourceBytes   = 16 << 20
	MaxArtifactBytes = 64 << 20
	MaxRecords       = 100000
)

var (
	releaseIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	keyIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	lowerHex64       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Manifest struct {
	Architecture              string
	RunnerSHA256              string
	ReleaseID                 string
	ContractSHA256            string
	ClassifierSHA256          string
	VerifierSHA256            string
	SourceFormat              string
	SourceLength              uint64
	SourceSHA256              string
	ArtifactLength            uint64
	ArtifactSHA256            string
	ArtifactHMACKeyID         string
	ArtifactHMACSHA256        string
	EvidenceHMACKeyID         string
	InputRows                 uint64
	OutputRows                uint64
	Provable                  uint64
	Orphan                    uint64
	CrossTenant               uint64
	UnprovableKey             uint64
	ReleaseExpectationsSHA256 string
}

func (m Manifest) validate(currentArchitecture string) error {
	if currentArchitecture != "amd64" && currentArchitecture != "arm64" {
		return errors.New("unsupported runtime architecture")
	}
	if m.Architecture != currentArchitecture {
		return errors.New("architecture mismatch")
	}
	if m.ContractSHA256 != ContractSHA256 {
		return errors.New("contract identity mismatch")
	}
	for _, value := range []string{
		m.RunnerSHA256, m.ContractSHA256, m.ClassifierSHA256, m.VerifierSHA256,
		m.SourceSHA256, m.ArtifactSHA256, m.ArtifactHMACSHA256,
		m.ReleaseExpectationsSHA256,
	} {
		if !lowerHex64.MatchString(value) {
			return errors.New("noncanonical hash")
		}
	}
	if !releaseIDPattern.MatchString(m.ReleaseID) {
		return errors.New("invalid release id")
	}
	if !keyIDPattern.MatchString(m.ArtifactHMACKeyID) ||
		!keyIDPattern.MatchString(m.EvidenceHMACKeyID) ||
		m.ArtifactHMACKeyID == m.EvidenceHMACKeyID {
		return errors.New("invalid key ids")
	}
	if m.SourceFormat != "tsv" && m.SourceFormat != "ndjson" {
		return errors.New("invalid source format")
	}
	if m.SourceLength == 0 || m.SourceLength > MaxSourceBytes ||
		m.ArtifactLength == 0 || m.ArtifactLength > MaxArtifactBytes ||
		m.InputRows == 0 || m.InputRows > MaxRecords || m.InputRows != m.OutputRows {
		return errors.New("invalid lengths or row counts")
	}
	count, carry := bits.Add64(m.Provable, m.Orphan, 0)
	if carry != 0 {
		return errors.New("classification count overflow")
	}
	count, carry = bits.Add64(count, m.CrossTenant, 0)
	if carry != 0 {
		return errors.New("classification count overflow")
	}
	count, carry = bits.Add64(count, m.UnprovableKey, 0)
	if carry != 0 || count != m.InputRows {
		return errors.New("classification count mismatch")
	}
	return nil
}

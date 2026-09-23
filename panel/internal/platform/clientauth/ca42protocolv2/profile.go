package ca42protocolv2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42storage"
)

const (
	Format                       = "pandora-ca42-admission-profile-v2"
	ProfileID                    = ca42executionv2.RequiredProfileID
	ReleaseManifestFormat        = "pandora-client-auth-00042-release-manifest-v3"
	ReleaseContractCoreFormat    = "pandora-client-auth-00042-release-contract-core-v2"
	ExecutionPlanFormat          = ca42executionv2.Format
	TrustCapsuleFormat           = "client-auth-00042-trust-capsule-v2"
	AttestationFormat            = "client-auth-attestation-v3"
	ExpectedFormat               = "client-auth-attestation-expected-v2"
	ArtifactStorageFormat        = ca42storage.Format
	ArtifactStorageProfile       = ca42storage.Profile
	ReleaseJournalFormat         = "pandora-release-journal-v3"
	ReleaseJournalManifestFormat = "pandora-release-journal-manifest-v3"
	LedgerNamespace              = ca42executionv2.LedgerNamespace
	ReleaseJournalNamespace      = ProfileID
	ControllerContract           = "pandora-production-release-controller-v2"
)

var fieldValues = [...]struct{ name, value string }{
	{"format", Format},
	{"profile_id", ProfileID},
	{"release_manifest_format", ReleaseManifestFormat},
	{"release_contract_core_format", ReleaseContractCoreFormat},
	{"execution_plan_format", ExecutionPlanFormat},
	{"trust_capsule_format", TrustCapsuleFormat},
	{"attestation_format", AttestationFormat},
	{"expected_format", ExpectedFormat},
	{"artifact_storage_format", ArtifactStorageFormat},
	{"artifact_storage_profile", ArtifactStorageProfile},
	{"release_journal_format", ReleaseJournalFormat},
	{"release_journal_manifest_format", ReleaseJournalManifestFormat},
	{"ledger_namespace", LedgerNamespace},
	{"release_journal_namespace", ReleaseJournalNamespace},
	{"controller_contract", ControllerContract},
}

var profileHashDomain = []byte("PANDORA\x00CA42-ADMISSION-PROFILE\x00V2\x00")

// Profile is an opaque compile-time capability for the only protocol family
// eligible for the future CA42 v2 admission path. A zero or hand-built value
// cannot validate.
type Profile struct {
	canonical []byte
	sha256    [sha256.Size]byte
	valid     bool
}

func Strict() Profile {
	canonical := canonicalProfileBytes()
	digest := domainHash(canonical)
	return Profile{canonical: canonical, sha256: digest, valid: hex.EncodeToString(digest[:]) == ca42executionv2.RequiredProfileSHA256}
}

func (profile Profile) VerifiedCopy() (Profile, error) {
	if !profile.valid || len(profile.canonical) == 0 || profile.sha256 == ([sha256.Size]byte{}) {
		return Profile{}, errors.New("CA42 v2 admission profile capability invalid")
	}
	expected := Strict()
	if !bytes.Equal(profile.canonical, expected.canonical) || profile.sha256 != expected.sha256 {
		return Profile{}, errors.New("CA42 v2 admission profile identity changed")
	}
	return expected, nil
}

func (profile Profile) CanonicalBytes() ([]byte, error) {
	verified, err := profile.VerifiedCopy()
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), verified.canonical...), nil
}

func (profile Profile) SHA256() ([sha256.Size]byte, error) {
	verified, err := profile.VerifiedCopy()
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return verified.sha256, nil
}

func (profile Profile) IsStrict() bool {
	_, err := profile.VerifiedCopy()
	return err == nil
}

func canonicalProfileBytes() []byte {
	var output bytes.Buffer
	for _, field := range fieldValues {
		output.WriteString(field.name)
		output.WriteByte('=')
		output.WriteString(field.value)
		output.WriteByte('\n')
	}
	return output.Bytes()
}

func domainHash(canonical []byte) [sha256.Size]byte {
	hasher := sha256.New()
	_, _ = hasher.Write(profileHashDomain)
	_, _ = hasher.Write(canonical)
	var result [sha256.Size]byte
	copy(result[:], hasher.Sum(nil))
	return result
}

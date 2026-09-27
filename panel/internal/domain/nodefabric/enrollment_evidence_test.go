package nodefabric

import (
	"strings"
	"testing"
)

func validEnrollmentEvidenceForTest() CommitEnrollmentInput {
	return CommitEnrollmentInput{
		AgentVersion:    "pandora-native-test",
		Architecture:    "amd64",
		BinarySHA256:    strings.Repeat("a", 64),
		ConfigSHA256:    strings.Repeat("b", 64),
		UnitSHA256:      strings.Repeat("c", 64),
		PreflightSHA256: strings.Repeat("d", 64),
	}
}

func TestValidateEnrollmentEvidenceRequiresPublishedBindingInProduction(t *testing.T) {
	production := &ReleaseBinding{Production: true}
	if err := validateEnrollmentEvidence(validEnrollmentEvidenceForTest(), production); err == nil {
		t.Fatal("production evidence was accepted without published artifact/version binding")
	}
}

func TestValidateEnrollmentEvidenceFailsClosedWithoutInjectedBinding(t *testing.T) {
	if err := validateEnrollmentEvidence(validEnrollmentEvidenceForTest(), nil); err == nil {
		t.Fatal("evidence was accepted by a service that never received a release binding")
	}
}

func TestValidateEnrollmentEvidenceSkipsComparisonOutsideProduction(t *testing.T) {
	if err := validateEnrollmentEvidence(validEnrollmentEvidenceForTest(), &ReleaseBinding{}); err != nil {
		t.Fatalf("non-production evidence without binding rejected: %v", err)
	}
}

func TestValidateEnrollmentEvidenceBindsPublishedArtifactAndVersion(t *testing.T) {
	evidence := validEnrollmentEvidenceForTest()
	binding := &ReleaseBinding{
		Production:     true,
		ArtifactSHA256: map[string]string{"amd64": evidence.BinarySHA256},
		Version:        evidence.AgentVersion,
	}
	if err := validateEnrollmentEvidence(evidence, binding); err != nil {
		t.Fatalf("matching published evidence rejected: %v", err)
	}

	binding.ArtifactSHA256["amd64"] = strings.Repeat("e", 64)
	if err := validateEnrollmentEvidence(evidence, binding); err == nil {
		t.Fatal("mismatched published artifact was accepted")
	}

	binding.ArtifactSHA256["amd64"] = evidence.BinarySHA256
	binding.Version = "pandora-native-other"
	if err := validateEnrollmentEvidence(evidence, binding); err == nil {
		t.Fatal("mismatched published version was accepted")
	}
}

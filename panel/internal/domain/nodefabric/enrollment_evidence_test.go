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
	t.Setenv("AEGIS_ENV", "production")
	t.Setenv("PANDORA_NATIVE_ARTIFACT_AMD64_SHA256", "")
	t.Setenv("PANDORA_NATIVE_RELEASE_VERSION", "")

	if err := validateEnrollmentEvidence(validEnrollmentEvidenceForTest()); err == nil {
		t.Fatal("production evidence was accepted without published artifact/version binding")
	}
}

func TestValidateEnrollmentEvidenceBindsPublishedArtifactAndVersion(t *testing.T) {
	evidence := validEnrollmentEvidenceForTest()
	t.Setenv("AEGIS_ENV", "production")
	t.Setenv("PANDORA_NATIVE_ARTIFACT_AMD64_SHA256", evidence.BinarySHA256)
	t.Setenv("PANDORA_NATIVE_RELEASE_VERSION", evidence.AgentVersion)
	if err := validateEnrollmentEvidence(evidence); err != nil {
		t.Fatalf("matching published evidence rejected: %v", err)
	}

	t.Setenv("PANDORA_NATIVE_ARTIFACT_AMD64_SHA256", strings.Repeat("e", 64))
	if err := validateEnrollmentEvidence(evidence); err == nil {
		t.Fatal("mismatched published artifact was accepted")
	}
}

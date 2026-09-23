package ca42controlv3

import (
	"strings"
	"testing"
)

func TestFrozenControlLayoutIsVersionedUniqueAndClosed(t *testing.T) {
	if Format != "pandora-ca42-control-layout-v3" || RootPath != "/var/lib/pandora/ca42-control-v3" ||
		DirectoryMode != 0o700 || DataMode != 0o400 || ExecutableMode != 0o500 {
		t.Fatal("CA42 V3 control root contract drifted")
	}
	wantRoles := [...]string{
		"release_manifest_v3", "execution_plan_v2", "trust_capsule_v2", "attestation_core",
		"attestation_v3", "expected_v2", "artifact_storage_descriptor_v2",
	}
	wantNames := [...]string{
		"release-manifest.v3", "execution-plan.v2", "trust-capsule.v2", "attestation-core",
		"attestation.v3", "expected.v2", "artifact-storage-descriptor.v2",
	}
	wantModes := [...]uint32{0o400, 0o400, 0o400, 0o500, 0o400, 0o400, 0o400}
	seenRoles, seenNames := map[string]bool{}, map[string]bool{}
	wantMaximums := [...]uint64{128 << 10, 96 << 10, 96 << 10, 4 << 20, 192 << 10, 128 << 10, 4 << 20}
	for index, entry := range Entries() {
		if entry.Ordinal != index+1 || entry.Role != wantRoles[index] || entry.Name != wantNames[index] ||
			entry.Role == "" || entry.Name == "" || strings.ContainsAny(entry.Name, "/\\\x00") ||
			entry.Mode != wantModes[index] || entry.MaxBytes != wantMaximums[index] ||
			seenRoles[entry.Role] || seenNames[entry.Name] {
			t.Fatalf("invalid frozen entry %d: %+v", index, entry)
		}
		seenRoles[entry.Role], seenNames[entry.Name] = true, true
		resolved, err := EntryForRole(entry.Role)
		if err != nil || resolved != entry {
			t.Fatalf("role lookup drifted: role=%q entry=%+v err=%v", entry.Role, resolved, err)
		}
	}
	for _, legacy := range []string{"release.manifest", "execution.plan", "trust.capsule", "attestation.v2", "attestation.expected", "external.manifest"} {
		for _, entry := range Entries() {
			if entry.Name == legacy {
				t.Fatalf("V3 control layout reused legacy basename %q", legacy)
			}
		}
	}
	if _, err := EntryForRole("unknown"); err == nil {
		t.Fatal("unknown control role was accepted")
	}
	copy := Entries()
	copy[0] = Entry{}
	if Entries()[0].Role != "release_manifest_v3" {
		t.Fatal("Entries leaked shared mutable state")
	}
}

func TestControlAttemptIDGrammar(t *testing.T) {
	for _, valid := range []string{"attempt-1", "a", "release_42.arm64"} {
		if err := ValidateAttemptID(valid); err != nil {
			t.Fatalf("valid attempt rejected: %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "UPPER", "../escape", "/absolute", "a\\b", strings.Repeat("a", 129)} {
		if err := ValidateAttemptID(invalid); err == nil {
			t.Fatalf("invalid attempt accepted: %q", invalid)
		}
	}
}

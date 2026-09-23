package ca44runner

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

func layoutManifest() Manifest {
	return Manifest{
		SourceFormat:      "ndjson",
		ArtifactHMACKeyID: "artifact-key-20260731",
		EvidenceHMACKeyID: "evidence-key-20260731",
	}
}

func TestBuildClassifierArgsExactAndIndependent(t *testing.T) {
	manifest := layoutManifest()
	want := []string{
		"-format", "ndjson",
		"-artifact-dir-fd", "4",
		"-artifact-name", "devices.classified.json",
		"-artifact-hmac-key-id", "artifact-key-20260731",
		"-artifact-hmac-key-fd", "3",
	}
	first := BuildClassifierArgs(manifest)
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("classifier argv mismatch:\n got: %#v\nwant: %#v", first, want)
	}
	first[0] = "mutated"
	if second := BuildClassifierArgs(manifest); !reflect.DeepEqual(second, want) {
		t.Fatalf("classifier argv reused mutable storage: %#v", second)
	}
}

func TestBuildVerifierArgsExactAndIndependent(t *testing.T) {
	manifest := layoutManifest()
	want := []string{
		"-source-format", "ndjson",
		"-source-fd", "30",
		"-artifact-fd", "31",
		"-detached-manifest-fd", "32",
		"-release-expectations-fd", "33",
		"-artifact-hmac-key-id", "artifact-key-20260731",
		"-artifact-hmac-key-fd", "34",
		"-evidence-hmac-key-id", "evidence-key-20260731",
		"-evidence-hmac-key-fd", "35",
	}
	first := BuildVerifierArgs(manifest)
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("verifier argv mismatch:\n got: %#v\nwant: %#v", first, want)
	}
	first[0] = "mutated"
	if second := BuildVerifierArgs(manifest); !reflect.DeepEqual(second, want) {
		t.Fatalf("verifier argv reused mutable storage: %#v", second)
	}
}

func TestClassifierFileLayoutExactNoAliases(t *testing.T) {
	want := []FileSlot{
		{FD: 0, Role: FileRoleSource},
		{FD: 1, Role: FileRoleCapture},
		{FD: 2, Role: FileRoleStderr},
		{FD: 3, Role: FileRoleArtifactKey},
		{FD: 4, Role: FileRoleCandidateDir},
		{FD: 5, Role: FileRoleExecutable},
	}
	first := BuildClassifierFiles()
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("classifier files mismatch:\n got: %#v\nwant: %#v", first, want)
	}
	assertUniqueAssignedFDs(t, first)
	first[0].Role = FileRoleClosed
	if second := BuildClassifierFiles(); !reflect.DeepEqual(second, want) {
		t.Fatalf("classifier layout reused mutable storage: %#v", second)
	}
}

func TestVerifierFileLayoutExactPaddingNoAliases(t *testing.T) {
	slots := BuildVerifierFiles()
	if len(slots) != VerifierExecutableFD+1 {
		t.Fatalf("verifier slot count=%d want=%d", len(slots), VerifierExecutableFD+1)
	}
	wantAssigned := map[int]FileRole{
		VerifierCaptureFD: FileRoleCapture, VerifierStderrFD: FileRoleStderr,
		VerifierSourceFD: FileRoleSource, VerifierArtifactFD: FileRoleArtifact,
		VerifierDetachedFD: FileRoleDetached, VerifierExpectationsFD: FileRoleExpectations,
		VerifierArtifactKeyFD: FileRoleArtifactKey, VerifierEvidenceKeyFD: FileRoleEvidenceKey,
		VerifierExecutableFD: FileRoleExecutable,
	}
	for fd, slot := range slots {
		if slot.FD != fd {
			t.Fatalf("slot index alias at %d: %#v", fd, slot)
		}
		wantRole := FileRoleClosed
		if assigned, ok := wantAssigned[fd]; ok {
			wantRole = assigned
		}
		if slot.Role != wantRole {
			t.Fatalf("fd %d role=%q want=%q", fd, slot.Role, wantRole)
		}
	}
	for fd := VerifierPaddingStartFD; fd <= VerifierPaddingEndFD; fd++ {
		if slots[fd].Role != FileRoleClosed {
			t.Fatalf("verifier padding fd %d is not closed", fd)
		}
	}
	assertUniqueAssignedFDs(t, slots)
	slots[VerifierSourceFD].Role = FileRoleClosed
	if second := BuildVerifierFiles(); second[VerifierSourceFD].Role != FileRoleSource {
		t.Fatal("verifier layout reused mutable storage")
	}
}

func assertUniqueAssignedFDs(t *testing.T, slots []FileSlot) {
	t.Helper()
	seenFD := make(map[int]struct{}, len(slots))
	seenRole := make(map[FileRole]struct{}, len(slots))
	for _, slot := range slots {
		if _, exists := seenFD[slot.FD]; exists {
			t.Fatalf("duplicate fd %d", slot.FD)
		}
		seenFD[slot.FD] = struct{}{}
		if slot.Role == FileRoleClosed {
			continue
		}
		if _, exists := seenRole[slot.Role]; exists {
			t.Fatalf("duplicate assigned role %q", slot.Role)
		}
		seenRole[slot.Role] = struct{}{}
	}
}

func TestCleanEnvironmentExactSecretFreeAndIndependent(t *testing.T) {
	t.Setenv("PANDORA_ARTIFACT_KEY", "must-not-leak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "must-not-leak")
	t.Setenv("HOME", "must-not-leak")
	want := []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C",
		"LC_ALL=C",
		"TZ=UTC",
	}
	first := CleanEnvironment()
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("clean environment mismatch:\n got: %#v\nwant: %#v", first, want)
	}
	joined := strings.ToLower(strings.Join(first, "\n"))
	for _, forbidden := range []string{"secret", "password", "token", "private", "artifact_key", "evidence_key", os.Getenv("PANDORA_ARTIFACT_KEY")} {
		if forbidden != "" && strings.Contains(joined, strings.ToLower(forbidden)) {
			t.Fatalf("clean environment leaked forbidden value or name %q", forbidden)
		}
	}
	first[0] = "PATH=mutated"
	if second := CleanEnvironment(); !reflect.DeepEqual(second, want) {
		t.Fatalf("clean environment reused mutable storage: %#v", second)
	}
}

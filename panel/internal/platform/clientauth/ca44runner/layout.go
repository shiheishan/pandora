package ca44runner

import "strconv"

const (
	ClassifierSourceFD       = 0
	ClassifierCaptureFD      = 1
	ClassifierStderrFD       = 2
	ClassifierArtifactKeyFD  = 3
	ClassifierCandidateDirFD = 4
	ClassifierExecutableFD   = 5

	VerifierStdinFD        = 0
	VerifierCaptureFD      = 1
	VerifierStderrFD       = 2
	VerifierPaddingStartFD = 3
	VerifierPaddingEndFD   = 29
	VerifierSourceFD       = 30
	VerifierArtifactFD     = 31
	VerifierDetachedFD     = 32
	VerifierExpectationsFD = 33
	VerifierArtifactKeyFD  = 34
	VerifierEvidenceKeyFD  = 35
	VerifierExecutableFD   = 36
	ClassifiedArtifactName = "devices.classified.json"
)

type FileRole string

const (
	FileRoleClosed       FileRole = "closed"
	FileRoleSource       FileRole = "source"
	FileRoleCapture      FileRole = "capture"
	FileRoleStderr       FileRole = "stderr"
	FileRoleArtifact     FileRole = "artifact"
	FileRoleDetached     FileRole = "detached"
	FileRoleExpectations FileRole = "expectations"
	FileRoleArtifactKey  FileRole = "artifact_key"
	FileRoleEvidenceKey  FileRole = "evidence_key"
	FileRoleCandidateDir FileRole = "candidate_dir"
	FileRoleExecutable   FileRole = "executable"
)

// FileSlot is a pure child-FD layout description. The Linux process launcher
// resolves each role to a retained *os.File and maps FileRoleClosed to nil.
type FileSlot struct {
	FD   int
	Role FileRole
}

func BuildClassifierArgs(manifest Manifest) []string {
	return []string{
		"-format", manifest.SourceFormat,
		"-artifact-dir-fd", strconv.Itoa(ClassifierCandidateDirFD),
		"-artifact-name", ClassifiedArtifactName,
		"-artifact-hmac-key-id", manifest.ArtifactHMACKeyID,
		"-artifact-hmac-key-fd", strconv.Itoa(ClassifierArtifactKeyFD),
	}
}

func BuildVerifierArgs(manifest Manifest) []string {
	return []string{
		"-source-format", manifest.SourceFormat,
		"-source-fd", strconv.Itoa(VerifierSourceFD),
		"-artifact-fd", strconv.Itoa(VerifierArtifactFD),
		"-detached-manifest-fd", strconv.Itoa(VerifierDetachedFD),
		"-release-expectations-fd", strconv.Itoa(VerifierExpectationsFD),
		"-artifact-hmac-key-id", manifest.ArtifactHMACKeyID,
		"-artifact-hmac-key-fd", strconv.Itoa(VerifierArtifactKeyFD),
		"-evidence-hmac-key-id", manifest.EvidenceHMACKeyID,
		"-evidence-hmac-key-fd", strconv.Itoa(VerifierEvidenceKeyFD),
	}
}

func BuildClassifierFiles() []FileSlot {
	return []FileSlot{
		{FD: ClassifierSourceFD, Role: FileRoleSource},
		{FD: ClassifierCaptureFD, Role: FileRoleCapture},
		{FD: ClassifierStderrFD, Role: FileRoleStderr},
		{FD: ClassifierArtifactKeyFD, Role: FileRoleArtifactKey},
		{FD: ClassifierCandidateDirFD, Role: FileRoleCandidateDir},
		{FD: ClassifierExecutableFD, Role: FileRoleExecutable},
	}
}

func BuildVerifierFiles() []FileSlot {
	slots := make([]FileSlot, VerifierExecutableFD+1)
	for fd := range slots {
		slots[fd] = FileSlot{FD: fd, Role: FileRoleClosed}
	}
	slots[VerifierCaptureFD].Role = FileRoleCapture
	slots[VerifierStderrFD].Role = FileRoleStderr
	slots[VerifierSourceFD].Role = FileRoleSource
	slots[VerifierArtifactFD].Role = FileRoleArtifact
	slots[VerifierDetachedFD].Role = FileRoleDetached
	slots[VerifierExpectationsFD].Role = FileRoleExpectations
	slots[VerifierArtifactKeyFD].Role = FileRoleArtifactKey
	slots[VerifierEvidenceKeyFD].Role = FileRoleEvidenceKey
	slots[VerifierExecutableFD].Role = FileRoleExecutable
	return slots
}

func CleanEnvironment() []string {
	return []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		"LANG=C",
		"LC_ALL=C",
		"TZ=UTC",
	}
}

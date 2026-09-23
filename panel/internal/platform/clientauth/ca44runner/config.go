package ca44runner

import (
	"errors"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MinChildTimeout = time.Second
	MaxChildTimeout = 15 * time.Minute
	MinWaitDelay    = time.Millisecond
	MaxWaitDelay    = time.Second

	maxConfigPathBytes  = 4096
	maxPathSegmentBytes = 255
)

// Config contains only public identities, paths, and bounded timing policy.
// Key material is inherited through retained file descriptors and must never be
// added to this structure, argv, or the child environment.
type Config struct {
	ExpectedManifestSHA256  string
	ApprovedSignerKeySHA256 string
	ArtifactKeySHA256       string
	EvidenceKeySHA256       string
	TrustRoot               string
	ManifestPath            string
	SignerPublicKeyPath     string
	ContractPath            string
	ClassifierPath          string
	VerifierPath            string
	SourcePath              string
	ArtifactKeyPath         string
	EvidenceKeyPath         string
	StagingRoot             string
	PublishRoot             string
	ChildTimeout            time.Duration
	WaitDelay               time.Duration
}

// Validate performs platform-independent grammar checks. Linux ownership,
// mode, mount, symlink, and EUID checks remain the Linux runner's responsibility
// and must be performed on retained descriptors rather than path strings.
func (c Config) Validate(runtimeGOOS, runtimeGOARCH string) error {
	if runtimeGOOS != "linux" {
		return errors.New("unsupported runtime operating system")
	}
	if runtimeGOARCH != "amd64" && runtimeGOARCH != "arm64" {
		return errors.New("unsupported runtime architecture")
	}

	for _, pin := range []string{
		c.ExpectedManifestSHA256,
		c.ApprovedSignerKeySHA256,
		c.ArtifactKeySHA256,
		c.EvidenceKeySHA256,
	} {
		if !lowerHex64.MatchString(pin) {
			return errors.New("noncanonical identity pin")
		}
	}
	if c.ArtifactKeySHA256 == c.EvidenceKeySHA256 {
		return errors.New("key identity pins must differ")
	}

	if err := validateTrustRoot(c.TrustRoot); err != nil {
		return err
	}
	if c.ArtifactKeyPath == c.EvidenceKeyPath {
		return errors.New("key paths must differ")
	}
	protectedObjects := []string{
		c.ManifestPath,
		c.SignerPublicKeyPath,
		c.ContractPath,
		c.ClassifierPath,
		c.VerifierPath,
		c.SourcePath,
		c.ArtifactKeyPath,
		c.EvidenceKeyPath,
	}
	objects := append([]string(nil), protectedObjects...)
	objects = append(objects,
		c.StagingRoot,
		c.PublishRoot,
	)
	seen := make(map[string]struct{}, len(objects))
	for _, objectPath := range objects {
		if err := validateRelativeObjectPath(objectPath); err != nil {
			return err
		}
		if _, exists := seen[objectPath]; exists {
			return errors.New("object path alias")
		}
		seen[objectPath] = struct{}{}
	}
	if pathsOverlap(c.StagingRoot, c.PublishRoot) {
		return errors.New("staging and publish roots overlap")
	}
	for _, mutableRoot := range []string{c.StagingRoot, c.PublishRoot} {
		for _, protectedObject := range protectedObjects {
			if pathsOverlap(mutableRoot, protectedObject) {
				return errors.New("mutable root overlaps protected object")
			}
		}
	}

	if c.ChildTimeout < MinChildTimeout || c.ChildTimeout > MaxChildTimeout {
		return errors.New("child timeout out of bounds")
	}
	if c.WaitDelay < MinWaitDelay || c.WaitDelay > MaxWaitDelay || c.WaitDelay >= c.ChildTimeout {
		return errors.New("wait delay out of bounds")
	}
	return nil
}

func validateTrustRoot(value string) error {
	if !validPathBytes(value) || !path.IsAbs(value) || value == "/" || path.Clean(value) != value {
		return errors.New("invalid trust root")
	}
	return nil
}

func validateRelativeObjectPath(value string) error {
	if !validPathBytes(value) || path.IsAbs(value) || value == "." || path.Clean(value) != value {
		return errors.New("invalid relative object path")
	}
	return nil
}

func validPathBytes(value string) bool {
	if value == "" || len(value) > maxConfigPathBytes || !utf8.ValidString(value) ||
		strings.ContainsAny(value, "\\:") {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	for _, segment := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." || len(segment) > maxPathSegmentBytes {
			return false
		}
	}
	return true
}

func pathsOverlap(first, second string) bool {
	return first == second || strings.HasPrefix(first, second+"/") || strings.HasPrefix(second, first+"/")
}

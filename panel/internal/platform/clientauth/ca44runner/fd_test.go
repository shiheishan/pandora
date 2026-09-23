package ca44runner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestValidateTrustedFileSpecAcceptsCanonicalPolicy(t *testing.T) {
	digest := sha256.Sum256([]byte("trusted"))
	want := hex.EncodeToString(digest[:])
	got, err := validateTrustedFileSpec(TrustedFileSpec{
		RelativePath:   "bin/classifier",
		ExpectedSHA256: want,
		MaxBytes:       64 << 20,
		ExactMode:      0o500,
		ExpectedUID:    0,
	})
	if err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
	if got != digest {
		t.Fatalf("decoded digest = %x, want %x", got, digest)
	}
}

func TestValidateTrustedFileSpecRejectsNonCanonicalInputs(t *testing.T) {
	validHash := strings.Repeat("a", sha256.Size*2)
	tests := []struct {
		name string
		spec TrustedFileSpec
	}{
		{name: "empty path", spec: TrustedFileSpec{ExpectedSHA256: validHash, MaxBytes: 1, ExactMode: 0o400}},
		{name: "absolute", spec: TrustedFileSpec{RelativePath: "/bin/tool", ExpectedSHA256: validHash, MaxBytes: 1, ExactMode: 0o400}},
		{name: "dot", spec: TrustedFileSpec{RelativePath: "bin/./tool", ExpectedSHA256: validHash, MaxBytes: 1, ExactMode: 0o400}},
		{name: "dotdot", spec: TrustedFileSpec{RelativePath: "bin/../tool", ExpectedSHA256: validHash, MaxBytes: 1, ExactMode: 0o400}},
		{name: "empty component", spec: TrustedFileSpec{RelativePath: "bin//tool", ExpectedSHA256: validHash, MaxBytes: 1, ExactMode: 0o400}},
		{name: "trailing slash", spec: TrustedFileSpec{RelativePath: "bin/tool/", ExpectedSHA256: validHash, MaxBytes: 1, ExactMode: 0o400}},
		{name: "backslash", spec: TrustedFileSpec{RelativePath: `bin\tool`, ExpectedSHA256: validHash, MaxBytes: 1, ExactMode: 0o400}},
		{name: "nul", spec: TrustedFileSpec{RelativePath: "bin/\x00tool", ExpectedSHA256: validHash, MaxBytes: 1, ExactMode: 0o400}},
		{name: "zero limit", spec: TrustedFileSpec{RelativePath: "bin/tool", ExpectedSHA256: validHash, ExactMode: 0o400}},
		{name: "negative limit", spec: TrustedFileSpec{RelativePath: "bin/tool", ExpectedSHA256: validHash, MaxBytes: -1, ExactMode: 0o400}},
		{name: "special mode", spec: TrustedFileSpec{RelativePath: "bin/tool", ExpectedSHA256: validHash, MaxBytes: 1, ExactMode: 0o1400}},
		{name: "uppercase hash", spec: TrustedFileSpec{RelativePath: "bin/tool", ExpectedSHA256: strings.Repeat("A", sha256.Size*2), MaxBytes: 1, ExactMode: 0o400}},
		{name: "short hash", spec: TrustedFileSpec{RelativePath: "bin/tool", ExpectedSHA256: validHash[:len(validHash)-1], MaxBytes: 1, ExactMode: 0o400}},
		{name: "non hex hash", spec: TrustedFileSpec{RelativePath: "bin/tool", ExpectedSHA256: strings.Repeat("g", sha256.Size*2), MaxBytes: 1, ExactMode: 0o400}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateTrustedFileSpec(test.spec); err == nil {
				t.Fatal("invalid spec accepted")
			}
		})
	}
}

func TestUnsupportedPlatformFailsClosed(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("native Linux openat2 behavior is outside this cross-platform unit gate")
	}
	digest := strings.Repeat("a", sha256.Size*2)
	file, identity, err := OpenTrustedRegularAt(-1, "bin/tool", digest, 1, 0o400, 0)
	if file != nil || !errors.Is(err, ErrTrustedFilesUnsupported) {
		t.Fatalf("OpenTrustedRegularAt = (%v, %v), want nil unsupported", file, err)
	}
	if err := VerifySameFile((*os.File)(nil), identity, 0); !errors.Is(err, ErrTrustedFilesUnsupported) {
		t.Fatalf("VerifySameFile error = %v, want unsupported", err)
	}
}

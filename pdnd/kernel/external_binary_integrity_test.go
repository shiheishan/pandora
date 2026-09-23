package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
	"testing"
)

// requireExternalBinarySHA256 binds opt-in black-box interoperability tests
// to an explicitly reviewed executable.  Merely finding a file at an
// operator-supplied path is not provenance evidence.
func requireExternalBinarySHA256(t *testing.T, path, digestEnv string) {
	t.Helper()
	want := strings.ToLower(strings.TrimSpace(os.Getenv(digestEnv)))
	decoded, err := hex.DecodeString(want)
	if err != nil || len(decoded) != sha256.Size {
		t.Fatalf("%s must be exactly 64 hexadecimal SHA-256 characters", digestEnv)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open external binary: %v", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatalf("hash external binary: %v", err)
	}
	got := hash.Sum(nil)
	if !equalBytes(got, decoded) {
		t.Fatalf("external binary SHA-256 mismatch: got %x want %s", got, want)
	}
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var different byte
	for i := range left {
		different |= left[i] ^ right[i]
	}
	return different == 0
}

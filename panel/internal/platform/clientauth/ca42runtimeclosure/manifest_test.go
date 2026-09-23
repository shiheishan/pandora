package ca42runtimeclosure

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestParseExactRuntimeClosure(t *testing.T) {
	data, digest := runtimeClosureFixture(t)
	manifest, err := Parse(data, digest, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.parsed || manifest.DockerClientBuildInfoSHA256 != "none" || manifest.GooseBinarySHA256 == "" {
		t.Fatalf("unexpected runtime closure: %+v", manifest)
	}
	mutated := manifest
	mutated.GooseBinarySHA256 = strings.Repeat("f", 64)
	trusted, err := mutated.VerifiedCopy()
	if err != nil || trusted.GooseBinarySHA256 == mutated.GooseBinarySHA256 {
		t.Fatal("mutable runtime closure projection influenced canonical verification")
	}
}

func TestParseRejectsRuntimeClosureDrift(t *testing.T) {
	base, _ := runtimeClosureFixture(t)
	tests := map[string]func([]byte) []byte{
		"v2_format": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), Format, "pandora-ca42-go-runtime-closure-v2", 1))
		},
		"missing_role": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "entry_count=7", "entry_count=6", 1))
		},
		"wrong_arch": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "architecture=amd64", "architecture=arm64", 1))
		},
		"duplicate_role_hash": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "pathtrust_binary_sha256="+hashFor(3), "pathtrust_binary_sha256="+hashFor(1), 1))
		},
		"zero_hash": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), hashFor(4), strings.Repeat("0", 64), 1))
		},
		"docker_unverified_value": func(value []byte) []byte {
			return []byte(strings.Replace(string(value), "docker_client_build_info_sha256=none", "docker_client_build_info_sha256=unknown", 1))
		},
		"crlf":  func(value []byte) []byte { return []byte(strings.ReplaceAll(string(value), "\n", "\r\n")) },
		"extra": func(value []byte) []byte { return append(value, []byte("unknown=x\n")...) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := mutate(append([]byte(nil), base...))
			if _, err := Parse(candidate, sha256.Sum256(candidate), "amd64"); err == nil {
				t.Fatal("invalid runtime closure accepted")
			}
		})
	}
}

func runtimeClosureFixture(t *testing.T) ([]byte, [sha256.Size]byte) {
	t.Helper()
	values := []string{Format, "release-1", "run-1", "attempt-1", "amd64", EntryCount}
	for index := 1; index <= len(fieldNames)-7; index++ {
		values = append(values, hashFor(index))
	}
	values = append(values, "none")
	if len(values) != len(fieldNames) {
		t.Fatalf("fixture width %d != %d", len(values), len(fieldNames))
	}
	data, err := CanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	return data, sha256.Sum256(data)
}

func hashFor(index int) string { return fmt.Sprintf("%064x", index) }

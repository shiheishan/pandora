package ca42gooseinfo

import (
	"crypto/sha256"
	"strings"
	"testing"
)

const fixtureModuleSum = RequiredMainModuleSum

func TestParseFrozenStaticGooseBuildInfo(t *testing.T) {
	data, digest := validBuildInfo(t)
	parsed, err := Parse(data, digest, strings.Repeat("a", 64), "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if !parsed.IsParsed() || parsed.Architecture != "amd64" || parsed.DependencyCount != 31 || parsed.BuildSettingCount != 8 {
		t.Fatalf("unexpected projection: %+v", parsed)
	}
	mutated := parsed
	mutated.BinarySHA256 = strings.Repeat("f", 64)
	trusted, err := mutated.VerifiedCopy()
	if err != nil || trusted.BinarySHA256 != strings.Repeat("a", 64) {
		t.Fatal("mutable build-info projection influenced canonical verification")
	}
}

func TestParseRejectsBuildIdentityDrift(t *testing.T) {
	base, _ := validBuildInfo(t)
	tests := map[string][2]string{
		"wrong_goose":           {"main_module_version=v3.24.1", "main_module_version=v3.25.0"},
		"devel":                 {"main_module_version=v3.24.1", "main_module_version=(devel)"},
		"wrong_command":         {"command_path=" + RequiredCommandPath, "command_path=github.com/example/goose"},
		"wrong_module":          {"main_module_path=" + RequiredMainModulePath, "main_module_path=github.com/example/goose/v3"},
		"missing_sum":           {"main_module_sum=" + fixtureModuleSum, "main_module_sum=none"},
		"replace":               {"main_replace=none", "main_replace=local"},
		"dirty":                 {"vcs_modified=false", "vcs_modified=true"},
		"cgo":                   {"cgo_enabled=false", "cgo_enabled=true"},
		"dynamic_interpreter":   {"pt_interp=absent", "pt_interp=/lib64/ld-linux-x86-64.so.2"},
		"dynamic_dependency":    {"dt_needed_count=0", "dt_needed_count=1"},
		"rpath":                 {"rpath=absent", "rpath=/tmp"},
		"wrong_arch":            {"architecture=amd64", "architecture=arm64"},
		"wrong_machine":         {"elf_machine=EM_X86_64", "elf_machine=EM_AARCH64"},
		"noncanonical_vcs_time": {"vcs_time=" + RequiredVCSTime, "vcs_time=2025-01-07T14:23:49+00:00"},
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			candidate := []byte(strings.Replace(string(base), change[0], change[1], 1))
			digest := sha256.Sum256(candidate)
			if _, err := Parse(candidate, digest, strings.Repeat("a", 64), "amd64"); err == nil {
				t.Fatal("drifted build info accepted")
			}
		})
	}
}

func TestParseRejectsUnapprovedModuleSum(t *testing.T) {
	data, _ := validBuildInfo(t)
	candidate := []byte(strings.Replace(string(data), RequiredMainModuleSum, "h1:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB=", 1))
	digest := sha256.Sum256(candidate)
	if _, err := Parse(candidate, digest, strings.Repeat("a", 64), "amd64"); err == nil {
		t.Fatal("unapproved module sum accepted")
	}
}

func validBuildInfo(t *testing.T) ([]byte, [sha256.Size]byte) {
	t.Helper()
	values := []string{
		Format, "goose", strings.Repeat("a", 64), "12345678", "amd64", RequiredGoVersion,
		RequiredCommandPath, RequiredMainModulePath, RequiredGooseVersion, fixtureModuleSum, "none", "31",
		strings.Repeat("b", 64), "8", strings.Repeat("d", 64), "false", "exe", "true", "git",
		RequiredVCSRevision, RequiredVCSTime, "false", "ELFCLASS64", "ELFDATA2LSB", "ELFOSABI_NONE",
		"ET_EXEC", "EM_X86_64", "absent", "0", "absent", "absent",
	}
	data, err := CanonicalBytes(values)
	if err != nil {
		t.Fatal(err)
	}
	return data, sha256.Sum256(data)
}

package ca42gooseinfo

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
)

const (
	Format                 = "pandora-ca42-go-build-info-v2"
	RequiredCommandPath    = "github.com/pressly/goose/v3/cmd/goose"
	RequiredMainModulePath = "github.com/pressly/goose/v3"
	RequiredGooseVersion   = "v3.24.1"
	// RequiredMainModuleSum is pinned from sum.golang.org for the exact
	// RequiredMainModulePath@RequiredGooseVersion module zip. It is part of
	// the production trust policy and must never come from build-info input.
	RequiredMainModuleSum = "h1:bZmxRco2uy5uu5Ng1MMVEfYsFlrMJI+e/VMXHQ3C4LY="
	RequiredVCSRevision   = "dc90c17981fd7517ac960ab11a27f890a72a6171"
	RequiredVCSTime       = "2025-01-07T14:23:49Z"
	RequiredGoVersion     = "go1.26.5"
	MaxBuildInfoBytes     = 16 << 10
	MaxBinaryBytes        = uint64(256 << 20)
	MaxDependencies       = uint64(512)
	MaxBuildSettings      = uint64(64)
)

var (
	hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	h1sum = regexp.MustCompile(`^h1:[A-Za-z0-9+/]{43}=$`)
)

var fieldNames = [...]string{
	"format", "role", "binary_sha256", "binary_size_bytes", "architecture", "go_version",
	"command_path", "main_module_path", "main_module_version", "main_module_sum", "main_replace",
	"dependency_count", "dependency_set_sha256", "build_setting_count", "build_setting_set_sha256",
	"cgo_enabled", "build_mode", "trimpath", "vcs", "vcs_revision", "vcs_time", "vcs_modified",
	"elf_class", "elf_data", "elf_osabi", "elf_type", "elf_machine", "pt_interp", "dt_needed_count", "rpath", "runpath",
}

type BuildInfo struct {
	BinarySHA256, Architecture, MainModuleSum           string
	DependencySetSHA256, BuildSettingSetSHA256          string
	BinarySizeBytes, DependencyCount, BuildSettingCount uint64
	VCSRevision                                         string
	VCSTime                                             time.Time
	SHA256                                              [sha256.Size]byte
	parsed                                              bool
	canonical                                           []byte
	expectedBinarySHA256                                string
	parseArchitecture                                   string
}

// Parse validates the canonical projection derived from the same retained
// Goose FD. The module sum is an internal offline lock rather than caller
// input; this parser authorizes nothing by itself.
func Parse(data []byte, expectedSHA256 [sha256.Size]byte, expectedBinarySHA256, architecture string) (BuildInfo, error) {
	var empty BuildInfo
	if len(data) == 0 || len(data) > MaxBuildInfoBytes || data[len(data)-1] != '\n' || bytes.HasSuffix(data, []byte("\n\n")) ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 || bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return empty, errors.New("Goose build info envelope invalid")
	}
	digest := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(digest[:], expectedSHA256[:]) != 1 {
		return empty, errors.New("Goose build info identity mismatch")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) != len(fieldNames) {
		return empty, errors.New("Goose build info field count invalid")
	}
	values := make(map[string]string, len(fieldNames))
	for index, name := range fieldNames {
		prefix := []byte(name + "=")
		if !bytes.HasPrefix(lines[index], prefix) || len(lines[index]) == len(prefix) {
			return empty, fmt.Errorf("Goose build info field order invalid: %s", name)
		}
		values[name] = string(lines[index][len(prefix):])
	}
	machine := map[string]string{"amd64": "EM_X86_64", "arm64": "EM_AARCH64"}[architecture]
	if machine == "" || values["format"] != Format || values["role"] != "goose" || values["architecture"] != architecture ||
		values["go_version"] != RequiredGoVersion || values["command_path"] != RequiredCommandPath ||
		values["main_module_path"] != RequiredMainModulePath || values["main_module_version"] != RequiredGooseVersion ||
		values["main_replace"] != "none" || values["cgo_enabled"] != "false" || values["build_mode"] != "exe" ||
		values["trimpath"] != "true" || values["vcs"] != "git" || values["vcs_modified"] != "false" ||
		values["elf_class"] != "ELFCLASS64" || values["elf_data"] != "ELFDATA2LSB" || values["elf_osabi"] != "ELFOSABI_NONE" ||
		values["elf_type"] != "ET_EXEC" || values["elf_machine"] != machine || values["pt_interp"] != "absent" ||
		values["dt_needed_count"] != "0" || values["rpath"] != "absent" || values["runpath"] != "absent" {
		return empty, errors.New("Goose build info fixed field mismatch")
	}
	if !h1sum.MatchString(RequiredMainModuleSum) || values["main_module_sum"] != RequiredMainModuleSum ||
		!nonZeroHex64(values["binary_sha256"]) || values["binary_sha256"] != expectedBinarySHA256 ||
		!nonZeroHex64(values["dependency_set_sha256"]) || !nonZeroHex64(values["build_setting_set_sha256"]) ||
		values["vcs_revision"] != RequiredVCSRevision || values["vcs_time"] != RequiredVCSTime {
		return empty, errors.New("Goose build info identity field invalid")
	}
	binarySize, err := positiveUint(values["binary_size_bytes"], MaxBinaryBytes)
	if err != nil {
		return empty, errors.New("Goose build info binary size invalid")
	}
	dependencyCount, err := positiveUint(values["dependency_count"], MaxDependencies)
	if err != nil {
		return empty, errors.New("Goose build info dependency count invalid")
	}
	settingCount, err := positiveUint(values["build_setting_count"], MaxBuildSettings)
	if err != nil {
		return empty, errors.New("Goose build info setting count invalid")
	}
	vcsTime, err := time.Parse(time.RFC3339, values["vcs_time"])
	if err != nil || vcsTime.Location() != time.UTC || vcsTime.Format(time.RFC3339) != values["vcs_time"] {
		return empty, errors.New("Goose build info VCS time invalid")
	}
	return BuildInfo{
		BinarySHA256: values["binary_sha256"], BinarySizeBytes: binarySize, Architecture: architecture,
		MainModuleSum: values["main_module_sum"], DependencyCount: dependencyCount,
		DependencySetSHA256: values["dependency_set_sha256"], BuildSettingCount: settingCount,
		BuildSettingSetSHA256: values["build_setting_set_sha256"], VCSRevision: values["vcs_revision"],
		VCSTime: vcsTime.UTC(), SHA256: digest, parsed: true, canonical: append([]byte(nil), data...),
		expectedBinarySHA256: expectedBinarySHA256, parseArchitecture: architecture,
	}, nil
}

func CanonicalBytes(values []string) ([]byte, error) {
	if len(values) != len(fieldNames) {
		return nil, errors.New("Goose build info field count invalid")
	}
	var output strings.Builder
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("Goose build info value invalid")
		}
		fmt.Fprintf(&output, "%s=%s\n", fieldNames[index], value)
	}
	return []byte(output.String()), nil
}

func (info BuildInfo) VerifiedCopy() (BuildInfo, error) {
	if !info.parsed || len(info.canonical) == 0 || info.expectedBinarySHA256 == "" || info.parseArchitecture == "" {
		return BuildInfo{}, errors.New("Goose build info parsed capability invalid")
	}
	digest := sha256.Sum256(info.canonical)
	if digest != info.SHA256 {
		return BuildInfo{}, errors.New("Goose build info projection identity changed")
	}
	return Parse(info.canonical, digest, info.expectedBinarySHA256, info.parseArchitecture)
}

func (info BuildInfo) IsParsed() bool {
	_, err := info.VerifiedCopy()
	return err == nil
}

// BindPlan cross-binds independently parsed build-info bytes to the v2 plan.
// It is structural evidence only and does not execute or authorize Goose.
func BindPlan(info BuildInfo, plan ca42executionv2.Plan, now time.Time) error {
	trustedInfo, err := info.VerifiedCopy()
	if err != nil {
		return err
	}
	trustedPlan, err := plan.VerifiedCopyAt(now)
	if err != nil {
		return err
	}
	info, plan = trustedInfo, trustedPlan
	if plan.GooseBuildInfoSHA256 != fmt.Sprintf("%x", info.SHA256) || plan.GooseBinarySHA256 != info.BinarySHA256 ||
		plan.GooseVersion != RequiredGooseVersion || plan.Architecture != info.Architecture {
		return errors.New("Goose build info execution plan binding mismatch")
	}
	return nil
}

func nonZeroHex64(value string) bool {
	return hex64.MatchString(value) && value != strings.Repeat("0", 64)
}

func positiveUint(value string, maximum uint64) (uint64, error) {
	if value == "" || value[0] == '0' {
		return 0, errors.New("positive integer invalid")
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 || parsed > maximum || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("positive integer invalid")
	}
	return parsed, nil
}

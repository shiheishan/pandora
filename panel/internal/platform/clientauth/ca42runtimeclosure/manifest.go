package ca42runtimeclosure

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42executionv2"
)

const (
	Format           = "pandora-ca42-go-runtime-closure-v1"
	EntryCount       = "7"
	MaxManifestBytes = 32 << 10
)

var (
	hex64     = regexp.MustCompile(`^[0-9a-f]{64}$`)
	safeToken = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

var fieldNames = [...]string{
	"format", "release_id", "release_run_id", "attempt_id", "architecture", "entry_count",
	"root_runner_binary_sha256", "root_runner_build_info_sha256",
	"pathtrust_binary_sha256", "pathtrust_build_info_sha256",
	"manifest_verifier_binary_sha256", "manifest_verifier_build_info_sha256",
	"preflight_runner_binary_sha256", "preflight_runner_build_info_sha256",
	"migration_runner_binary_sha256", "migration_runner_build_info_sha256",
	"goose_binary_sha256", "goose_build_info_sha256",
	"docker_client_binary_sha256", "docker_client_build_info_sha256",
}

type Manifest struct {
	ReleaseID, ReleaseRunID, AttemptID, Architecture              string
	RootRunnerBinarySHA256, RootRunnerBuildInfoSHA256             string
	PathtrustBinarySHA256, PathtrustBuildInfoSHA256               string
	ManifestVerifierBinarySHA256, ManifestVerifierBuildInfoSHA256 string
	PreflightRunnerBinarySHA256, PreflightRunnerBuildInfoSHA256   string
	MigrationRunnerBinarySHA256, MigrationRunnerBuildInfoSHA256   string
	GooseBinarySHA256, GooseBuildInfoSHA256                       string
	DockerClientBinarySHA256, DockerClientBuildInfoSHA256         string
	SHA256                                                        [sha256.Size]byte
	parsed                                                        bool
	canonical                                                     []byte
	parseArchitecture                                             string
}

func Parse(data []byte, expectedSHA256 [sha256.Size]byte, architecture string) (Manifest, error) {
	var empty Manifest
	if len(data) == 0 || len(data) > MaxManifestBytes || data[len(data)-1] != '\n' || bytes.HasSuffix(data, []byte("\n\n")) ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 || bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return empty, errors.New("runtime closure manifest envelope invalid")
	}
	digest := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(digest[:], expectedSHA256[:]) != 1 {
		return empty, errors.New("runtime closure manifest identity mismatch")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) != len(fieldNames) {
		return empty, errors.New("runtime closure manifest field count invalid")
	}
	values := make(map[string]string, len(fieldNames))
	for index, name := range fieldNames {
		prefix := []byte(name + "=")
		if !bytes.HasPrefix(lines[index], prefix) || len(lines[index]) == len(prefix) {
			return empty, fmt.Errorf("runtime closure manifest field order invalid: %s", name)
		}
		values[name] = string(lines[index][len(prefix):])
	}
	if values["format"] != Format || values["architecture"] != architecture ||
		(architecture != "amd64" && architecture != "arm64") || values["entry_count"] != EntryCount {
		return empty, errors.New("runtime closure manifest fixed field mismatch")
	}
	for _, name := range []string{"release_id", "release_run_id", "attempt_id"} {
		if !safeToken.MatchString(values[name]) {
			return empty, fmt.Errorf("runtime closure manifest token invalid: %s", name)
		}
	}
	hashFields := fieldNames[6:]
	seen := map[string]bool{}
	for _, name := range hashFields {
		value := values[name]
		if name == "docker_client_build_info_sha256" && value == "none" {
			continue
		}
		if !nonZeroHex64(value) || seen[value] {
			return empty, fmt.Errorf("runtime closure manifest hash invalid or duplicate: %s", name)
		}
		seen[value] = true
	}
	return Manifest{
		ReleaseID: values["release_id"], ReleaseRunID: values["release_run_id"], AttemptID: values["attempt_id"], Architecture: architecture,
		RootRunnerBinarySHA256: values["root_runner_binary_sha256"], RootRunnerBuildInfoSHA256: values["root_runner_build_info_sha256"],
		PathtrustBinarySHA256: values["pathtrust_binary_sha256"], PathtrustBuildInfoSHA256: values["pathtrust_build_info_sha256"],
		ManifestVerifierBinarySHA256: values["manifest_verifier_binary_sha256"], ManifestVerifierBuildInfoSHA256: values["manifest_verifier_build_info_sha256"],
		PreflightRunnerBinarySHA256: values["preflight_runner_binary_sha256"], PreflightRunnerBuildInfoSHA256: values["preflight_runner_build_info_sha256"],
		MigrationRunnerBinarySHA256: values["migration_runner_binary_sha256"], MigrationRunnerBuildInfoSHA256: values["migration_runner_build_info_sha256"],
		GooseBinarySHA256: values["goose_binary_sha256"], GooseBuildInfoSHA256: values["goose_build_info_sha256"],
		DockerClientBinarySHA256: values["docker_client_binary_sha256"], DockerClientBuildInfoSHA256: values["docker_client_build_info_sha256"],
		SHA256: digest, parsed: true, canonical: append([]byte(nil), data...), parseArchitecture: architecture,
	}, nil
}

func BindPlan(manifest Manifest, plan ca42executionv2.Plan, now time.Time) error {
	trustedManifest, err := manifest.VerifiedCopy()
	if err != nil {
		return err
	}
	trustedPlan, err := plan.VerifiedCopyAt(now)
	if err != nil {
		return err
	}
	manifest, plan = trustedManifest, trustedPlan
	if fmt.Sprintf("%x", manifest.SHA256) != plan.RuntimeClosureManifestSHA256 || manifest.ReleaseID != plan.ReleaseID ||
		manifest.ReleaseRunID != plan.ReleaseRunID || manifest.AttemptID != plan.AttemptID || manifest.Architecture != plan.Architecture ||
		manifest.PathtrustBinarySHA256 != plan.PathtrustBinarySHA256 || manifest.ManifestVerifierBinarySHA256 != plan.ManifestVerifierSHA256 ||
		manifest.PreflightRunnerBinarySHA256 != plan.PreflightRunnerSHA256 || manifest.MigrationRunnerBinarySHA256 != plan.MigrationRunnerSHA256 ||
		manifest.GooseBinarySHA256 != plan.GooseBinarySHA256 || manifest.GooseBuildInfoSHA256 != plan.GooseBuildInfoSHA256 ||
		manifest.DockerClientBinarySHA256 != plan.DockerClientSHA256 {
		return errors.New("runtime closure manifest execution plan binding mismatch")
	}
	return nil
}

func (manifest Manifest) VerifiedCopy() (Manifest, error) {
	if !manifest.parsed || len(manifest.canonical) == 0 || manifest.parseArchitecture == "" {
		return Manifest{}, errors.New("runtime closure parsed capability invalid")
	}
	digest := sha256.Sum256(manifest.canonical)
	if digest != manifest.SHA256 {
		return Manifest{}, errors.New("runtime closure projection identity changed")
	}
	return Parse(manifest.canonical, digest, manifest.parseArchitecture)
}

func CanonicalBytes(values []string) ([]byte, error) {
	if len(values) != len(fieldNames) {
		return nil, errors.New("runtime closure manifest field count invalid")
	}
	var output strings.Builder
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("runtime closure manifest value invalid")
		}
		fmt.Fprintf(&output, "%s=%s\n", fieldNames[index], value)
	}
	return []byte(output.String()), nil
}

func nonZeroHex64(value string) bool {
	return hex64.MatchString(value) && value != strings.Repeat("0", 64)
}

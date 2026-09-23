package ca42capsule

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42execution"
)

const (
	Format          = "client-auth-00042-trust-capsule-v1"
	Transition      = "goose-41-to-42"
	CoreMode        = "0500"
	MaxCapsuleBytes = 64 << 10
)

var (
	hex64      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	safeToken  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
)

var fieldNames = [...]string{
	"format", "transition", "release_id", "release_run_id", "core_sha256",
	"core_chain_sha256", "core_device", "core_mode", "attestation_sha256",
	"expected_sha256", "public_key_sha256", "external_manifest_sha256",
	"target_system_identifier", "target_database_name", "target_database_oid",
	"ledger_namespace", "ledger_directory_sha256",
}

type Capsule struct {
	ReleaseID, ReleaseRunID                 string
	CoreSHA256, CoreChainSHA256             string
	CoreDevice                              uint64
	AttestationSHA256, ExpectedSHA256       string
	PublicKeySHA256, ExternalManifestSHA256 string
	TargetSystemIdentifier, TargetDatabase  string
	TargetDatabaseOID, LedgerNamespace      string
	LedgerDirectorySHA256                   string
	SHA256                                  [sha256.Size]byte
}

func Parse(data []byte, expectedSHA256 [sha256.Size]byte) (Capsule, error) {
	var empty Capsule
	if len(data) == 0 || len(data) > MaxCapsuleBytes || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return empty, errors.New("trust capsule envelope invalid")
	}
	digest := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(digest[:], expectedSHA256[:]) != 1 {
		return empty, errors.New("trust capsule identity mismatch")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) != len(fieldNames) {
		return empty, errors.New("trust capsule field count invalid")
	}
	values := make([]string, len(lines))
	for index, line := range lines {
		prefix := fieldNames[index] + "="
		if !bytes.HasPrefix(line, []byte(prefix)) || len(line) == len(prefix) {
			return empty, fmt.Errorf("trust capsule field order invalid: %s", fieldNames[index])
		}
		values[index] = string(line[len(prefix):])
	}
	if values[0] != Format || values[1] != Transition || values[7] != CoreMode ||
		values[15] != ca42execution.LedgerNamespace || values[16] != ca42execution.LedgerDirectorySHA256 {
		return empty, errors.New("trust capsule fixed field mismatch")
	}
	for _, index := range []int{2, 3, 15} {
		if !safeToken.MatchString(values[index]) {
			return empty, fmt.Errorf("trust capsule token invalid: %s", fieldNames[index])
		}
	}
	for _, index := range []int{4, 5, 8, 9, 10, 11, 16} {
		if !nonZeroHex64(values[index]) {
			return empty, fmt.Errorf("trust capsule SHA256 invalid: %s", fieldNames[index])
		}
	}
	coreDevice, err := positiveUint(values[6], 64)
	if err != nil {
		return empty, errors.New("trust capsule core device invalid")
	}
	if _, err := positiveUint(values[12], 64); err != nil {
		return empty, errors.New("trust capsule target system identifier invalid")
	}
	if !identifier.MatchString(values[13]) {
		return empty, errors.New("trust capsule target database invalid")
	}
	if _, err := positiveUint(values[14], 32); err != nil {
		return empty, errors.New("trust capsule target database OID invalid")
	}
	return Capsule{
		ReleaseID: values[2], ReleaseRunID: values[3], CoreSHA256: values[4],
		CoreChainSHA256: values[5], CoreDevice: coreDevice, AttestationSHA256: values[8],
		ExpectedSHA256: values[9], PublicKeySHA256: values[10], ExternalManifestSHA256: values[11],
		TargetSystemIdentifier: values[12], TargetDatabase: values[13], TargetDatabaseOID: values[14],
		LedgerNamespace: values[15], LedgerDirectorySHA256: values[16], SHA256: digest,
	}, nil
}

func BindPlan(capsule Capsule, plan ca42execution.Plan) error {
	if capsule.ReleaseID != plan.ReleaseID || capsule.ReleaseRunID != plan.ReleaseRunID ||
		capsule.CoreSHA256 != plan.AttestationCoreSHA256 || capsule.CoreChainSHA256 != plan.AttestationCoreChainSHA256 ||
		capsule.CoreDevice != plan.AttestationCoreDevice || capsule.AttestationSHA256 != plan.AttestationSHA256 ||
		capsule.ExpectedSHA256 != plan.ExpectedSHA256 || capsule.PublicKeySHA256 != plan.AttestationPublicKeySHA256 ||
		capsule.ExternalManifestSHA256 != plan.ExternalManifestSHA256 ||
		capsule.TargetSystemIdentifier != plan.SourceSystemIdentifier || capsule.TargetDatabase != plan.SourceDatabase ||
		capsule.TargetDatabaseOID != plan.SourceDatabaseOID || capsule.LedgerNamespace != ca42execution.LedgerNamespace ||
		capsule.LedgerDirectorySHA256 != ca42execution.LedgerDirectorySHA256 {
		return errors.New("trust capsule and execution plan binding mismatch")
	}
	return nil
}

func CanonicalBytes(values []string) ([]byte, error) {
	if len(values) != len(fieldNames) {
		return nil, errors.New("trust capsule field count invalid")
	}
	var output strings.Builder
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("trust capsule value invalid")
		}
		fmt.Fprintf(&output, "%s=%s\n", fieldNames[index], value)
	}
	return []byte(output.String()), nil
}

func nonZeroHex64(value string) bool {
	return hex64.MatchString(value) && value != strings.Repeat("0", 64)
}

func positiveUint(value string, bits int) (uint64, error) {
	if value == "" || value[0] == '0' {
		return 0, errors.New("noncanonical positive integer")
	}
	parsed, err := strconv.ParseUint(value, 10, bits)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("noncanonical positive integer")
	}
	return parsed, nil
}

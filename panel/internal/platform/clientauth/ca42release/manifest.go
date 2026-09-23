package ca42release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42manifest"
)

const (
	Format             = "pandora-client-auth-00042-release-manifest-v2"
	SignatureAlgorithm = "ed25519"
	Status             = "READY_FOR_ROOT_RUNNER"
	MaxManifestBytes   = 64 << 10
	MaxValidity        = time.Hour
)

var (
	lowerHex64    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	safeToken     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	identifier    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	isolatedRunID = regexp.MustCompile(`^pandoraisolatedpg18[A-Za-z0-9]{6}-[1-9][0-9]{0,9}$`)
)

var fieldNames = [...]string{
	"format", "signature_algorithm", "status", "authority_epoch", "authority_sequence",
	"authority_binding_sha256", "release_signer_key_id", "architecture", "release_id",
	"release_run_id", "attempt_id", "migration_runner_sha256", "manifest_verifier_sha256",
	"preflight_runner_sha256", "goose_binary_sha256", "migration_set_sha256",
	"client_auth_00042_sha256", "globals_dump_sha256", "database_dump_sha256",
	"postgres_image_sha256", "production_source_container_id",
	"production_source_system_identifier", "production_source_database",
	"production_source_database_oid", "production_source_database_owner_oid",
	"production_source_database_owner_name", "production_source_goose_waterline",
	"isolated_target_container_id", "isolated_target_system_identifier",
	"isolated_target_network_id", "isolated_target_database", "isolated_target_database_oid",
	"isolated_target_image_id", "isolated_target_run_id", "external_manifest_sha256",
	"external_object_manifest_sha256", "attestation_public_key_sha256",
	"release_journal_head_sha256", "execution_plan_sha256", "not_before_epoch", "not_after_epoch", "signature_b64",
}

type Manifest struct {
	AuthorityEpoch                   uint64
	AuthoritySequence                uint64
	AuthorityBindingSHA256           string
	ReleaseSignerKeyID               string
	Architecture                     string
	ReleaseID                        string
	ReleaseRunID                     string
	AttemptID                        string
	MigrationRunnerSHA256            string
	ManifestVerifierSHA256           string
	PreflightRunnerSHA256            string
	GooseBinarySHA256                string
	MigrationSetSHA256               string
	ClientAuth00042SHA256            string
	GlobalsDumpSHA256                string
	DatabaseDumpSHA256               string
	PostgresImageSHA256              string
	ProductionSourceContainerID      string
	ProductionSourceSystemIdentifier string
	ProductionSourceDatabase         string
	ProductionSourceDatabaseOID      string
	ProductionSourceDatabaseOwnerOID string
	ProductionSourceDatabaseOwner    string
	ProductionSourceGooseWaterline   uint64
	IsolatedTargetContainerID        string
	IsolatedTargetSystemIdentifier   string
	IsolatedTargetNetworkID          string
	IsolatedTargetDatabase           string
	IsolatedTargetDatabaseOID        string
	IsolatedTargetImageID            string
	IsolatedTargetRunID              string
	ExternalManifestSHA256           string
	ExternalObjectManifestSHA256     string
	AttestationPublicKeySHA256       string
	ReleaseJournalHeadSHA256         string
	ExecutionPlanSHA256              string
	NotBefore                        time.Time
	NotAfter                         time.Time
}

type AuthorityBinding struct {
	PublicKey          ed25519.PublicKey
	ManifestSHA256     [sha256.Size]byte
	SignerSHA256       [sha256.Size]byte
	Epoch              uint64
	Sequence           uint64
	BindingSHA256      [sha256.Size]byte
	ReleaseSignerKeyID string
}

// ParseAndVerify validates the signed contract but does not by itself authorize
// a migration. A root-owned runner must supply authority pins that ordinary
// callers cannot override, then bind retained file descriptors to every SHA.
func ParseAndVerify(data []byte, authority AuthorityBinding, architecture string, now time.Time, externalManifestBytes []byte) (Manifest, error) {
	var empty Manifest
	if len(data) == 0 || len(data) > MaxManifestBytes || data[len(data)-1] != '\n' || bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 || bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return empty, errors.New("invalid release manifest envelope")
	}
	digest := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(digest[:], authority.ManifestSHA256[:]) != 1 {
		return empty, errors.New("release manifest identity mismatch")
	}
	if len(authority.PublicKey) != ed25519.PublicKeySize || authority.Epoch == 0 || authority.Sequence == 0 ||
		authority.BindingSHA256 == ([sha256.Size]byte{}) || !safeToken.MatchString(authority.ReleaseSignerKeyID) {
		return empty, errors.New("invalid approved authority key")
	}
	keyDigest := sha256.Sum256(authority.PublicKey)
	if subtle.ConstantTimeCompare(keyDigest[:], authority.SignerSHA256[:]) != 1 {
		return empty, errors.New("release authority identity mismatch")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) != len(fieldNames) {
		return empty, errors.New("release manifest line count mismatch")
	}
	values := make([]string, len(lines))
	for i, line := range lines {
		prefix := fieldNames[i] + "="
		if !bytes.HasPrefix(line, []byte(prefix)) || len(line) == len(prefix) {
			return empty, fmt.Errorf("release manifest field order mismatch: %s", fieldNames[i])
		}
		values[i] = string(line[len(prefix):])
	}
	if values[0] != Format || values[1] != SignatureAlgorithm || values[2] != Status || values[7] != architecture ||
		(architecture != "amd64" && architecture != "arm64") {
		return empty, errors.New("release manifest fixed field mismatch")
	}
	authorityEpoch, epochErr := parsePositiveUint(values[3])
	authoritySequence, sequenceErr := parsePositiveUint(values[4])
	if epochErr != nil || sequenceErr != nil || authorityEpoch != authority.Epoch || authoritySequence != authority.Sequence ||
		!constantHashEqual(values[5], authority.BindingSHA256) || values[6] != authority.ReleaseSignerKeyID {
		return empty, errors.New("release manifest authority binding mismatch")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(values[41])
	if err != nil || len(signature) != ed25519.SignatureSize || base64.StdEncoding.EncodeToString(signature) != values[41] {
		return empty, errors.New("release manifest signature encoding invalid")
	}
	signedLength := len(data) - len(lines[41]) - 1
	if signedLength <= 0 || !ed25519.Verify(authority.PublicKey, data[:signedLength], signature) {
		return empty, errors.New("release manifest signature denied")
	}
	for _, index := range []int{5, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 27, 29, 34, 35, 36, 37, 38} {
		if !nonZeroHex64(values[index]) {
			return empty, fmt.Errorf("noncanonical SHA256: %s", fieldNames[index])
		}
	}
	for _, index := range []int{6, 8, 9, 10} {
		if !safeToken.MatchString(values[index]) {
			return empty, fmt.Errorf("unsafe token: %s", fieldNames[index])
		}
	}
	if !isolatedRunID.MatchString(values[33]) {
		return empty, errors.New("isolated target run ID invalid")
	}
	for _, index := range []int{22, 25, 30} {
		if !identifier.MatchString(values[index]) {
			return empty, fmt.Errorf("unsafe identifier: %s", fieldNames[index])
		}
	}
	for _, index := range []int{21, 23, 24, 26, 28, 31, 39, 40} {
		if !canonicalPositiveDecimal(values[index]) {
			return empty, fmt.Errorf("noncanonical positive integer: %s", fieldNames[index])
		}
	}
	for _, index := range []int{23, 24, 31} {
		if !canonicalOID(values[index]) {
			return empty, fmt.Errorf("OID out of range: %s", fieldNames[index])
		}
	}
	if values[16] != ca42manifest.FrozenMigrationSHA256 || values[26] != "41" || values[32] != "sha256:"+values[19] {
		return empty, errors.New("frozen migration, waterline or image binding mismatch")
	}
	if values[20] == values[27] || values[21] == values[28] {
		return empty, errors.New("production and isolated identities are conflated")
	}
	if values[22] != values[30] {
		return empty, errors.New("database restore target name mismatch")
	}
	external, err := ca42manifest.Verify(externalManifestBytes)
	if err != nil {
		return empty, errors.New("external manifest structural verification denied")
	}
	if external.Decision != "STRUCTURALLY_VALID" || external.Authorization != "NONE" || external.ContractSHA256 != ca42manifest.ContractSHA256 || values[27] != external.IsolatedContainerID || values[28] != external.IsolatedSystemIdentifier || values[29] != external.IsolatedNetworkID || values[30] != external.IsolatedDatabase || values[31] != external.IsolatedDatabaseOID || values[32] != external.IsolatedImageID || values[33] != external.IsolatedRunID || values[34] != external.ExternalManifestSHA256 || values[35] != external.ExactObjectManifestSHA256 {
		return empty, errors.New("external manifest dual-identity binding mismatch")
	}
	notBeforeEpoch, err := strconv.ParseInt(values[39], 10, 64)
	if err != nil {
		return empty, errors.New("release manifest not-before invalid")
	}
	notAfterEpoch, err := strconv.ParseInt(values[40], 10, 64)
	if err != nil {
		return empty, errors.New("release manifest not-after invalid")
	}
	maxValiditySeconds := int64(MaxValidity / time.Second)
	if notAfterEpoch <= notBeforeEpoch || notAfterEpoch-notBeforeEpoch > maxValiditySeconds {
		return empty, errors.New("release manifest validity window invalid")
	}
	nowEpoch := now.UTC().Unix()
	if nowEpoch < notBeforeEpoch || nowEpoch >= notAfterEpoch {
		return empty, errors.New("release manifest outside validity window")
	}
	return Manifest{
		AuthorityEpoch: authorityEpoch, AuthoritySequence: authoritySequence,
		AuthorityBindingSHA256: values[5], ReleaseSignerKeyID: values[6],
		Architecture: values[7], ReleaseID: values[8], ReleaseRunID: values[9], AttemptID: values[10],
		MigrationRunnerSHA256: values[11], ManifestVerifierSHA256: values[12], PreflightRunnerSHA256: values[13],
		GooseBinarySHA256: values[14], MigrationSetSHA256: values[15], ClientAuth00042SHA256: values[16],
		GlobalsDumpSHA256: values[17], DatabaseDumpSHA256: values[18], PostgresImageSHA256: values[19],
		ProductionSourceContainerID: values[20], ProductionSourceSystemIdentifier: values[21],
		ProductionSourceDatabase: values[22], ProductionSourceDatabaseOID: values[23],
		ProductionSourceDatabaseOwnerOID: values[24], ProductionSourceDatabaseOwner: values[25],
		ProductionSourceGooseWaterline: 41, IsolatedTargetContainerID: values[27],
		IsolatedTargetSystemIdentifier: values[28], IsolatedTargetNetworkID: values[29],
		IsolatedTargetDatabase: values[30], IsolatedTargetDatabaseOID: values[31],
		IsolatedTargetImageID: values[32], IsolatedTargetRunID: values[33],
		ExternalManifestSHA256: values[34], ExternalObjectManifestSHA256: values[35],
		AttestationPublicKeySHA256: values[36], ReleaseJournalHeadSHA256: values[37],
		ExecutionPlanSHA256: values[38],
		NotBefore:           time.Unix(notBeforeEpoch, 0).UTC(), NotAfter: time.Unix(notAfterEpoch, 0).UTC(),
	}, nil
}

func parsePositiveUint(value string) (uint64, error) {
	if !canonicalPositiveDecimal(value) {
		return 0, errors.New("noncanonical positive integer")
	}
	return strconv.ParseUint(value, 10, 64)
}

func constantHashEqual(value string, expected [sha256.Size]byte) bool {
	if !lowerHex64.MatchString(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && subtle.ConstantTimeCompare(decoded, expected[:]) == 1
}

func nonZeroHex64(value string) bool {
	return lowerHex64.MatchString(value) && value != strings.Repeat("0", 64)
}

func canonicalPositiveDecimal(value string) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == value
}

func canonicalOID(value string) bool {
	if !canonicalPositiveDecimal(value) {
		return false
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	return err == nil && parsed > 0
}

func SignatureInput(lines []string) ([]byte, error) {
	if len(lines) != len(fieldNames)-1 {
		return nil, errors.New("signature input field count mismatch")
	}
	var out strings.Builder
	for i, value := range lines {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("invalid signature input value")
		}
		fmt.Fprintf(&out, "%s=%s\n", fieldNames[i], value)
	}
	return []byte(out.String()), nil
}

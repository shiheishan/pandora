package ca42authority

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
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Format             = "pandora-ca42-authority-descriptor-v1"
	SignatureAlgorithm = "ed25519"
	SignatureDomain    = "pandora.ca42.authority.v1"
	Purpose            = "client-auth-00042-release"
	Status             = "AUTHORIZED"
	ModeNormal         = "NORMAL"
	ModeRecovery       = "RECOVERY"
	MaxDescriptorBytes = 64 << 10
	MaxValidity        = time.Hour
	RequiredRootCount  = 3
	RequiredQuorum     = 2
)

var domainPrefix = []byte("PANDORA\x00CA42-AUTHORITY-DESCRIPTOR\x00V1\x00")
var bindingDomainPrefix = []byte("PANDORA\x00CA42-AUTHORITY-BINDING\x00V1\x00")

var (
	lowerHex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	safeToken  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
)

var fieldNames = [...]string{
	"format", "signature_algorithm", "signature_domain", "purpose", "status",
	"root_keyset_id", "ledger_id", "authority_epoch", "authority_sequence",
	"previous_descriptor_sha256", "authorization_mode", "architecture",
	"host_identity_sha256", "release_id", "release_run_id", "attempt_id",
	"release_manifest_sha256", "release_signer_key_id", "release_signer_public_key_b64",
	"release_signer_sha256", "root_runner_sha256", "not_before_epoch", "not_after_epoch",
	"clock_floor_epoch", "root_signature_1_key_id", "root_signature_1_b64",
	"root_signature_2_key_id", "root_signature_2_b64",
}

const signedFieldCount = 24

type RootKey struct {
	ID        string
	PublicKey ed25519.PublicKey
}

type RootKeyset struct {
	ID     string
	Quorum int
	Keys   []RootKey
}

type Descriptor struct {
	RootKeysetID          string
	LedgerID              string
	AuthorityEpoch        uint64
	AuthoritySequence     uint64
	PreviousDescriptor    [sha256.Size]byte
	AuthorizationMode     string
	Architecture          string
	HostIdentitySHA256    [sha256.Size]byte
	ReleaseID             string
	ReleaseRunID          string
	AttemptID             string
	ReleaseManifestSHA256 [sha256.Size]byte
	ReleaseSignerKeyID    string
	ReleaseSignerKey      ed25519.PublicKey
	ReleaseSignerSHA256   [sha256.Size]byte
	RootRunnerSHA256      [sha256.Size]byte
	NotBefore             time.Time
	NotAfter              time.Time
	ClockFloor            time.Time
	RootSignatureKeyIDs   [RequiredQuorum]string
	SHA256                [sha256.Size]byte
	BindingSHA256         [sha256.Size]byte
}

func ParseAndVerify(data []byte, roots RootKeyset, architecture string, hostIdentity [sha256.Size]byte, now time.Time) (Descriptor, error) {
	var empty Descriptor
	rootMap, err := validateRootKeyset(roots)
	if err != nil {
		return empty, err
	}
	if architecture == "" {
		architecture = runtime.GOARCH
	}
	if architecture != "amd64" && architecture != "arm64" {
		return empty, errors.New("authority architecture unsupported")
	}
	if len(data) == 0 || len(data) > MaxDescriptorBytes || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return empty, errors.New("authority descriptor envelope invalid")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) != len(fieldNames) {
		return empty, errors.New("authority descriptor field count invalid")
	}
	values := make([]string, len(lines))
	for i, line := range lines {
		prefix := fieldNames[i] + "="
		if !bytes.HasPrefix(line, []byte(prefix)) || len(line) == len(prefix) {
			return empty, fmt.Errorf("authority descriptor field order invalid: %s", fieldNames[i])
		}
		values[i] = string(line[len(prefix):])
	}
	if values[0] != Format || values[1] != SignatureAlgorithm || values[2] != SignatureDomain ||
		values[3] != Purpose || values[4] != Status || values[5] != roots.ID || values[11] != architecture {
		return empty, errors.New("authority descriptor fixed field mismatch")
	}
	for _, index := range []int{5, 13, 14, 15, 17, 24, 26} {
		if !safeToken.MatchString(values[index]) {
			return empty, fmt.Errorf("authority token invalid: %s", fieldNames[index])
		}
	}
	for _, index := range []int{6, 9, 12, 16, 19, 20} {
		if !lowerHex64.MatchString(values[index]) {
			return empty, fmt.Errorf("authority sha256 invalid: %s", fieldNames[index])
		}
	}
	if values[10] != ModeNormal && values[10] != ModeRecovery {
		return empty, errors.New("authority mode invalid")
	}
	epoch, err := parsePositiveUint(values[7])
	if err != nil {
		return empty, errors.New("authority epoch invalid")
	}
	sequence, err := parsePositiveUint(values[8])
	if err != nil {
		return empty, errors.New("authority sequence invalid")
	}
	notBeforeEpoch, err := parsePositiveInt(values[21])
	if err != nil {
		return empty, errors.New("authority not-before invalid")
	}
	notAfterEpoch, err := parsePositiveInt(values[22])
	if err != nil {
		return empty, errors.New("authority not-after invalid")
	}
	clockFloorEpoch, err := parsePositiveInt(values[23])
	if err != nil {
		return empty, errors.New("authority clock floor invalid")
	}
	maxSeconds := int64(MaxValidity / time.Second)
	if notAfterEpoch <= notBeforeEpoch || notAfterEpoch-notBeforeEpoch > maxSeconds || clockFloorEpoch > notBeforeEpoch {
		return empty, errors.New("authority time policy invalid")
	}
	nowEpoch := now.UTC().Unix()
	if nowEpoch < notBeforeEpoch || nowEpoch >= notAfterEpoch || nowEpoch < clockFloorEpoch {
		return empty, errors.New("authority descriptor outside trusted time window")
	}
	signerKey, err := decodeCanonicalBase64(values[18], ed25519.PublicKeySize)
	if err != nil {
		return empty, errors.New("authority release signer key invalid")
	}
	signerDigest := sha256.Sum256(signerKey)
	expectedSignerDigest, _ := decodeHex32(values[19])
	if subtle.ConstantTimeCompare(signerDigest[:], expectedSignerDigest[:]) != 1 {
		return empty, errors.New("authority release signer identity mismatch")
	}
	if _, rootIDCollision := rootMap[values[17]]; rootIDCollision {
		return empty, errors.New("authority release signer ID collides with root key")
	}
	for _, rootKey := range rootMap {
		if subtle.ConstantTimeCompare(rootKey, signerKey) == 1 {
			return empty, errors.New("authority release signer must be separated from root keys")
		}
	}
	keyIDs := [RequiredQuorum]string{values[24], values[26]}
	signedInput, err := SignatureInput(values[:signedFieldCount], keyIDs)
	if err != nil {
		return empty, err
	}
	if keyIDs[0] >= keyIDs[1] {
		return empty, errors.New("authority root signature keys must be unique and sorted")
	}
	for index, keyID := range keyIDs {
		publicKey, ok := rootMap[keyID]
		if !ok {
			return empty, errors.New("authority root signature key is not compiled")
		}
		signature, decodeErr := decodeCanonicalBase64(values[25+index*2], ed25519.SignatureSize)
		if decodeErr != nil || !ed25519.Verify(publicKey, signedInput, signature) {
			return empty, errors.New("authority root signature denied")
		}
	}
	if !constantHashEqual(values[12], hostIdentity) {
		return empty, errors.New("authority host identity mismatch")
	}
	previous, _ := decodeHex32(values[9])
	manifestDigest, _ := decodeHex32(values[16])
	runnerDigest, _ := decodeHex32(values[20])
	descriptorDigest := sha256.Sum256(data)
	bindingBytes, err := bindingInput(values[:signedFieldCount])
	if err != nil {
		return empty, err
	}
	bindingDigest := sha256.Sum256(bindingBytes)
	return Descriptor{
		RootKeysetID: roots.ID, LedgerID: values[6], AuthorityEpoch: epoch,
		AuthoritySequence: sequence, PreviousDescriptor: previous, AuthorizationMode: values[10],
		Architecture: architecture, HostIdentitySHA256: hostIdentity, ReleaseID: values[13],
		ReleaseRunID: values[14], AttemptID: values[15], ReleaseManifestSHA256: manifestDigest,
		ReleaseSignerKeyID: values[17], ReleaseSignerKey: ed25519.PublicKey(append([]byte(nil), signerKey...)),
		ReleaseSignerSHA256: signerDigest, RootRunnerSHA256: runnerDigest,
		NotBefore: time.Unix(notBeforeEpoch, 0).UTC(), NotAfter: time.Unix(notAfterEpoch, 0).UTC(),
		ClockFloor: time.Unix(clockFloorEpoch, 0).UTC(), RootSignatureKeyIDs: keyIDs,
		SHA256: descriptorDigest, BindingSHA256: bindingDigest,
	}, nil
}

func SignatureInput(values []string, keyIDs [RequiredQuorum]string) ([]byte, error) {
	if len(values) != signedFieldCount {
		return nil, errors.New("authority signature input field count invalid")
	}
	if !safeToken.MatchString(keyIDs[0]) || !safeToken.MatchString(keyIDs[1]) || keyIDs[0] >= keyIDs[1] {
		return nil, errors.New("authority signature key IDs invalid")
	}
	var out strings.Builder
	for i, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("authority signature input value invalid")
		}
		fmt.Fprintf(&out, "%s=%s\n", fieldNames[i], value)
	}
	fmt.Fprintf(&out, "%s=%s\n", fieldNames[24], keyIDs[0])
	fmt.Fprintf(&out, "%s=%s\n", fieldNames[26], keyIDs[1])
	result := make([]byte, 0, len(domainPrefix)+out.Len())
	result = append(result, domainPrefix...)
	result = append(result, out.String()...)
	return result, nil
}

// bindingInput excludes release_manifest_sha256 (field 16) to avoid a circular
// hash. The descriptor binds the release manifest, and the release manifest
// binds this stable authority context.
func bindingInput(values []string) ([]byte, error) {
	if len(values) != signedFieldCount {
		return nil, errors.New("authority binding input field count invalid")
	}
	result := append([]byte(nil), bindingDomainPrefix...)
	for index, value := range values {
		if index == 16 {
			continue
		}
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("authority binding input value invalid")
		}
		result = append(result, []byte(fieldNames[index]+"="+value+"\n")...)
	}
	return result, nil
}

func validateRootKeyset(roots RootKeyset) (map[string]ed25519.PublicKey, error) {
	if !safeToken.MatchString(roots.ID) || roots.Quorum != RequiredQuorum || len(roots.Keys) != RequiredRootCount {
		return nil, errors.New("compiled authority root keyset invalid")
	}
	result := make(map[string]ed25519.PublicKey, len(roots.Keys))
	publicKeyDigests := make(map[[sha256.Size]byte]struct{}, len(roots.Keys))
	previous := ""
	for _, key := range roots.Keys {
		if !safeToken.MatchString(key.ID) || len(key.PublicKey) != ed25519.PublicKeySize ||
			(previous != "" && key.ID <= previous) {
			return nil, errors.New("compiled authority root key invalid")
		}
		digest := sha256.Sum256(key.PublicKey)
		if _, duplicate := publicKeyDigests[digest]; duplicate {
			return nil, errors.New("compiled authority root public keys must be distinct")
		}
		publicKeyDigests[digest] = struct{}{}
		result[key.ID] = ed25519.PublicKey(append([]byte(nil), key.PublicKey...))
		previous = key.ID
	}
	return result, nil
}

func parsePositiveUint(value string) (uint64, error) {
	if value == "" || value[0] == '0' {
		return 0, errors.New("noncanonical positive uint")
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("noncanonical positive uint")
	}
	return parsed, nil
}

func parsePositiveInt(value string) (int64, error) {
	if value == "" || value[0] == '0' {
		return 0, errors.New("noncanonical positive int")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, errors.New("noncanonical positive int")
	}
	return parsed, nil
}

func decodeCanonicalBase64(value string, size int) ([]byte, error) {
	decoded, err := base64.StdEncoding.Strict().DecodeString(value)
	if err != nil || len(decoded) != size || base64.StdEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("noncanonical base64")
	}
	return decoded, nil
}

func decodeHex32(value string) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	if !lowerHex64.MatchString(value) {
		return result, errors.New("noncanonical sha256")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return result, errors.New("noncanonical sha256")
	}
	copy(result[:], decoded)
	return result, nil
}

func constantHashEqual(value string, expected [sha256.Size]byte) bool {
	decoded, err := decodeHex32(value)
	return err == nil && subtle.ConstantTimeCompare(decoded[:], expected[:]) == 1
}

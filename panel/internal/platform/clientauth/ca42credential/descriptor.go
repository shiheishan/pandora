package ca42credential

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	Format                  = "pandora-ca42-credential-source-descriptor-v1"
	Kind                    = "postgres-password"
	CredentialName          = "source-postgres-password"
	CredentialFormat        = "opaque-bytes-v1"
	Delivery                = "retained-fd-only-v1"
	Mode                    = "0400"
	MinCredentialBytes      = 32
	MaxCredentialBytes      = 1024
	MaxDescriptorBytes      = 16 << 10
	MaxValidity             = time.Hour
	CommitmentHMACKeyringV1 = "hmac-sha256-keyring-v1"
)

var (
	hex64      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	token      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
)

var fieldNames = [...]string{
	"format", "kind", "credential_name", "credential_format",
	"credential_commitment_algorithm", "credential_commitment_key_id", "credential_commitment",
	"credential_mode", "credential_size_bytes", "delivery",
	"release_id", "release_run_id", "attempt_id", "source_container_id",
	"source_system_identifier", "source_database", "source_database_oid",
	"source_database_owner_name", "source_database_owner_oid",
	"not_before_epoch", "not_after_epoch",
}

type Descriptor struct {
	CommitmentAlgorithm, CommitmentKeyID, Commitment string
	ReleaseID, ReleaseRunID, AttemptID               string
	SourceContainerID, SourceSystemIdentifier        string
	SourceDatabase, SourceDatabaseOID                string
	SourceDatabaseOwner, SourceDatabaseOwnerOID      string
	CredentialSizeBytes                              uint64
	NotBefore, NotAfter                              time.Time
	SHA256                                           [sha256.Size]byte
	parsed                                           bool
	canonical                                        []byte
	parseNow                                         time.Time
}

func Parse(data []byte, expectedSHA256 [sha256.Size]byte, now time.Time) (Descriptor, error) {
	var empty Descriptor
	if len(data) == 0 || len(data) > MaxDescriptorBytes || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return empty, errors.New("credential source descriptor envelope invalid")
	}
	digest := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(digest[:], expectedSHA256[:]) != 1 {
		return empty, errors.New("credential source descriptor identity mismatch")
	}
	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) != len(fieldNames) {
		return empty, errors.New("credential source descriptor field count invalid")
	}
	values := make(map[string]string, len(fieldNames))
	for index, name := range fieldNames {
		prefix := []byte(name + "=")
		if !bytes.HasPrefix(lines[index], prefix) || len(lines[index]) == len(prefix) {
			return empty, fmt.Errorf("credential source descriptor field order invalid: %s", name)
		}
		values[name] = string(lines[index][len(prefix):])
	}
	if values["format"] != Format || values["kind"] != Kind || values["credential_name"] != CredentialName ||
		values["credential_format"] != CredentialFormat || values["credential_mode"] != Mode || values["delivery"] != Delivery {
		return empty, errors.New("credential source descriptor fixed field mismatch")
	}
	algorithm, keyID := values["credential_commitment_algorithm"], values["credential_commitment_key_id"]
	if algorithm != CommitmentHMACKeyringV1 || !token.MatchString(keyID) || keyID == "none" {
		return empty, errors.New("credential source descriptor commitment algorithm invalid")
	}
	credentialSize, err := parseCredentialSize(values["credential_size_bytes"])
	if err != nil {
		return empty, errors.New("credential source descriptor size invalid")
	}
	if !nonZeroHex64(values["credential_commitment"]) || !nonZeroHex64(values["source_container_id"]) {
		return empty, errors.New("credential source descriptor commitment or source identity invalid")
	}
	for _, name := range []string{"release_id", "release_run_id", "attempt_id"} {
		if !token.MatchString(values[name]) {
			return empty, fmt.Errorf("credential source descriptor token invalid: %s", name)
		}
	}
	if !canonicalPositiveUint(values["source_system_identifier"], 64) ||
		!identifier.MatchString(values["source_database"]) || !canonicalPositiveUint(values["source_database_oid"], 32) ||
		!identifier.MatchString(values["source_database_owner_name"]) || !canonicalPositiveUint(values["source_database_owner_oid"], 32) {
		return empty, errors.New("credential source descriptor database identity invalid")
	}
	notBefore, err := positiveEpoch(values["not_before_epoch"])
	if err != nil {
		return empty, errors.New("credential source descriptor not-before invalid")
	}
	notAfter, err := positiveEpoch(values["not_after_epoch"])
	if err != nil || notAfter <= notBefore || notAfter-notBefore > int64(MaxValidity/time.Second) {
		return empty, errors.New("credential source descriptor validity invalid")
	}
	nowEpoch := now.UTC().Unix()
	if nowEpoch < notBefore || nowEpoch >= notAfter {
		return empty, errors.New("credential source descriptor outside validity window")
	}
	return Descriptor{
		CommitmentAlgorithm: algorithm, CommitmentKeyID: keyID, Commitment: values["credential_commitment"],
		ReleaseID: values["release_id"], ReleaseRunID: values["release_run_id"], AttemptID: values["attempt_id"],
		SourceContainerID: values["source_container_id"], SourceSystemIdentifier: values["source_system_identifier"],
		SourceDatabase: values["source_database"], SourceDatabaseOID: values["source_database_oid"],
		SourceDatabaseOwner: values["source_database_owner_name"], SourceDatabaseOwnerOID: values["source_database_owner_oid"],
		CredentialSizeBytes: credentialSize,
		NotBefore:           time.Unix(notBefore, 0).UTC(), NotAfter: time.Unix(notAfter, 0).UTC(), SHA256: digest, parsed: true,
		canonical: append([]byte(nil), data...), parseNow: now.UTC(),
	}, nil
}

// VerifyHMACCredential validates an opaque credential against a
// domain-separated HMAC commitment. The key must come from the fixed
// kernel-keyring loader; public, unkeyed commitments are unsupported.
func VerifyHMACCredential(secret, commitmentKey []byte, descriptor Descriptor, now time.Time) error {
	trusted, err := descriptor.VerifiedCopy(now)
	if err != nil {
		return err
	}
	descriptor = trusted
	nowEpoch := now.UTC().Unix()
	if !descriptor.parsed || descriptor.SHA256 == ([sha256.Size]byte{}) ||
		nowEpoch < descriptor.NotBefore.Unix() || nowEpoch >= descriptor.NotAfter.Unix() ||
		descriptor.NotBefore.Location() != time.UTC || descriptor.NotAfter.Location() != time.UTC ||
		descriptor.NotAfter.Sub(descriptor.NotBefore) <= 0 || descriptor.NotAfter.Sub(descriptor.NotBefore) > MaxValidity ||
		descriptor.CommitmentAlgorithm != CommitmentHMACKeyringV1 ||
		!token.MatchString(descriptor.CommitmentKeyID) || descriptor.CommitmentKeyID == "none" ||
		!validCommitmentContext(descriptor) || !nonZeroHex64(descriptor.Commitment) ||
		len(commitmentKey) != sha256.Size || len(secret) != int(descriptor.CredentialSizeBytes) ||
		len(secret) < MinCredentialBytes || len(secret) > MaxCredentialBytes || bytes.IndexAny(secret, "\r\n\x00") >= 0 {
		return errors.New("credential source bytes invalid")
	}
	digest := hmac.New(sha256.New, commitmentKey)
	domain := commitmentDomain(descriptor, secret)
	defer clear(domain)
	_, _ = digest.Write(domain)
	expected, err := hex.DecodeString(descriptor.Commitment)
	if err != nil || !hmac.Equal(digest.Sum(nil), expected) {
		return errors.New("credential source commitment mismatch")
	}
	return nil
}

// HMACCommitment computes the domain-separated commitment. The key is not
// serialized into the descriptor.
func HMACCommitment(secret, commitmentKey []byte, descriptor Descriptor) (string, error) {
	if descriptor.CommitmentAlgorithm != CommitmentHMACKeyringV1 ||
		!token.MatchString(descriptor.CommitmentKeyID) || descriptor.CommitmentKeyID == "none" ||
		!validCommitmentContext(descriptor) || len(commitmentKey) != sha256.Size ||
		len(secret) != int(descriptor.CredentialSizeBytes) || len(secret) < MinCredentialBytes ||
		len(secret) > MaxCredentialBytes || bytes.IndexAny(secret, "\r\n\x00") >= 0 {
		return "", errors.New("credential source commitment input invalid")
	}
	digest := hmac.New(sha256.New, commitmentKey)
	domain := commitmentDomain(descriptor, secret)
	defer clear(domain)
	_, _ = digest.Write(domain)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func commitmentDomain(descriptor Descriptor, secret []byte) []byte {
	result := make([]byte, 0, 320+len(secret))
	result = append(result, "pandora-ca42-credential-commitment-v2\x00release="...)
	result = append(result, descriptor.ReleaseID...)
	result = append(result, "\x00run="...)
	result = append(result, descriptor.ReleaseRunID...)
	result = append(result, "\x00attempt="...)
	result = append(result, descriptor.AttemptID...)
	result = append(result, "\x00container="...)
	result = append(result, descriptor.SourceContainerID...)
	result = append(result, "\x00system="...)
	result = append(result, descriptor.SourceSystemIdentifier...)
	result = append(result, "\x00database="...)
	result = append(result, descriptor.SourceDatabase...)
	result = append(result, "\x00database_oid="...)
	result = append(result, descriptor.SourceDatabaseOID...)
	result = append(result, "\x00owner="...)
	result = append(result, descriptor.SourceDatabaseOwner...)
	result = append(result, "\x00owner_oid="...)
	result = append(result, descriptor.SourceDatabaseOwnerOID...)
	result = append(result, "\x00key_id="...)
	result = append(result, descriptor.CommitmentKeyID...)
	result = append(result, "\x00kind="...)
	result = append(result, Kind...)
	result = append(result, "\x00length="...)
	result = strconv.AppendInt(result, int64(len(secret)), 10)
	result = append(result, 0)
	result = append(result, secret...)
	return result
}

func validCommitmentContext(descriptor Descriptor) bool {
	return token.MatchString(descriptor.ReleaseID) && token.MatchString(descriptor.ReleaseRunID) &&
		token.MatchString(descriptor.AttemptID) && nonZeroHex64(descriptor.SourceContainerID) &&
		canonicalPositiveUint(descriptor.SourceSystemIdentifier, 64) && identifier.MatchString(descriptor.SourceDatabase) &&
		canonicalPositiveUint(descriptor.SourceDatabaseOID, 32) && identifier.MatchString(descriptor.SourceDatabaseOwner) &&
		canonicalPositiveUint(descriptor.SourceDatabaseOwnerOID, 32) && descriptor.CredentialSizeBytes >= MinCredentialBytes &&
		descriptor.CredentialSizeBytes <= MaxCredentialBytes
}

func CanonicalBytes(values []string) ([]byte, error) {
	if len(values) != len(fieldNames) {
		return nil, errors.New("credential source descriptor field count invalid")
	}
	var output strings.Builder
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("credential source descriptor value invalid")
		}
		fmt.Fprintf(&output, "%s=%s\n", fieldNames[index], value)
	}
	return []byte(output.String()), nil
}

// VerifiedCopy reparses private canonical bytes at the supplied trusted time.
func (descriptor Descriptor) VerifiedCopy(now time.Time) (Descriptor, error) {
	if !descriptor.parsed || len(descriptor.canonical) == 0 || descriptor.parseNow.IsZero() {
		return Descriptor{}, errors.New("credential source parsed capability invalid")
	}
	digest := sha256.Sum256(descriptor.canonical)
	if digest != descriptor.SHA256 {
		return Descriptor{}, errors.New("credential source projection identity changed")
	}
	return Parse(descriptor.canonical, digest, now)
}

func (descriptor Descriptor) IsParsed() bool {
	_, err := descriptor.VerifiedCopy(descriptor.parseNow)
	return err == nil
}

func nonZeroHex64(value string) bool {
	return hex64.MatchString(value) && value != strings.Repeat("0", 64)
}

func parseCredentialSize(value string) (uint64, error) {
	parsed, err := strconv.ParseUint(value, 10, 16)
	if err != nil || parsed < MinCredentialBytes || parsed > MaxCredentialBytes || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("credential size invalid")
	}
	return parsed, nil
}

func canonicalPositiveUint(value string, bits int) bool {
	if value == "" || value[0] == '0' {
		return false
	}
	parsed, err := strconv.ParseUint(value, 10, bits)
	return err == nil && parsed > 0 && strconv.FormatUint(parsed, 10) == value
}

func positiveEpoch(value string) (int64, error) {
	if value == "" || value[0] == '0' || len(value) > 10 {
		return 0, errors.New("epoch invalid")
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != value {
		return 0, errors.New("epoch invalid")
	}
	return parsed, nil
}

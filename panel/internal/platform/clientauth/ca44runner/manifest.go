package ca44runner

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var manifestKeys = [...]string{
	"format", "signature_algorithm", "status", "architecture", "runner_sha256",
	"expectations_format", "release_id", "contract_sha256", "classifier_sha256",
	"verifier_sha256", "rule_version", "source_format", "source_length",
	"source_sha256", "artifact_format", "artifact_length", "artifact_sha256",
	"artifact_hmac_version", "artifact_hmac_key_id", "artifact_hmac_sha256",
	"evidence_hmac_key_id", "input_rows", "output_rows", "provable", "orphan",
	"cross_tenant", "unprovable_key", "detached_manifest_format", "receipt_format",
	"release_expectations_sha256", "signature_b64",
}

func ParseAndVerifyManifest(
	data []byte,
	approvedPublicKey ed25519.PublicKey,
	expectedManifestSHA256 [sha256.Size]byte,
	expectedSignerSHA256 [sha256.Size]byte,
	currentArchitecture string,
) (Manifest, error) {
	var empty Manifest
	if len(data) == 0 || len(data) > MaxManifestBytes || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return empty, errors.New("invalid manifest envelope")
	}
	manifestDigest := sha256.Sum256(data)
	if subtle.ConstantTimeCompare(manifestDigest[:], expectedManifestSHA256[:]) != 1 {
		return empty, errors.New("manifest identity mismatch")
	}
	if len(approvedPublicKey) != ed25519.PublicKeySize {
		return empty, errors.New("invalid approved signer key")
	}
	signerDigest := sha256.Sum256(approvedPublicKey)
	if subtle.ConstantTimeCompare(signerDigest[:], expectedSignerSHA256[:]) != 1 {
		return empty, errors.New("approved signer identity mismatch")
	}

	lines := bytes.Split(data[:len(data)-1], []byte{'\n'})
	if len(lines) != len(manifestKeys) {
		return empty, errors.New("manifest line count mismatch")
	}
	values := make([]string, len(lines))
	for i, line := range lines {
		if len(line) == 0 {
			return empty, errors.New("empty manifest line")
		}
		prefix := manifestKeys[i] + "="
		if !bytes.HasPrefix(line, []byte(prefix)) || len(line) == len(prefix) {
			return empty, fmt.Errorf("manifest field order mismatch: %s", manifestKeys[i])
		}
		values[i] = string(line[len(prefix):])
	}
	if values[0] != ReleaseManifestFormat || values[1] != SignatureAlgorithm ||
		values[2] != TrustedStatus || values[5] != ExpectationsFormat ||
		values[10] != RuleVersion || values[14] != ArtifactFormat ||
		values[17] != ArtifactHMACVersion || values[27] != DetachedFormat ||
		values[28] != ReceiptFormat {
		return empty, errors.New("manifest fixed field mismatch")
	}

	parseUint := func(name, value string) (uint64, error) {
		if value == "" || value != "0" && (value[0] < '1' || value[0] > '9') {
			return 0, fmt.Errorf("noncanonical integer: %s", name)
		}
		for i := range len(value) {
			if value[i] < '0' || value[i] > '9' {
				return 0, fmt.Errorf("noncanonical integer: %s", name)
			}
		}
		parsed, err := strconv.ParseUint(value, 10, 64)
		if err != nil || strconv.FormatUint(parsed, 10) != value {
			return 0, fmt.Errorf("noncanonical integer: %s", name)
		}
		return parsed, nil
	}
	numericIndexes := [...]int{12, 15, 21, 22, 23, 24, 25, 26}
	numeric := make(map[int]uint64, len(numericIndexes))
	for _, index := range numericIndexes {
		parsed, err := parseUint(manifestKeys[index], values[index])
		if err != nil {
			return empty, err
		}
		numeric[index] = parsed
	}

	signature, err := base64.StdEncoding.Strict().DecodeString(values[30])
	if err != nil || len(signature) != ed25519.SignatureSize ||
		base64.StdEncoding.EncodeToString(signature) != values[30] {
		return empty, errors.New("invalid manifest signature encoding")
	}
	signedLength := len(data) - len(lines[30]) - 1
	if signedLength <= 0 || !ed25519.Verify(approvedPublicKey, data[:signedLength], signature) {
		return empty, errors.New("manifest signature denied")
	}

	manifest := Manifest{
		Architecture: values[3], RunnerSHA256: values[4], ReleaseID: values[6],
		ContractSHA256: values[7], ClassifierSHA256: values[8], VerifierSHA256: values[9],
		SourceFormat: values[11], SourceLength: numeric[12], SourceSHA256: values[13],
		ArtifactLength: numeric[15], ArtifactSHA256: values[16], ArtifactHMACKeyID: values[18],
		ArtifactHMACSHA256: values[19], EvidenceHMACKeyID: values[20], InputRows: numeric[21],
		OutputRows: numeric[22], Provable: numeric[23], Orphan: numeric[24],
		CrossTenant: numeric[25], UnprovableKey: numeric[26], ReleaseExpectationsSHA256: values[29],
	}
	if err := manifest.validate(currentArchitecture); err != nil {
		return empty, err
	}
	expectations, err := manifest.ExpectationsBytes()
	if err != nil {
		return empty, err
	}
	expectationsDigest := sha256.Sum256(expectations)
	expectedExpectationsDigest := mustDecodeLowerHex(manifest.ReleaseExpectationsSHA256)
	if subtle.ConstantTimeCompare(
		expectationsDigest[:],
		expectedExpectationsDigest[:],
	) != 1 {
		return empty, errors.New("release expectations identity mismatch")
	}
	return manifest, nil
}

func mustDecodeLowerHex(value string) [sha256.Size]byte {
	var out [sha256.Size]byte
	for i := 0; i < len(out); i++ {
		high := strings.IndexByte("0123456789abcdef", value[i*2])
		low := strings.IndexByte("0123456789abcdef", value[i*2+1])
		out[i] = byte(high<<4 | low)
	}
	return out
}

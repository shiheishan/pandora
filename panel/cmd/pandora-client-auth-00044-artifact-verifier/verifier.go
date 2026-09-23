package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/platform/clientauth/evidencecodec"
)

const (
	artifactFormat      = "pandora-device-key-classification-v1"
	artifactHMACVersion = "pandora-client-auth-00044-classification-artifact-hmac-v1"
	ruleVersion         = "pandora-client-auth-00044-classification-v1"
	detachedFormat      = "pandora-client-auth-00044-classifier-detached-v1"
	expectationsFormat  = "pandora-client-auth-00044-artifact-expectations-v1"
	receiptFormat       = "pandora-client-auth-00044-artifact-verifier-receipt-v1"
	maxSourceBytes      = 16 << 20
	maxArtifactBytes    = 64 << 20
	maxEnvelopeBytes    = 64 << 10
	maxLineBytes        = 64 << 10
	maxDERBytes         = 4096
	maxRecords          = 100000
	classProvable       = "provable"
	classOrphan         = "orphan"
	classCrossTenant    = "cross_tenant"
	classUnprovableKey  = "unprovable_key"
	provenanceProvable  = "provable"
	provenanceOrphan    = "orphan"
	provenanceCross     = "cross_tenant"
	algorithmEd25519    = "ed25519"
	algorithmP256ES256  = "p256-es256"
	unknownKeyAlgorithm = "unknown"
)

var (
	keyIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	releaseIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	lowerHex64       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type sourceRecord struct {
	TenantID       string
	ID             string
	JoinProvenance string
	KeyAlgorithm   string
	PublicKey      string
}

type sourceWire struct {
	TenantID       *string `json:"tenant_id"`
	ID             *string `json:"id"`
	JoinProvenance *string `json:"join_provenance"`
	KeyAlgorithm   *string `json:"key_algorithm"`
	PublicKey      *string `json:"public_key"`
}

type artifactRecord struct {
	TenantID          string  `json:"tenant_id"`
	ID                string  `json:"id"`
	JoinProvenance    string  `json:"join_provenance"`
	KeyAlgorithm      string  `json:"key_algorithm"`
	Classification    string  `json:"classification"`
	Reason            string  `json:"reason"`
	SourceSHA256      string  `json:"source_sha256"`
	FingerprintSHA256 *string `json:"fingerprint_sha256"`

	candidateFingerprint [sha256.Size]byte
	hasFingerprint       bool
}

type classificationArtifact struct {
	Format       string           `json:"format"`
	SourceSHA256 string           `json:"source_sha256"`
	InputRows    uint64           `json:"input_rows"`
	OutputRows   uint64           `json:"output_rows"`
	Records      []artifactRecord `json:"records"`
}

type detachedManifest struct {
	ManifestFormat      string `json:"manifest_format"`
	ArtifactHMACVersion string `json:"artifact_hmac_version"`
	ArtifactHMACKeyID   string `json:"artifact_hmac_key_id"`
	ArtifactFormat      string `json:"artifact_format"`
	SourceFormat        string `json:"source_format"`
	SourceLength        uint64 `json:"source_length"`
	ArtifactLength      uint64 `json:"artifact_length"`
	SourceSHA256        string `json:"source_sha256"`
	ArtifactSHA256      string `json:"artifact_sha256"`
	ArtifactHMACSHA256  string `json:"artifact_hmac_sha256"`
	InputRows           uint64 `json:"input_rows"`
	OutputRows          uint64 `json:"output_rows"`
	Provable            uint64 `json:"provable"`
	Orphan              uint64 `json:"orphan"`
	CrossTenant         uint64 `json:"cross_tenant"`
	UnprovableKey       uint64 `json:"unprovable_key"`
}

type releaseExpectations struct {
	ExpectationsFormat     string `json:"expectations_format"`
	ReleaseID              string `json:"release_id"`
	ContractSHA256         string `json:"contract_sha256"`
	ClassifierSHA256       string `json:"classifier_sha256"`
	VerifierSHA256         string `json:"verifier_sha256"`
	RuleVersion            string `json:"rule_version"`
	SourceFormat           string `json:"source_format"`
	SourceLength           uint64 `json:"source_length"`
	SourceSHA256           string `json:"source_sha256"`
	ArtifactFormat         string `json:"artifact_format"`
	ArtifactLength         uint64 `json:"artifact_length"`
	ArtifactSHA256         string `json:"artifact_sha256"`
	ArtifactHMACVersion    string `json:"artifact_hmac_version"`
	ArtifactHMACKeyID      string `json:"artifact_hmac_key_id"`
	ArtifactHMACSHA256     string `json:"artifact_hmac_sha256"`
	EvidenceHMACKeyID      string `json:"evidence_hmac_key_id"`
	InputRows              uint64 `json:"input_rows"`
	OutputRows             uint64 `json:"output_rows"`
	Provable               uint64 `json:"provable"`
	Orphan                 uint64 `json:"orphan"`
	CrossTenant            uint64 `json:"cross_tenant"`
	UnprovableKey          uint64 `json:"unprovable_key"`
	DetachedManifestFormat string `json:"detached_manifest_format"`
	ReceiptFormat          string `json:"receipt_format"`
}

type verifierReceipt struct {
	ReceiptFormat             string `json:"receipt_format"`
	Decision                  string `json:"decision"`
	ReleaseID                 string `json:"release_id"`
	ContractSHA256            string `json:"contract_sha256"`
	ClassifierSHA256          string `json:"classifier_sha256"`
	VerifierSHA256            string `json:"verifier_sha256"`
	RuleVersion               string `json:"rule_version"`
	SourceFormat              string `json:"source_format"`
	SourceLength              uint64 `json:"source_length"`
	SourceSHA256              string `json:"source_sha256"`
	ArtifactFormat            string `json:"artifact_format"`
	ArtifactLength            uint64 `json:"artifact_length"`
	ArtifactSHA256            string `json:"artifact_sha256"`
	ArtifactHMACVersion       string `json:"artifact_hmac_version"`
	ArtifactHMACKeyID         string `json:"artifact_hmac_key_id"`
	ArtifactHMACSHA256        string `json:"artifact_hmac_sha256"`
	EvidenceHMACKeyID         string `json:"evidence_hmac_key_id"`
	InputRows                 uint64 `json:"input_rows"`
	OutputRows                uint64 `json:"output_rows"`
	Provable                  uint64 `json:"provable"`
	Orphan                    uint64 `json:"orphan"`
	CrossTenant               uint64 `json:"cross_tenant"`
	UnprovableKey             uint64 `json:"unprovable_key"`
	DetachedManifestSHA256    string `json:"detached_manifest_sha256"`
	ReleaseExpectationsSHA256 string `json:"release_expectations_sha256"`
}

type verificationInputs struct {
	Argv          []string
	Environment   []string
	SourceFormat  string
	Source        []byte
	Artifact      []byte
	Detached      []byte
	Expectations  []byte
	ArtifactKeyID string
	ArtifactKey   []byte
	EvidenceKeyID string
	EvidenceKey   []byte
}

type classificationCounts struct {
	Provable, Orphan, CrossTenant, UnprovableKey uint64
}

func verifyAll(in verificationInputs) ([]byte, error) {
	var expectations releaseExpectations
	if err := parseCanonicalJSON(in.Expectations, &expectations); err != nil {
		return nil, fmt.Errorf("expectations: %w", err)
	}
	if err := validateExpectations(expectations, in); err != nil {
		return nil, err
	}

	var detached detachedManifest
	if err := parseCanonicalJSON(in.Detached, &detached); err != nil {
		return nil, fmt.Errorf("detached: %w", err)
	}
	if err := validateDetachedShape(detached, in); err != nil {
		return nil, err
	}

	regenerated, counts, err := regenerateArtifact(in.Source, in.SourceFormat)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	var received classificationArtifact
	if err := parseCanonicalJSON(in.Artifact, &received); err != nil {
		return nil, fmt.Errorf("artifact: %w", err)
	}
	if !hmac.Equal(regenerated, in.Artifact) {
		return nil, errors.New("artifact regeneration mismatch")
	}

	sourceDigest := sha256.Sum256(in.Source)
	artifactDigest := sha256.Sum256(in.Artifact)
	sourceHex := hex.EncodeToString(sourceDigest[:])
	artifactHex := hex.EncodeToString(artifactDigest[:])
	inputRows := uint64(len(received.Records))
	if received.InputRows != inputRows || received.OutputRows != inputRows {
		return nil, errors.New("artifact count mismatch")
	}
	if err := compareMetadata(detached, expectations, in, sourceHex, artifactHex, inputRows, counts); err != nil {
		return nil, err
	}
	if len(in.ArtifactKey) != 32 || len(in.EvidenceKey) != 32 || hmac.Equal(in.ArtifactKey, in.EvidenceKey) {
		return nil, errors.New("key material separation")
	}
	if keyMaterialExposed(in.Argv, in.Environment, in.ArtifactKey, in.EvidenceKey) {
		return nil, errors.New("key material exposure")
	}
	framed, err := evidencecodec.ComputeSeparatedArtifactDigest(in.ArtifactKeyID, in.ArtifactKey, in.EvidenceKeyID, in.EvidenceKey, artifactHMACVersion, artifactFormat, in.Artifact)
	if err != nil {
		return nil, fmt.Errorf("framing: %w", err)
	}
	framedHex := hex.EncodeToString(framed.HMAC[:])
	if !constantHexEqual(framedHex, detached.ArtifactHMACSHA256) || !constantHexEqual(framedHex, expectations.ArtifactHMACSHA256) {
		return nil, errors.New("artifact HMAC mismatch")
	}

	detachedDigest := sha256.Sum256(in.Detached)
	expectationsDigest := sha256.Sum256(in.Expectations)
	receipt := verifierReceipt{
		ReceiptFormat: receiptFormat, Decision: "VERIFIED", ReleaseID: expectations.ReleaseID,
		ContractSHA256: expectations.ContractSHA256, ClassifierSHA256: expectations.ClassifierSHA256,
		VerifierSHA256: expectations.VerifierSHA256, RuleVersion: ruleVersion,
		SourceFormat: in.SourceFormat, SourceLength: uint64(len(in.Source)), SourceSHA256: sourceHex,
		ArtifactFormat: artifactFormat, ArtifactLength: uint64(len(in.Artifact)), ArtifactSHA256: artifactHex,
		ArtifactHMACVersion: artifactHMACVersion, ArtifactHMACKeyID: in.ArtifactKeyID,
		ArtifactHMACSHA256: framedHex, EvidenceHMACKeyID: in.EvidenceKeyID,
		InputRows: inputRows, OutputRows: inputRows, Provable: counts.Provable, Orphan: counts.Orphan,
		CrossTenant: counts.CrossTenant, UnprovableKey: counts.UnprovableKey,
		DetachedManifestSHA256:    hex.EncodeToString(detachedDigest[:]),
		ReleaseExpectationsSHA256: hex.EncodeToString(expectationsDigest[:]),
	}
	return marshalCanonicalJSON(receipt)
}

func validateExpectations(e releaseExpectations, in verificationInputs) error {
	if e.ExpectationsFormat != expectationsFormat || e.RuleVersion != ruleVersion ||
		e.SourceFormat != in.SourceFormat || e.ArtifactFormat != artifactFormat ||
		e.ArtifactHMACVersion != artifactHMACVersion || e.DetachedManifestFormat != detachedFormat ||
		e.ReceiptFormat != receiptFormat || !releaseIDPattern.MatchString(e.ReleaseID) {
		return errors.New("expectations fixed field")
	}
	for _, value := range []string{e.ContractSHA256, e.ClassifierSHA256, e.VerifierSHA256, e.SourceSHA256, e.ArtifactSHA256, e.ArtifactHMACSHA256} {
		if !lowerHex64.MatchString(value) {
			return errors.New("expectations hash syntax")
		}
	}
	if !keyIDPattern.MatchString(in.ArtifactKeyID) || !keyIDPattern.MatchString(in.EvidenceKeyID) ||
		in.ArtifactKeyID == in.EvidenceKeyID || e.ArtifactHMACKeyID != in.ArtifactKeyID ||
		e.EvidenceHMACKeyID != in.EvidenceKeyID {
		return errors.New("expectations key id")
	}
	return nil
}

func validateDetachedShape(d detachedManifest, in verificationInputs) error {
	if d.ManifestFormat != detachedFormat || d.ArtifactHMACVersion != artifactHMACVersion ||
		d.ArtifactHMACKeyID != in.ArtifactKeyID || d.ArtifactFormat != artifactFormat ||
		d.SourceFormat != in.SourceFormat {
		return errors.New("detached fixed field")
	}
	for _, value := range []string{d.SourceSHA256, d.ArtifactSHA256, d.ArtifactHMACSHA256} {
		if !lowerHex64.MatchString(value) {
			return errors.New("detached hash syntax")
		}
	}
	return nil
}

func compareMetadata(d detachedManifest, e releaseExpectations, in verificationInputs, sourceHex, artifactHex string, rows uint64, c classificationCounts) error {
	if d.SourceLength != uint64(len(in.Source)) || e.SourceLength != uint64(len(in.Source)) ||
		d.ArtifactLength != uint64(len(in.Artifact)) || e.ArtifactLength != uint64(len(in.Artifact)) ||
		d.SourceSHA256 != sourceHex || e.SourceSHA256 != sourceHex ||
		d.ArtifactSHA256 != artifactHex || e.ArtifactSHA256 != artifactHex ||
		d.InputRows != rows || e.InputRows != rows || d.OutputRows != rows || e.OutputRows != rows ||
		d.Provable != c.Provable || e.Provable != c.Provable ||
		d.Orphan != c.Orphan || e.Orphan != c.Orphan ||
		d.CrossTenant != c.CrossTenant || e.CrossTenant != c.CrossTenant ||
		d.UnprovableKey != c.UnprovableKey || e.UnprovableKey != c.UnprovableKey ||
		d.ArtifactHMACSHA256 != e.ArtifactHMACSHA256 {
		return errors.New("metadata mismatch")
	}
	return nil
}

func regenerateArtifact(source []byte, format string) ([]byte, classificationCounts, error) {
	records, err := parseSource(source, format)
	if err != nil {
		return nil, classificationCounts{}, err
	}
	digest := sha256.Sum256(source)
	sourceHex := hex.EncodeToString(digest[:])
	ids := make(map[string]struct{}, len(records))
	classified := make([]artifactRecord, 0, len(records))
	for _, record := range records {
		if !canonicalUUID(record.TenantID) || !canonicalUUID(record.ID) {
			return nil, classificationCounts{}, errors.New("noncanonical UUID")
		}
		if _, ok := ids[record.ID]; ok {
			return nil, classificationCounts{}, errors.New("duplicate id")
		}
		ids[record.ID] = struct{}{}
		result := artifactRecord{TenantID: record.TenantID, ID: record.ID, JoinProvenance: record.JoinProvenance, KeyAlgorithm: normalizedAlgorithm(record.KeyAlgorithm), SourceSHA256: sourceHex}
		switch record.JoinProvenance {
		case provenanceOrphan:
			result.Classification, result.Reason = classOrphan, "tenant_user_join_orphan"
		case provenanceCross:
			result.Classification, result.Reason = classCrossTenant, "tenant_user_join_cross_tenant"
		case provenanceProvable:
			fp, reason := classifyPublicKey(record.PublicKey, record.KeyAlgorithm)
			if reason != "" {
				result.Classification, result.Reason = classUnprovableKey, reason
			} else {
				result.Classification, result.Reason = classProvable, "canonical_spki"
				result.candidateFingerprint, result.hasFingerprint = fp, true
			}
		default:
			return nil, classificationCounts{}, errors.New("invalid join provenance")
		}
		classified = append(classified, result)
	}
	downgradeFingerprintConflicts(classified)
	sort.Slice(classified, func(i, j int) bool {
		if classified[i].TenantID != classified[j].TenantID {
			return classified[i].TenantID < classified[j].TenantID
		}
		return classified[i].ID < classified[j].ID
	})
	var counts classificationCounts
	for i := range classified {
		r := &classified[i]
		switch r.Classification {
		case classProvable:
			value := hex.EncodeToString(r.candidateFingerprint[:])
			r.FingerprintSHA256 = &value
			counts.Provable++
		case classOrphan:
			counts.Orphan++
		case classCrossTenant:
			counts.CrossTenant++
		case classUnprovableKey:
			counts.UnprovableKey++
		default:
			return nil, classificationCounts{}, errors.New("classification invariant")
		}
		r.candidateFingerprint = [32]byte{}
		r.hasFingerprint = false
	}
	artifact := classificationArtifact{Format: artifactFormat, SourceSHA256: sourceHex, InputRows: uint64(len(records)), OutputRows: uint64(len(records)), Records: classified}
	out, err := marshalCanonicalJSON(artifact)
	return out, counts, err
}

func parseSource(source []byte, format string) ([]sourceRecord, error) {
	if len(source) == 0 || len(source) > maxSourceBytes || !utf8.Valid(source) || bytes.IndexByte(source, 0) >= 0 {
		return nil, errors.New("source envelope")
	}
	lines := bytes.Split(source, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	for i := range lines {
		if len(lines[i]) > 0 && lines[i][len(lines[i])-1] == '\r' {
			lines[i] = lines[i][:len(lines[i])-1]
		}
		if len(lines[i]) == 0 || len(lines[i]) > maxLineBytes {
			return nil, errors.New("source line")
		}
	}
	switch format {
	case "tsv":
		return parseTSVLines(lines)
	case "ndjson":
		return parseNDJSONLines(lines)
	default:
		return nil, errors.New("source format")
	}
}

func parseTSVLines(lines [][]byte) ([]sourceRecord, error) {
	if len(lines) < 2 || !bytes.Equal(lines[0], []byte("tenant_id\tid\tjoin_provenance\tkey_algorithm\tpublic_key")) || len(lines)-1 > maxRecords {
		return nil, errors.New("TSV shape")
	}
	result := make([]sourceRecord, 0, len(lines)-1)
	for _, line := range lines[1:] {
		fields := bytes.Split(line, []byte{'\t'})
		if len(fields) != 5 {
			return nil, errors.New("TSV field count")
		}
		result = append(result, sourceRecord{string(fields[0]), string(fields[1]), string(fields[2]), string(fields[3]), string(fields[4])})
	}
	return result, nil
}

func parseNDJSONLines(lines [][]byte) ([]sourceRecord, error) {
	if len(lines) == 0 || len(lines) > maxRecords {
		return nil, errors.New("NDJSON count")
	}
	result := make([]sourceRecord, 0, len(lines))
	for _, line := range lines {
		if err := rejectDuplicateKeys(line); err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		var wire sourceWire
		if err := decoder.Decode(&wire); err != nil {
			return nil, errors.New("NDJSON object")
		}
		if err := requireEOF(decoder); err != nil || wire.TenantID == nil || wire.ID == nil || wire.JoinProvenance == nil || wire.KeyAlgorithm == nil || wire.PublicKey == nil {
			return nil, errors.New("NDJSON exact fields")
		}
		result = append(result, sourceRecord{*wire.TenantID, *wire.ID, *wire.JoinProvenance, *wire.KeyAlgorithm, *wire.PublicKey})
	}
	return result, nil
}

func classifyPublicKey(encoded, algorithm string) ([32]byte, string) {
	der, reason := decodePublicKey(encoded)
	if reason != "" {
		return [32]byte{}, reason
	}
	return classifyDER(der, algorithm, x509.ParsePKIXPublicKey, x509.MarshalPKIXPublicKey)
}

func classifyDER(der []byte, algorithm string, parse func([]byte) (any, error), marshal func(any) ([]byte, error)) ([32]byte, string) {
	parsed, err := parse(der)
	if err != nil {
		return [32]byte{}, "invalid_spki"
	}
	remarshaled, err := marshal(parsed)
	if err != nil || !bytes.Equal(remarshaled, der) {
		return [32]byte{}, "noncanonical_spki"
	}
	switch algorithm {
	case algorithmEd25519:
		key, ok := parsed.(ed25519.PublicKey)
		if !ok || len(key) != ed25519.PublicKeySize {
			return [32]byte{}, "key_algorithm_mismatch"
		}
	case algorithmP256ES256:
		key, ok := parsed.(*ecdsa.PublicKey)
		if !ok || key.Curve != elliptic.P256() || key.X == nil || key.Y == nil || !key.Curve.IsOnCurve(key.X, key.Y) {
			return [32]byte{}, "key_algorithm_mismatch"
		}
	default:
		return [32]byte{}, "unsupported_key_algorithm"
	}
	return sha256.Sum256(der), ""
}

func decodePublicKey(encoded string) ([]byte, string) {
	var der []byte
	var err error
	switch {
	case strings.HasPrefix(encoded, "hex:"):
		payload := strings.TrimPrefix(encoded, "hex:")
		if len(payload) == 0 || len(payload) > maxDERBytes*2 || len(payload)%2 != 0 {
			return nil, "invalid_public_key_encoding"
		}
		der, err = hex.DecodeString(payload)
		if err != nil || hex.EncodeToString(der) != payload {
			return nil, "noncanonical_public_key_encoding"
		}
	case strings.HasPrefix(encoded, "base64:"):
		payload := strings.TrimPrefix(encoded, "base64:")
		if len(payload) == 0 || len(payload) > base64.StdEncoding.EncodedLen(maxDERBytes) {
			return nil, "invalid_public_key_encoding"
		}
		der, err = base64.StdEncoding.Strict().DecodeString(payload)
		if err != nil || base64.StdEncoding.EncodeToString(der) != payload {
			return nil, "noncanonical_public_key_encoding"
		}
	default:
		return nil, "public_key_encoding_prefix_required"
	}
	if len(der) == 0 || len(der) > maxDERBytes {
		return nil, "public_key_der_too_large"
	}
	return der, ""
}

func downgradeFingerprintConflicts(records []artifactRecord) {
	type key struct {
		tenant string
		fp     [32]byte
	}
	counts := make(map[key]int)
	for i := range records {
		if records[i].hasFingerprint {
			counts[key{records[i].TenantID, records[i].candidateFingerprint}]++
		}
	}
	for i := range records {
		k := key{records[i].TenantID, records[i].candidateFingerprint}
		if records[i].hasFingerprint && counts[k] > 1 {
			records[i].Classification = classUnprovableKey
			records[i].Reason = "duplicate_tenant_fingerprint"
			records[i].candidateFingerprint = [32]byte{}
			records[i].hasFingerprint = false
		}
	}
}

func normalizedAlgorithm(value string) string {
	if value == algorithmEd25519 || value == algorithmP256ES256 {
		return value
	}
	return unknownKeyAlgorithm
}

func canonicalUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i := range len(value) {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if value[i] != '-' {
				return false
			}
		} else if !((value[i] >= '0' && value[i] <= '9') || (value[i] >= 'a' && value[i] <= 'f')) {
			return false
		}
	}
	return true
}

func parseCanonicalJSON(data []byte, out any) error {
	if len(data) < 2 || data[len(data)-1] != '\n' || bytes.Count(data, []byte{'\n'}) != 1 || bytes.IndexByte(data, '\r') >= 0 || bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return errors.New("canonical envelope")
	}
	body := data[:len(data)-1]
	if err := rejectDuplicateKeys(body); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return errors.New("JSON schema")
	}
	if err := requireEOF(decoder); err != nil {
		return err
	}
	canonical, err := marshalCanonicalJSON(out)
	if err != nil || !bytes.Equal(canonical, data) {
		return errors.New("noncanonical JSON")
	}
	return nil
}

func marshalCanonicalJSON(value any) ([]byte, error) {
	out, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func(int) error
	walk = func(depth int) error {
		if depth > 16 {
			return errors.New("JSON nesting too deep")
		}
		token, err := decoder.Token()
		if err != nil {
			return errors.New("invalid JSON")
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return errors.New("invalid JSON key")
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("invalid JSON key")
				}
				if _, exists := seen[key]; exists {
					return errors.New("duplicate JSON key")
				}
				seen[key] = struct{}{}
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("invalid JSON object")
			}
		case '[':
			for decoder.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("invalid JSON array")
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		return nil
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func constantHexEqual(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

func keyMaterialExposed(argv, environment []string, keys ...[]byte) bool {
	for _, key := range keys {
		if len(key) == 0 {
			continue
		}
		hexLower := make([]byte, hex.EncodedLen(len(key)))
		hex.Encode(hexLower, key)
		hexUpper := append([]byte(nil), hexLower...)
		for i, value := range hexUpper {
			if value >= 'a' && value <= 'f' {
				hexUpper[i] = value - ('a' - 'A')
			}
		}
		std := make([]byte, base64.StdEncoding.EncodedLen(len(key)))
		base64.StdEncoding.Encode(std, key)
		rawStd := make([]byte, base64.RawStdEncoding.EncodedLen(len(key)))
		base64.RawStdEncoding.Encode(rawStd, key)
		url := make([]byte, base64.URLEncoding.EncodedLen(len(key)))
		base64.URLEncoding.Encode(url, key)
		rawURL := make([]byte, base64.RawURLEncoding.EncodedLen(len(key)))
		base64.RawURLEncoding.Encode(rawURL, key)
		forms := [][]byte{key, hexLower, hexUpper, std, rawStd, url, rawURL}
		exposed := false
		for _, values := range [][]string{argv, environment} {
			for _, value := range values {
				for _, encoded := range forms {
					if containsStringBytes(value, encoded) {
						exposed = true
						break
					}
				}
				if exposed {
					break
				}
			}
			if exposed {
				break
			}
		}
		clear(hexLower)
		clear(hexUpper)
		clear(std)
		clear(rawStd)
		clear(url)
		clear(rawURL)
		if exposed {
			return true
		}
	}
	return false
}

func containsStringBytes(value string, needle []byte) bool {
	if len(needle) == 0 || len(needle) > len(value) {
		return false
	}
	for offset := 0; offset <= len(value)-len(needle); offset++ {
		matched := true
		for i, b := range needle {
			if value[offset+i] != b {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

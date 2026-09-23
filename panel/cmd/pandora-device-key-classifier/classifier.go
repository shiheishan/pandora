package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/platform/clientauth/evidencecodec"
)

const (
	artifactFormat         = evidencecodec.ArtifactFormat
	artifactHMACVersion    = evidencecodec.ArtifactHMACVersion
	detachedManifestFormat = "pandora-client-auth-00044-classifier-detached-v1"
	maxSourceBytes         = 16 << 20
	maxLineBytes           = 64 << 10
	maxDERBytes            = 4096
	maxRecords             = 100000
)

const (
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

type sourceRecord struct {
	TenantID       string
	ID             string
	JoinProvenance string
	KeyAlgorithm   string
	PublicKey      string
	Line           int
}

type ndjsonRecord struct {
	TenantID       *string `json:"tenant_id"`
	ID             *string `json:"id"`
	JoinProvenance *string `json:"join_provenance"`
	KeyAlgorithm   *string `json:"key_algorithm"`
	PublicKey      *string `json:"public_key"`
}

type classifiedRecord struct {
	TenantID          string  `json:"tenant_id"`
	ID                string  `json:"id"`
	JoinProvenance    string  `json:"join_provenance"`
	KeyAlgorithm      string  `json:"key_algorithm"`
	Classification    string  `json:"classification"`
	Reason            string  `json:"reason"`
	SourceSHA256      string  `json:"source_sha256"`
	FingerprintSHA256 *string `json:"fingerprint_sha256"`

	candidateFingerprint    [sha256.Size]byte
	hasCandidateFingerprint bool
}

type classificationArtifact struct {
	Format       string             `json:"format"`
	SourceSHA256 string             `json:"source_sha256"`
	InputRows    int                `json:"input_rows"`
	OutputRows   int                `json:"output_rows"`
	Records      []classifiedRecord `json:"records"`
}

type classificationSummary struct {
	SourceSHA256   string
	ArtifactSHA256 string
	InputRows      int
	OutputRows     int
	Provable       int
	Orphan         int
	CrossTenant    int
	UnprovableKey  int
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
	InputRows           int    `json:"input_rows"`
	OutputRows          int    `json:"output_rows"`
	Provable            int    `json:"provable"`
	Orphan              int    `json:"orphan"`
	CrossTenant         int    `json:"cross_tenant"`
	UnprovableKey       int    `json:"unprovable_key"`
}

type denyError struct {
	Code string
	Line int
}

func (e *denyError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line_%d:%s", e.Line, e.Code)
	}
	return e.Code
}

func deny(code string, line int) error {
	return &denyError{Code: code, Line: line}
}

func classifySource(source []byte, format string) ([]byte, classificationSummary, error) {
	var empty classificationSummary
	if len(source) == 0 {
		return nil, empty, deny("empty_source", 0)
	}
	if len(source) > maxSourceBytes {
		return nil, empty, deny("source_too_large", 0)
	}
	if !utf8.Valid(source) {
		return nil, empty, deny("source_not_utf8", 0)
	}
	if bytes.IndexByte(source, 0) >= 0 {
		return nil, empty, deny("source_contains_nul", 0)
	}

	var (
		records []sourceRecord
		err     error
	)
	switch format {
	case "tsv":
		records, err = parseTSV(source)
	case "ndjson":
		records, err = parseNDJSON(source)
	default:
		return nil, empty, deny("unsupported_format", 0)
	}
	if err != nil {
		return nil, empty, err
	}

	sourceDigest := sha256.Sum256(source)
	sourceSHA256 := hex.EncodeToString(sourceDigest[:])
	ids := make(map[string]struct{}, len(records))
	classified := make([]classifiedRecord, 0, len(records))

	for _, record := range records {
		if !canonicalUUID(record.TenantID) {
			return nil, empty, deny("tenant_id_not_canonical_uuid", record.Line)
		}
		if !canonicalUUID(record.ID) {
			return nil, empty, deny("id_not_canonical_uuid", record.Line)
		}
		if _, exists := ids[record.ID]; exists {
			return nil, empty, deny("duplicate_id", record.Line)
		}
		ids[record.ID] = struct{}{}

		result := classifiedRecord{
			TenantID:       record.TenantID,
			ID:             record.ID,
			JoinProvenance: record.JoinProvenance,
			KeyAlgorithm:   normalizedAlgorithm(record.KeyAlgorithm),
			SourceSHA256:   sourceSHA256,
		}
		switch record.JoinProvenance {
		case provenanceOrphan:
			result.Classification = classOrphan
			result.Reason = "tenant_user_join_orphan"
		case provenanceCross:
			result.Classification = classCrossTenant
			result.Reason = "tenant_user_join_cross_tenant"
		case provenanceProvable:
			fingerprint, reason := classifyPublicKey(record.PublicKey, record.KeyAlgorithm)
			if reason != "" {
				result.Classification = classUnprovableKey
				result.Reason = reason
			} else {
				result.Classification = classProvable
				result.Reason = "canonical_spki"
				result.candidateFingerprint = fingerprint
				result.hasCandidateFingerprint = true
			}
		default:
			return nil, empty, deny("invalid_join_provenance", record.Line)
		}
		classified = append(classified, result)
	}

	downgradeTenantFingerprintConflicts(classified)
	sort.Slice(classified, func(i, j int) bool {
		if classified[i].TenantID != classified[j].TenantID {
			return classified[i].TenantID < classified[j].TenantID
		}
		return classified[i].ID < classified[j].ID
	})

	summary := classificationSummary{
		SourceSHA256: sourceSHA256,
		InputRows:    len(records),
		OutputRows:   len(classified),
	}
	for i := range classified {
		record := &classified[i]
		switch record.Classification {
		case classProvable:
			fingerprint := hex.EncodeToString(record.candidateFingerprint[:])
			record.FingerprintSHA256 = &fingerprint
			summary.Provable++
		case classOrphan:
			summary.Orphan++
		case classCrossTenant:
			summary.CrossTenant++
		case classUnprovableKey:
			summary.UnprovableKey++
		}
		record.candidateFingerprint = [sha256.Size]byte{}
		record.hasCandidateFingerprint = false
	}

	artifact := classificationArtifact{
		Format:       artifactFormat,
		SourceSHA256: sourceSHA256,
		InputRows:    len(records),
		OutputRows:   len(classified),
		Records:      classified,
	}
	artifactBytes, err := json.Marshal(artifact)
	if err != nil {
		return nil, empty, deny("artifact_encoding_failed", 0)
	}
	artifactBytes = append(artifactBytes, '\n')
	artifactDigest := sha256.Sum256(artifactBytes)
	summary.ArtifactSHA256 = hex.EncodeToString(artifactDigest[:])
	return artifactBytes, summary, nil
}

func buildDetachedManifest(source []byte, sourceFormat string, artifact []byte, summary classificationSummary, artifactKeyID string, artifactKey []byte) ([]byte, error) {
	if sourceFormat != "tsv" && sourceFormat != "ndjson" {
		return nil, deny("detached_source_format_invalid", 0)
	}
	counts := [...]int{
		summary.InputRows,
		summary.OutputRows,
		summary.Provable,
		summary.Orphan,
		summary.CrossTenant,
		summary.UnprovableKey,
	}
	for _, count := range counts {
		if count < 0 || count > maxRecords {
			return nil, deny("detached_count_summary_invalid", 0)
		}
	}
	if summary.InputRows != summary.OutputRows ||
		summary.Provable+summary.Orphan+summary.CrossTenant+summary.UnprovableKey != summary.OutputRows {
		return nil, deny("detached_count_summary_invalid", 0)
	}
	sourceDigest := sha256.Sum256(source)
	if summary.SourceSHA256 != hex.EncodeToString(sourceDigest[:]) {
		return nil, deny("detached_source_summary_mismatch", 0)
	}
	digest, err := evidencecodec.ComputeArtifactDigest(
		artifactKeyID,
		artifactKey,
		artifactHMACVersion,
		artifactFormat,
		artifact,
	)
	if err != nil {
		return nil, deny("artifact_hmac_invalid", 0)
	}
	artifactSHA256 := hex.EncodeToString(digest.SHA256[:])
	if summary.ArtifactSHA256 != artifactSHA256 {
		return nil, deny("detached_artifact_summary_mismatch", 0)
	}
	detached := detachedManifest{
		ManifestFormat:      detachedManifestFormat,
		ArtifactHMACVersion: artifactHMACVersion,
		ArtifactHMACKeyID:   artifactKeyID,
		ArtifactFormat:      artifactFormat,
		SourceFormat:        sourceFormat,
		SourceLength:        uint64(len(source)),
		ArtifactLength:      digest.Length,
		SourceSHA256:        summary.SourceSHA256,
		ArtifactSHA256:      artifactSHA256,
		ArtifactHMACSHA256:  hex.EncodeToString(digest.HMAC[:]),
		InputRows:           summary.InputRows,
		OutputRows:          summary.OutputRows,
		Provable:            summary.Provable,
		Orphan:              summary.Orphan,
		CrossTenant:         summary.CrossTenant,
		UnprovableKey:       summary.UnprovableKey,
	}
	wire, err := json.Marshal(detached)
	if err != nil {
		return nil, deny("detached_encoding_failed", 0)
	}
	return append(wire, '\n'), nil
}

func downgradeTenantFingerprintConflicts(records []classifiedRecord) {
	type groupKey struct {
		TenantID    string
		Fingerprint [sha256.Size]byte
	}
	counts := make(map[groupKey]int)
	for i := range records {
		if records[i].Classification != classProvable || !records[i].hasCandidateFingerprint {
			continue
		}
		key := groupKey{
			TenantID:    records[i].TenantID,
			Fingerprint: records[i].candidateFingerprint,
		}
		counts[key]++
	}
	for i := range records {
		if records[i].Classification != classProvable || !records[i].hasCandidateFingerprint {
			continue
		}
		key := groupKey{
			TenantID:    records[i].TenantID,
			Fingerprint: records[i].candidateFingerprint,
		}
		if counts[key] > 1 {
			records[i].Classification = classUnprovableKey
			records[i].Reason = "duplicate_tenant_fingerprint"
			records[i].candidateFingerprint = [sha256.Size]byte{}
			records[i].hasCandidateFingerprint = false
		}
	}
}

func parseTSV(source []byte) ([]sourceRecord, error) {
	lines := bytes.Split(source, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) < 2 {
		return nil, deny("tsv_requires_header_and_record", 0)
	}
	for i := range lines {
		if len(lines[i]) > 0 && lines[i][len(lines[i])-1] == '\r' {
			lines[i] = lines[i][:len(lines[i])-1]
		}
		if len(lines[i]) > maxLineBytes {
			return nil, deny("line_too_large", i+1)
		}
		if len(lines[i]) == 0 {
			return nil, deny("blank_line", i+1)
		}
	}
	if !bytes.Equal(lines[0], []byte("tenant_id\tid\tjoin_provenance\tkey_algorithm\tpublic_key")) {
		return nil, deny("invalid_tsv_header", 1)
	}
	if len(lines)-1 > maxRecords {
		return nil, deny("too_many_records", 0)
	}

	records := make([]sourceRecord, 0, len(lines)-1)
	for i, line := range lines[1:] {
		fields := bytes.Split(line, []byte{'\t'})
		if len(fields) != 5 {
			return nil, deny("invalid_tsv_field_count", i+2)
		}
		records = append(records, sourceRecord{
			TenantID:       string(fields[0]),
			ID:             string(fields[1]),
			JoinProvenance: string(fields[2]),
			KeyAlgorithm:   string(fields[3]),
			PublicKey:      string(fields[4]),
			Line:           i + 2,
		})
	}
	return records, nil
}

func parseNDJSON(source []byte) ([]sourceRecord, error) {
	lines := bytes.Split(source, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil, deny("empty_source", 0)
	}
	if len(lines) > maxRecords {
		return nil, deny("too_many_records", 0)
	}

	records := make([]sourceRecord, 0, len(lines))
	for i, line := range lines {
		lineNumber := i + 1
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if len(line) == 0 {
			return nil, deny("blank_line", lineNumber)
		}
		if len(line) > maxLineBytes {
			return nil, deny("line_too_large", lineNumber)
		}
		if err := rejectDuplicateJSONKeys(line); err != nil {
			return nil, deny("invalid_ndjson_object", lineNumber)
		}

		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		var wire ndjsonRecord
		if err := decoder.Decode(&wire); err != nil {
			return nil, deny("invalid_ndjson_object", lineNumber)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return nil, deny("invalid_ndjson_object", lineNumber)
		}
		if wire.TenantID == nil || wire.ID == nil || wire.JoinProvenance == nil ||
			wire.KeyAlgorithm == nil || wire.PublicKey == nil {
			return nil, deny("missing_ndjson_field", lineNumber)
		}
		records = append(records, sourceRecord{
			TenantID:       *wire.TenantID,
			ID:             *wire.ID,
			JoinProvenance: *wire.JoinProvenance,
			KeyAlgorithm:   *wire.KeyAlgorithm,
			PublicKey:      *wire.PublicKey,
			Line:           lineNumber,
		})
	}
	return records, nil
}

func rejectDuplicateJSONKeys(line []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(line))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errors.New("not_object")
	}
	seen := make(map[string]struct{}, 5)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return errors.New("invalid_key")
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("invalid_key")
		}
		if _, exists := seen[key]; exists {
			return errors.New("duplicate_key")
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errors.New("invalid_value")
		}
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return errors.New("invalid_object_end")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("trailing_json")
	}
	return nil
}

func classifyPublicKey(encoded, algorithm string) ([sha256.Size]byte, string) {
	var empty [sha256.Size]byte
	der, reason := decodePublicKey(encoded)
	if reason != "" {
		return empty, reason
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return empty, "invalid_spki"
	}
	remarshaled, err := x509.MarshalPKIXPublicKey(parsed)
	if err != nil || !bytes.Equal(remarshaled, der) {
		return empty, "noncanonical_spki"
	}

	switch algorithm {
	case algorithmEd25519:
		publicKey, ok := parsed.(ed25519.PublicKey)
		if !ok || len(publicKey) != ed25519.PublicKeySize {
			return empty, "key_algorithm_mismatch"
		}
	case algorithmP256ES256:
		publicKey, ok := parsed.(*ecdsa.PublicKey)
		if !ok || publicKey.Curve != elliptic.P256() ||
			publicKey.X == nil || publicKey.Y == nil ||
			!publicKey.Curve.IsOnCurve(publicKey.X, publicKey.Y) {
			return empty, "key_algorithm_mismatch"
		}
	default:
		return empty, "unsupported_key_algorithm"
	}
	return sha256.Sum256(der), ""
}

func decodePublicKey(encoded string) ([]byte, string) {
	var (
		der []byte
		err error
	)
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

func canonicalUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i := 0; i < len(value); i++ {
		switch i {
		case 8, 13, 18, 23:
			if value[i] != '-' {
				return false
			}
		default:
			if !((value[i] >= '0' && value[i] <= '9') ||
				(value[i] >= 'a' && value[i] <= 'f')) {
				return false
			}
		}
	}
	compact := strings.ReplaceAll(value, "-", "")
	decoded, err := hex.DecodeString(compact)
	return err == nil && len(decoded) == 16
}

func normalizedAlgorithm(algorithm string) string {
	if algorithm == algorithmEd25519 || algorithm == algorithmP256ES256 {
		return algorithm
	}
	return unknownKeyAlgorithm
}

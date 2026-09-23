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
	"io"
	"os"
	"strings"
	"testing"
)

const (
	tenantOne         = "10000000-0000-4000-8000-000000000001"
	tenantTwo         = "20000000-0000-4000-8000-000000000002"
	deviceOne         = "30000000-0000-4000-8000-000000000001"
	deviceTwo         = "30000000-0000-4000-8000-000000000002"
	deviceThree       = "30000000-0000-4000-8000-000000000003"
	deviceFour        = "30000000-0000-4000-8000-000000000004"
	testArtifactKeyID = "artifact-2026-01"
)

var testHMACKey = bytes.Repeat([]byte{0x5a}, 32)

func testSPKIs(t *testing.T) (edDER, edDER2, p256DER []byte) {
	t.Helper()
	edPrivate := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x11}, ed25519.SeedSize))
	edDER, err := x509.MarshalPKIXPublicKey(edPrivate.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	edPrivate2 := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x22}, ed25519.SeedSize))
	edDER2, err = x509.MarshalPKIXPublicKey(edPrivate2.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	x, y := elliptic.P256().ScalarBaseMult(bytes.Repeat([]byte{0x33}, 32))
	p256DER, err = x509.MarshalPKIXPublicKey(&ecdsa.PublicKey{
		Curve: elliptic.P256(),
		X:     x,
		Y:     y,
	})
	if err != nil {
		t.Fatal(err)
	}
	return edDER, edDER2, p256DER
}

func TestClassifySourceProducesAllFrozenClasses(t *testing.T) {
	edDER, _, p256DER := testSPKIs(t)
	source := []byte(
		line(tenantOne, deviceFour, provenanceProvable, algorithmP256ES256,
			"base64:"+base64.StdEncoding.EncodeToString(p256DER)) +
			line(tenantOne, deviceOne, provenanceProvable, algorithmEd25519,
				"hex:"+hex.EncodeToString(edDER)) +
			line(tenantOne, deviceTwo, provenanceOrphan, algorithmEd25519, "hex:00") +
			line(tenantOne, deviceThree, provenanceCross, algorithmEd25519, "hex:00"),
	)

	artifactBytes, manifest, err := classifySource(source, "ndjson")
	if err != nil {
		t.Fatalf("classifySource() error = %v", err)
	}
	var artifact classificationArtifact
	if err := json.Unmarshal(artifactBytes, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.InputRows != 4 || artifact.OutputRows != 4 ||
		manifest.InputRows != 4 || manifest.OutputRows != 4 {
		t.Fatalf("row-count binding failed: artifact=%#v manifest=%#v", artifact, manifest)
	}
	if manifest.Provable != 2 || manifest.Orphan != 1 ||
		manifest.CrossTenant != 1 || manifest.UnprovableKey != 0 {
		t.Fatalf("unexpected counts: %#v", manifest)
	}
	if artifact.Records[0].ID != deviceOne || artifact.Records[3].ID != deviceFour {
		t.Fatalf("records not sorted by tenant/id: %#v", artifact.Records)
	}
	for _, record := range artifact.Records {
		if record.Classification == classProvable && record.FingerprintSHA256 == nil {
			t.Fatal("provable row missing fingerprint")
		}
		if record.Classification != classProvable && record.FingerprintSHA256 != nil {
			t.Fatal("untrusted row published a fingerprint")
		}
	}
	sourceDigest := sha256.Sum256(source)
	if artifact.SourceSHA256 != hex.EncodeToString(sourceDigest[:]) ||
		manifest.SourceSHA256 != artifact.SourceSHA256 {
		t.Fatal("exact source digest not bound")
	}
	artifactDigest := sha256.Sum256(artifactBytes)
	if manifest.ArtifactSHA256 != hex.EncodeToString(artifactDigest[:]) {
		t.Fatal("artifact digest not bound")
	}
}

func TestMalformedKeyIsRowClassificationNotBatchAbort(t *testing.T) {
	edDER, _, _ := testSPKIs(t)
	tests := []struct {
		name      string
		algorithm string
		publicKey string
		reason    string
	}{
		{"malformed spki", algorithmEd25519, "hex:00", "invalid_spki"},
		{"algorithm mismatch", algorithmP256ES256, "hex:" + hex.EncodeToString(edDER), "key_algorithm_mismatch"},
		{"unknown algorithm", "rsa", "hex:" + hex.EncodeToString(edDER), "unsupported_key_algorithm"},
		{"noncanonical encoding", algorithmEd25519, "hex:" + strings.ToUpper(hex.EncodeToString(edDER)), "noncanonical_public_key_encoding"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := []byte(line(
				tenantOne, deviceOne, provenanceProvable, test.algorithm, test.publicKey,
			))
			artifactBytes, manifest, err := classifySource(source, "ndjson")
			if err != nil {
				t.Fatalf("bad key aborted batch: %v", err)
			}
			var artifact classificationArtifact
			if err := json.Unmarshal(artifactBytes, &artifact); err != nil {
				t.Fatal(err)
			}
			if manifest.UnprovableKey != 1 ||
				artifact.Records[0].Classification != classUnprovableKey ||
				artifact.Records[0].Reason != test.reason ||
				artifact.Records[0].FingerprintSHA256 != nil {
				t.Fatalf("unexpected classification: %#v %#v", artifact.Records[0], manifest)
			}
		})
	}
}

func TestSameTenantFingerprintConflictHasNoWinner(t *testing.T) {
	edDER, _, _ := testSPKIs(t)
	key := "hex:" + hex.EncodeToString(edDER)
	source := []byte(
		line(tenantOne, deviceOne, provenanceProvable, algorithmEd25519, key) +
			line(tenantOne, deviceTwo, provenanceProvable, algorithmEd25519, key),
	)
	artifactBytes, manifest, err := classifySource(source, "ndjson")
	if err != nil {
		t.Fatal(err)
	}
	var artifact classificationArtifact
	if err := json.Unmarshal(artifactBytes, &artifact); err != nil {
		t.Fatal(err)
	}
	if manifest.Provable != 0 || manifest.UnprovableKey != 2 {
		t.Fatalf("conflict counts = %#v", manifest)
	}
	for _, record := range artifact.Records {
		if record.Classification != classUnprovableKey ||
			record.Reason != "duplicate_tenant_fingerprint" ||
			record.FingerprintSHA256 != nil {
			t.Fatalf("conflict selected a winner: %#v", record)
		}
	}
}

func TestSameFingerprintAcrossTenantsIsAllowed(t *testing.T) {
	edDER, _, _ := testSPKIs(t)
	key := "hex:" + hex.EncodeToString(edDER)
	source := []byte(
		line(tenantOne, deviceOne, provenanceProvable, algorithmEd25519, key) +
			line(tenantTwo, deviceTwo, provenanceProvable, algorithmEd25519, key),
	)
	_, manifest, err := classifySource(source, "ndjson")
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Provable != 2 || manifest.UnprovableKey != 0 {
		t.Fatalf("cross-tenant fingerprint was rejected: %#v", manifest)
	}
}

func TestStructuralErrorsAbortWithoutArtifact(t *testing.T) {
	edDER, edDER2, _ := testSPKIs(t)
	edHex := hex.EncodeToString(edDER)
	edHex2 := hex.EncodeToString(edDER2)
	tests := []struct {
		name   string
		format string
		source string
		code   string
	}{
		{
			name:   "noncanonical tenant uuid",
			format: "ndjson",
			source: line("A0000000-0000-4000-8000-000000000001", deviceOne, provenanceProvable, algorithmEd25519, "hex:"+edHex),
			code:   "tenant_id_not_canonical_uuid",
		},
		{
			name:   "noncanonical device uuid",
			format: "ndjson",
			source: line(tenantOne, "A0000000-0000-4000-8000-000000000001", provenanceProvable, algorithmEd25519, "hex:"+edHex),
			code:   "id_not_canonical_uuid",
		},
		{
			name:   "duplicate id",
			format: "ndjson",
			source: line(tenantOne, deviceOne, provenanceProvable, algorithmEd25519, "hex:"+edHex) +
				line(tenantTwo, deviceOne, provenanceProvable, algorithmEd25519, "hex:"+edHex2),
			code: "duplicate_id",
		},
		{
			name:   "unknown provenance",
			format: "ndjson",
			source: line(tenantOne, deviceOne, "unknown", algorithmEd25519, "hex:"+edHex),
			code:   "invalid_join_provenance",
		},
		{
			name:   "extra json field",
			format: "ndjson",
			source: `{"tenant_id":"` + tenantOne + `","id":"` + deviceOne +
				`","join_provenance":"provable","key_algorithm":"ed25519","public_key":"hex:` +
				edHex + `","private_key":"forbidden"}` + "\n",
			code: "invalid_ndjson_object",
		},
		{
			name:   "extra tsv field",
			format: "tsv",
			source: "tenant_id\tid\tjoin_provenance\tkey_algorithm\tpublic_key\n" +
				tenantOne + "\t" + deviceOne + "\tprovable\ted25519\thex:" + edHex + "\textra\n",
			code: "invalid_tsv_field_count",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			artifact, _, err := classifySource([]byte(test.source), test.format)
			if err == nil || !strings.Contains(err.Error(), test.code) {
				t.Fatalf("error = %v, want code %q", err, test.code)
			}
			if len(artifact) != 0 {
				t.Fatal("structural denial published artifact")
			}
		})
	}
}

func TestTSVAndManifestDoNotLeakSensitiveMapping(t *testing.T) {
	edDER, _, _ := testSPKIs(t)
	source := []byte("tenant_id\tid\tjoin_provenance\tkey_algorithm\tpublic_key\r\n" +
		tenantOne + "\t" + deviceOne + "\tprovable\ted25519\thex:" +
		hex.EncodeToString(edDER) + "\r\n")
	artifact, manifest, err := classifySource(source, "tsv")
	if err != nil {
		t.Fatal(err)
	}
	detached, err := buildDetachedManifest(source, "tsv", artifact, manifest, testArtifactKeyID, testHMACKey)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(detached, []byte(tenantOne)) || bytes.Contains(detached, []byte(deviceOne)) {
		t.Fatal("public manifest leaked raw UUID")
	}
	var parsed classificationArtifact
	if err := json.Unmarshal(artifact, &parsed); err != nil {
		t.Fatal(err)
	}
	fingerprint := *parsed.Records[0].FingerprintSHA256
	if bytes.Contains(detached, []byte(fingerprint)) {
		t.Fatal("public manifest leaked full fingerprint")
	}
	if manifest.InputRows != manifest.OutputRows {
		t.Fatal("row counts are not bound")
	}
	var parsedDetached detachedManifest
	if err := json.Unmarshal(detached, &parsedDetached); err != nil {
		t.Fatal(err)
	}
	if parsedDetached.SourceFormat != "tsv" || parsedDetached.InputRows != 1 || parsedDetached.OutputRows != 1 {
		t.Fatalf("detached manifest is not bound to the TSV result: %#v", parsedDetached)
	}
}

func TestArtifactIsDeterministic(t *testing.T) {
	edDER, _, _ := testSPKIs(t)
	source := []byte(line(
		tenantOne, deviceOne, provenanceProvable, algorithmEd25519,
		"hex:"+hex.EncodeToString(edDER),
	))
	first, firstManifest, err := classifySource(source, "ndjson")
	if err != nil {
		t.Fatal(err)
	}
	second, secondManifest, err := classifySource(source, "ndjson")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) || firstManifest != secondManifest {
		t.Fatal("classifier is not deterministic")
	}
}

func line(tenantID, id, provenance, algorithm, publicKey string) string {
	wire := struct {
		TenantID       string `json:"tenant_id"`
		ID             string `json:"id"`
		JoinProvenance string `json:"join_provenance"`
		KeyAlgorithm   string `json:"key_algorithm"`
		PublicKey      string `json:"public_key"`
	}{
		TenantID:       tenantID,
		ID:             id,
		JoinProvenance: provenance,
		KeyAlgorithm:   algorithm,
		PublicKey:      publicKey,
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		panic(err)
	}
	return string(encoded) + "\n"
}

func TestGolden20ExistingClassifierArtifact(t *testing.T) {
	source, err := os.ReadFile("testdata/golden20-source.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/golden20-artifact.json")
	if err != nil {
		t.Fatal(err)
	}
	artifact, manifest, err := classifySource(source, "ndjson")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(artifact, want) {
		t.Fatalf("artifact golden drift\n got=%q\nwant=%q", artifact, want)
	}
	if len(source) != 784 || len(artifact) != 1530 {
		t.Fatalf("Golden20 length drift: source=%d artifact=%d", len(source), len(artifact))
	}
	sourceDigest := sha256.Sum256(source)
	if got := hex.EncodeToString(sourceDigest[:]); got != "9d7957e43f1494aea7dd39c4cc2f442cc6096857008d578c55455f4588b1c1d8" || manifest.SourceSHA256 != got {
		t.Fatalf("source SHA drift: %s %#v", got, manifest)
	}
	if len(artifact) == 0 || artifact[len(artifact)-1] != '\n' || bytes.Count(artifact, []byte{'\n'}) != 1 {
		t.Fatal("artifact must have exactly one terminal LF")
	}
	digest := sha256.Sum256(artifact)
	if got := hex.EncodeToString(digest[:]); got != "4563d10078318eec79c2d42947122b9e1af311990a87e44f928fad7471befc1c" || manifest.ArtifactSHA256 != got {
		t.Fatalf("artifact SHA drift: %s %#v", got, manifest)
	}
	for _, forbidden := range [][]byte{[]byte("artifact_hmac_key_id"), []byte("artifact_hmac_sha256"), []byte("manifest_hmac_sha256"), []byte("legacy_raw_artifact_hmac_sha256")} {
		if bytes.Contains(artifact, forbidden) {
			t.Fatalf("artifact contains detached field %q", forbidden)
		}
	}

	artifactKey := make([]byte, 32)
	for i := range artifactKey {
		artifactKey[i] = byte(i)
	}
	detached, err := buildDetachedManifest(source, "ndjson", artifact, manifest, testArtifactKeyID, artifactKey)
	if err != nil {
		t.Fatal(err)
	}
	wantDetached := []byte("{\"manifest_format\":\"pandora-client-auth-00044-classifier-detached-v1\",\"artifact_hmac_version\":\"pandora-client-auth-00044-classification-artifact-hmac-v1\",\"artifact_hmac_key_id\":\"artifact-2026-01\",\"artifact_format\":\"pandora-device-key-classification-v1\",\"source_format\":\"ndjson\",\"source_length\":784,\"artifact_length\":1530,\"source_sha256\":\"9d7957e43f1494aea7dd39c4cc2f442cc6096857008d578c55455f4588b1c1d8\",\"artifact_sha256\":\"4563d10078318eec79c2d42947122b9e1af311990a87e44f928fad7471befc1c\",\"artifact_hmac_sha256\":\"17607946658785fb135d5fc8019c702c2dd3c2b8833c2e42f06c19b3c16a8606\",\"input_rows\":4,\"output_rows\":4,\"provable\":1,\"orphan\":1,\"cross_tenant\":1,\"unprovable_key\":1}\n")
	if !bytes.Equal(detached, wantDetached) {
		t.Fatalf("detached Golden20 drift\n got=%q\nwant=%q", detached, wantDetached)
	}
	if len(detached) != 671 || detached[len(detached)-1] != '\n' || bytes.Count(detached, []byte{'\n'}) != 1 {
		t.Fatalf("detached wire framing drift: len=%d", len(detached))
	}
	detachedDigest := sha256.Sum256(detached)
	if got := hex.EncodeToString(detachedDigest[:]); got != "811790829cc18e9a5d3fe9b94489e590ec006de8cc7dc530789dbeb5e6eb0c8d" {
		t.Fatalf("detached SHA drift: %s", got)
	}
	var parsed detachedManifest
	if err := json.Unmarshal(detached, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ArtifactHMACSHA256 != "17607946658785fb135d5fc8019c702c2dd3c2b8833c2e42f06c19b3c16a8606" {
		t.Fatalf("framed artifact HMAC drift: %s", parsed.ArtifactHMACSHA256)
	}
	raw := hmac.New(sha256.New, artifactKey)
	_, _ = raw.Write(artifact)
	legacyRawHMAC := hex.EncodeToString(raw.Sum(nil))
	if legacyRawHMAC != "8342372b3b1905b43676c26fffe9a95349b1ae58b8e723cf6fcd4709d169d73b" {
		t.Fatalf("legacy raw-HMAC vector drift: %s", legacyRawHMAC)
	}
	if legacyRawHMAC == parsed.ArtifactHMACSHA256 || bytes.Contains(detached, []byte(legacyRawHMAC)) {
		t.Fatal("legacy raw HMAC was emitted or accepted as framed HMAC")
	}
	for _, forbidden := range []string{
		"manifest_hmac_sha256",
		"legacy_raw_artifact_hmac_sha256",
		"evidence_hmac_key_id",
		"fingerprint_sha256",
		tenantOne,
		deviceOne,
	} {
		if bytes.Contains(detached, []byte(forbidden)) {
			t.Fatalf("detached manifest leaked forbidden value %q", forbidden)
		}
	}

	otherKey := bytes.Repeat([]byte{0x6b}, 32)
	artifact2, manifest2, err := classifySource(source, "ndjson")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(artifact, artifact2) || manifest.ArtifactSHA256 != manifest2.ArtifactSHA256 {
		t.Fatal("artifact bytes depend on HMAC key")
	}
	detached2, err := buildDetachedManifest(source, "ndjson", artifact2, manifest2, "artifact-2026-02", otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(detached, detached2) {
		t.Fatal("distinct HMAC keys produced equal detached HMAC")
	}
}

func TestDetachedManifestValidation(t *testing.T) {
	edDER, _, _ := testSPKIs(t)
	source := []byte(line(tenantOne, deviceOne, provenanceProvable, algorithmEd25519, "hex:"+hex.EncodeToString(edDER)))
	artifact, summary, err := classifySource(source, "ndjson")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := buildDetachedManifest(source, "ndjson", artifact, summary, testArtifactKeyID, testHMACKey); err != nil {
		t.Fatalf("valid detached manifest rejected: %v", err)
	}

	for name, test := range map[string]struct {
		format string
		keyID  string
		key    []byte
		code   string
	}{
		"invalid format": {format: "json", keyID: testArtifactKeyID, key: testHMACKey, code: "detached_source_format_invalid"},
		"empty key id":   {format: "ndjson", keyID: "", key: testHMACKey, code: "artifact_hmac_invalid"},
		"bad key id":     {format: "ndjson", keyID: "Bad", key: testHMACKey, code: "artifact_hmac_invalid"},
		"short key":      {format: "ndjson", keyID: testArtifactKeyID, key: bytes.Repeat([]byte{1}, 31), code: "artifact_hmac_invalid"},
		"long key":       {format: "ndjson", keyID: testArtifactKeyID, key: bytes.Repeat([]byte{1}, 33), code: "artifact_hmac_invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := buildDetachedManifest(source, test.format, artifact, summary, test.keyID, test.key)
			if err == nil || err.Error() != test.code {
				t.Fatalf("error=%v want=%s", err, test.code)
			}
		})
	}

	badSourceSummary := summary
	badSourceSummary.SourceSHA256 = strings.Repeat("0", 64)
	if _, err := buildDetachedManifest(source, "ndjson", artifact, badSourceSummary, testArtifactKeyID, testHMACKey); err == nil || err.Error() != "detached_source_summary_mismatch" {
		t.Fatalf("source-summary mismatch error=%v", err)
	}
	badArtifactSummary := summary
	badArtifactSummary.ArtifactSHA256 = strings.Repeat("0", 64)
	if _, err := buildDetachedManifest(source, "ndjson", artifact, badArtifactSummary, testArtifactKeyID, testHMACKey); err == nil || err.Error() != "detached_artifact_summary_mismatch" {
		t.Fatalf("artifact-summary mismatch error=%v", err)
	}

	badCounts := []classificationSummary{
		func() classificationSummary { value := summary; value.InputRows = -1; return value }(),
		func() classificationSummary { value := summary; value.OutputRows = maxRecords + 1; return value }(),
		func() classificationSummary { value := summary; value.InputRows++; return value }(),
		func() classificationSummary { value := summary; value.Provable++; return value }(),
	}
	for i, bad := range badCounts {
		if _, err := buildDetachedManifest(source, "ndjson", artifact, bad, testArtifactKeyID, testHMACKey); err == nil || err.Error() != "detached_count_summary_invalid" {
			t.Fatalf("bad count summary %d error=%v", i, err)
		}
	}
}

func validClassifierArgs() []string {
	return []string{
		"-format", "ndjson",
		"-artifact-dir-fd", "4",
		"-artifact-name", "devices.classified.json",
		"-artifact-hmac-key-id", testArtifactKeyID,
		"-artifact-hmac-key-fd", "3",
	}
}

func TestClassifierCLIExactGrammar(t *testing.T) {
	valid := validClassifierArgs()
	got, ok := parseClassifierCLI(valid)
	if !ok || got.Format != "ndjson" || got.ArtifactDirFD != 4 || got.ArtifactName != "devices.classified.json" || got.ArtifactHMACKeyID != testArtifactKeyID || got.ArtifactHMACKeyFD != 3 {
		t.Fatalf("valid CLI rejected or misparsed: ok=%v got=%#v", ok, got)
	}

	cases := map[string][]string{
		"legacy flag":          {"-format", "ndjson", "-hmac-key-fd", "3", "-artifact-dir-fd", "4", "-artifact-name", "devices.classified.json"},
		"reordered":            {"-artifact-dir-fd", "4", "-format", "ndjson", "-artifact-name", "devices.classified.json", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd", "3"},
		"duplicate":            {"-format", "ndjson", "-artifact-dir-fd", "4", "-artifact-name", "devices.classified.json", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-id", testArtifactKeyID},
		"equals syntax":        {"-format=ndjson", "-artifact-dir-fd", "4", "-artifact-name", "devices.classified.json", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd", "3", "extra"},
		"double dash":          {"--", "-format", "ndjson", "-artifact-dir-fd", "4", "-artifact-name", "devices.classified.json", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd"},
		"positional":           append(validClassifierArgs(), "source.ndjson"),
		"missing":              valid[:len(valid)-2],
		"empty value":          {"-format", "ndjson", "-artifact-dir-fd", "4", "-artifact-name", "", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd", "3"},
		"bad format":           {"-format", "json", "-artifact-dir-fd", "4", "-artifact-name", "devices.classified.json", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd", "3"},
		"leading zero fd":      {"-format", "ndjson", "-artifact-dir-fd", "04", "-artifact-name", "devices.classified.json", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd", "3"},
		"plus fd":              {"-format", "ndjson", "-artifact-dir-fd", "+4", "-artifact-name", "devices.classified.json", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd", "3"},
		"negative fd":          {"-format", "ndjson", "-artifact-dir-fd", "-4", "-artifact-name", "devices.classified.json", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd", "3"},
		"stdio fd":             {"-format", "ndjson", "-artifact-dir-fd", "2", "-artifact-name", "devices.classified.json", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd", "3"},
		"same fd":              {"-format", "ndjson", "-artifact-dir-fd", "4", "-artifact-name", "devices.classified.json", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd", "4"},
		"overflow fd":          {"-format", "ndjson", "-artifact-dir-fd", "18446744073709551616", "-artifact-name", "devices.classified.json", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd", "3"},
		"path artifact name":   {"-format", "ndjson", "-artifact-dir-fd", "4", "-artifact-name", "nested/devices.json", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd", "3"},
		"backslash artifact":   {"-format", "ndjson", "-artifact-dir-fd", "4", "-artifact-name", "nested\\devices.json", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd", "3"},
		"dot artifact name":    {"-format", "ndjson", "-artifact-dir-fd", "4", "-artifact-name", ".", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd", "3"},
		"dotdot artifact name": {"-format", "ndjson", "-artifact-dir-fd", "4", "-artifact-name", "..", "-artifact-hmac-key-id", testArtifactKeyID, "-artifact-hmac-key-fd", "3"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if got, ok := parseClassifierCLI(args); ok {
				t.Fatalf("invalid CLI accepted: %#v", got)
			}
		})
	}
}

func TestClassifierCLIDoesNotUseEnvironmentFallback(t *testing.T) {
	t.Setenv("PANDORA_CLASSIFIER_FORMAT", "ndjson")
	t.Setenv("PANDORA_ARTIFACT_DIR_FD", "4")
	t.Setenv("PANDORA_ARTIFACT_HMAC_KEY_ID", testArtifactKeyID)
	t.Setenv("PANDORA_ARTIFACT_HMAC_KEY_FD", "3")
	var stdout, stderr bytes.Buffer
	if code := run(nil, strings.NewReader("{}\n"), &stdout, &stderr); code != 64 {
		t.Fatalf("exit=%d want=64", code)
	}
	if stdout.Len() != 0 || stderr.String() != "classifier=DENY reason=invalid_arguments\n" {
		t.Fatalf("unexpected output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunWithPanicBoundary(t *testing.T) {
	var stderr bytes.Buffer
	code := runWithPanicBoundary(func() int {
		panic("sensitive panic detail")
	}, &stderr)
	if code != 70 {
		t.Fatalf("exit=%d want=70", code)
	}
	if stderr.String() != "classifier=DENY reason=internal_failure\n" || strings.Contains(stderr.String(), "sensitive") {
		t.Fatalf("panic boundary leaked detail or changed wire: %q", stderr.String())
	}
}

type shortWriter struct{}

func (shortWriter) Write(payload []byte) (int, error) {
	if len(payload) == 0 {
		return 0, nil
	}
	return len(payload) - 1, nil
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestWriteExact(t *testing.T) {
	payload := []byte("detached\n")
	var out bytes.Buffer
	if err := writeExact(&out, payload); err != nil || !bytes.Equal(out.Bytes(), payload) {
		t.Fatalf("exact write failed: err=%v out=%q", err, out.Bytes())
	}
	if err := writeExact(shortWriter{}, payload); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error=%v", err)
	}
	sentinel := errors.New("write failed")
	if err := writeExact(errorWriter{err: sentinel}, payload); !errors.Is(err, sentinel) {
		t.Fatalf("writer error=%v", err)
	}
}

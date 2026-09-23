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
	"io"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/clientauth/evidencecodec"
)

const (
	testTenant1        = "10000000-0000-4000-8000-000000000001"
	testTenant2        = "20000000-0000-4000-8000-000000000002"
	testDevice1        = "30000000-0000-4000-8000-000000000001"
	testDevice2        = "30000000-0000-4000-8000-000000000002"
	goldenSourceSHA    = "9d7957e43f1494aea7dd39c4cc2f442cc6096857008d578c55455f4588b1c1d8"
	goldenArtifactSHA  = "4563d10078318eec79c2d42947122b9e1af311990a87e44f928fad7471befc1c"
	goldenArtifactHMAC = "17607946658785fb135d5fc8019c702c2dd3c2b8833c2e42f06c19b3c16a8606"
)

func TestGolden20RegenerationAndFullVerification(t *testing.T) {
	in := goldenInputs(t)
	gotArtifact, counts, err := regenerateArtifact(in.Source, in.SourceFormat)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotArtifact, in.Artifact) {
		t.Fatalf("independent artifact regeneration drift\n got=%q\nwant=%q", gotArtifact, in.Artifact)
	}
	if counts != (classificationCounts{Provable: 1, Orphan: 1, CrossTenant: 1, UnprovableKey: 1}) {
		t.Fatalf("counts = %#v", counts)
	}
	assertDigest(t, in.Source, goldenSourceSHA)
	assertDigest(t, in.Artifact, goldenArtifactSHA)
	assertDigest(t, in.Detached, "811790829cc18e9a5d3fe9b94489e590ec006de8cc7dc530789dbeb5e6eb0c8d")

	receiptBytes, err := verifyAll(in)
	if err != nil {
		t.Fatal(err)
	}
	assertDigest(t, in.Expectations, "8ce4350daca5c6eea6995aba43b98d1991e1badbf649603b9b8c3cf201708bfc")
	assertDigest(t, receiptBytes, "ea1639ece7dda78cccad0fc894a299ea5830a3a684790b892570cd7e560ba152")
	var receipt verifierReceipt
	if err := parseCanonicalJSON(receiptBytes, &receipt); err != nil {
		t.Fatalf("receipt is not strict canonical JSON: %v", err)
	}
	if receipt.Decision != "VERIFIED" || receipt.ReceiptFormat != receiptFormat || receipt.SourceSHA256 != goldenSourceSHA ||
		receipt.ArtifactSHA256 != goldenArtifactSHA || receipt.ArtifactHMACSHA256 != goldenArtifactHMAC ||
		receipt.DetachedManifestSHA256 != "811790829cc18e9a5d3fe9b94489e590ec006de8cc7dc530789dbeb5e6eb0c8d" {
		t.Fatalf("receipt binding = %#v", receipt)
	}
	expectDigest := sha256.Sum256(in.Expectations)
	if receipt.ReleaseExpectationsSHA256 != hex.EncodeToString(expectDigest[:]) {
		t.Fatal("receipt does not bind exact expectations bytes")
	}
}

func TestEncodingBoundaries4096Through4099(t *testing.T) {
	tests := []struct {
		length int
		reason string
	}{
		{4096, "invalid_spki"},
		{4097, "public_key_der_too_large"},
		{4098, "public_key_der_too_large"},
		{4099, "invalid_public_key_encoding"},
	}
	for _, test := range tests {
		der := bytes.Repeat([]byte{0}, test.length)
		_, reason := classifyPublicKey("base64:"+base64.StdEncoding.EncodeToString(der), algorithmEd25519)
		if reason != test.reason {
			t.Errorf("decoded length %d reason = %q, want %q", test.length, reason, test.reason)
		}
	}
}

func TestCanonicalEd25519AndP256HexAndBase64(t *testing.T) {
	edSeed := bytes.Repeat([]byte{0x33}, ed25519.SeedSize)
	edDER, err := x509.MarshalPKIXPublicKey(ed25519.NewKeyFromSeed(edSeed).Public())
	if err != nil {
		t.Fatal(err)
	}
	curve := elliptic.P256()
	x, y := curve.ScalarBaseMult([]byte{1})
	p256DER, err := x509.MarshalPKIXPublicKey(&ecdsa.PublicKey{Curve: curve, X: x, Y: y})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, algorithm string
		der             []byte
	}{
		{"ed25519", algorithmEd25519, edDER},
		{"p256", algorithmP256ES256, p256DER},
	} {
		for _, encoded := range []string{"hex:" + hex.EncodeToString(test.der), "base64:" + base64.StdEncoding.EncodeToString(test.der)} {
			fingerprint, reason := classifyPublicKey(encoded, test.algorithm)
			if reason != "" || fingerprint != sha256.Sum256(test.der) {
				t.Fatalf("%s encoded=%q reason=%q fingerprint=%x", test.name, encoded[:8], reason, fingerprint)
			}
		}
	}
}

func TestFrozenKeyClassificationReasons(t *testing.T) {
	seed := bytes.Repeat([]byte{0x11}, ed25519.SeedSize)
	der, err := x509.MarshalPKIXPublicKey(ed25519.NewKeyFromSeed(seed).Public())
	if err != nil {
		t.Fatal(err)
	}
	hexKey := hex.EncodeToString(der)
	tests := []struct {
		name, encoded, algorithm, reason string
	}{
		{"invalid encoding", "base64:%%%%", algorithmEd25519, "noncanonical_public_key_encoding"},
		{"noncanonical encoding", "hex:" + strings.ToUpper(hexKey), algorithmEd25519, "noncanonical_public_key_encoding"},
		{"prefix required", hexKey, algorithmEd25519, "public_key_encoding_prefix_required"},
		{"invalid spki", "hex:00", algorithmEd25519, "invalid_spki"},
		{"algorithm mismatch", "hex:" + hexKey, algorithmP256ES256, "key_algorithm_mismatch"},
		{"unsupported algorithm", "hex:" + hexKey, "rsa", "unsupported_key_algorithm"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, reason := classifyPublicKey(test.encoded, test.algorithm)
			if reason != test.reason {
				t.Fatalf("reason = %q, want %q", reason, test.reason)
			}
		})
	}
	fingerprint, reason := classifyDER([]byte{0}, algorithmEd25519,
		func([]byte) (any, error) { return ed25519.PublicKey(make([]byte, 32)), nil },
		func(any) ([]byte, error) { return []byte{1}, nil })
	if reason != "noncanonical_spki" {
		t.Fatalf("injected noncanonical SPKI reason = %q", reason)
	}
	if fingerprint != ([sha256.Size]byte{}) {
		t.Fatalf("injected noncanonical SPKI leaked fingerprint %x", fingerprint)
	}
}

func TestDuplicateFingerprintDowngradesAllSameTenantOnly(t *testing.T) {
	seed := bytes.Repeat([]byte{0x22}, ed25519.SeedSize)
	der, err := x509.MarshalPKIXPublicKey(ed25519.NewKeyFromSeed(seed).Public())
	if err != nil {
		t.Fatal(err)
	}
	key := "hex:" + hex.EncodeToString(der)
	source := []byte(ndjsonLine(testTenant1, testDevice1, provenanceProvable, algorithmEd25519, key) +
		ndjsonLine(testTenant1, testDevice2, provenanceProvable, algorithmEd25519, key))
	artifactBytes, counts, err := regenerateArtifact(source, "ndjson")
	if err != nil {
		t.Fatal(err)
	}
	var artifact classificationArtifact
	if err := json.Unmarshal(artifactBytes, &artifact); err != nil {
		t.Fatal(err)
	}
	if counts.UnprovableKey != 2 || counts.Provable != 0 {
		t.Fatalf("counts = %#v", counts)
	}
	for _, record := range artifact.Records {
		if record.Reason != "duplicate_tenant_fingerprint" || record.FingerprintSHA256 != nil {
			t.Fatalf("collision record = %#v", record)
		}
	}

	crossTenant := []byte(ndjsonLine(testTenant1, testDevice1, provenanceProvable, algorithmEd25519, key) +
		ndjsonLine(testTenant2, testDevice2, provenanceProvable, algorithmEd25519, key))
	_, counts, err = regenerateArtifact(crossTenant, "ndjson")
	if err != nil || counts.Provable != 2 {
		t.Fatalf("cross-tenant fingerprint rejected: counts=%#v err=%v", counts, err)
	}
}

func TestStrictJSONSchemasRejectDuplicateUnknownMissingAndTrailing(t *testing.T) {
	in := goldenInputs(t)
	tests := []struct {
		name   string
		mutate func(*verificationInputs)
	}{
		{"artifact duplicate", func(v *verificationInputs) { v.Artifact = duplicateFirstField(v.Artifact) }},
		{"artifact unknown", func(v *verificationInputs) { v.Artifact = addUnknownField(v.Artifact) }},
		{"artifact missing", func(v *verificationInputs) { v.Artifact = removeField(v.Artifact, `"format":"`+artifactFormat+`",`) }},
		{"artifact trailing", func(v *verificationInputs) {
			v.Artifact = append(bytes.TrimSuffix(v.Artifact, []byte("\n")), []byte(" {}\n")...)
		}},
		{"artifact no LF", func(v *verificationInputs) { v.Artifact = bytes.TrimSuffix(v.Artifact, []byte("\n")) }},
		{"artifact double LF", func(v *verificationInputs) { v.Artifact = append(v.Artifact, '\n') }},
		{"artifact whitespace", func(v *verificationInputs) { v.Artifact = bytes.Replace(v.Artifact, []byte("{"), []byte("{ "), 1) }},
		{"artifact field reorder", func(v *verificationInputs) { v.Artifact = swapFirstTwoJSONFields(v.Artifact) }},
		{"artifact wrong count type", func(v *verificationInputs) {
			v.Artifact = bytes.Replace(v.Artifact, []byte(`"input_rows":4`), []byte(`"input_rows":"4"`), 1)
		}},
		{"artifact fingerprint nullability", func(v *verificationInputs) {
			v.Artifact = replaceFirstFingerprintWithNull(v.Artifact)
		}},
		{"detached duplicate", func(v *verificationInputs) { v.Detached = duplicateFirstField(v.Detached) }},
		{"detached unknown", func(v *verificationInputs) { v.Detached = addUnknownField(v.Detached) }},
		{"detached missing", func(v *verificationInputs) {
			v.Detached = removeField(v.Detached, `"manifest_format":"`+detachedFormat+`",`)
		}},
		{"detached trailing", func(v *verificationInputs) {
			v.Detached = append(bytes.TrimSuffix(v.Detached, []byte("\n")), []byte(" {}\n")...)
		}},
		{"detached field reorder", func(v *verificationInputs) { v.Detached = swapFirstTwoJSONFields(v.Detached) }},
		{"detached noncanonical number", func(v *verificationInputs) {
			v.Detached = bytes.Replace(v.Detached, []byte(`"source_length":784`), []byte(`"source_length":0784`), 1)
		}},
		{"detached uppercase hex", func(v *verificationInputs) {
			v.Detached = bytes.Replace(v.Detached, []byte(goldenSourceSHA), []byte(strings.ToUpper(goldenSourceSHA)), 1)
		}},
		{"expectations duplicate", func(v *verificationInputs) { v.Expectations = duplicateFirstField(v.Expectations) }},
		{"expectations unknown", func(v *verificationInputs) { v.Expectations = addUnknownField(v.Expectations) }},
		{"expectations missing", func(v *verificationInputs) {
			v.Expectations = removeField(v.Expectations, `"expectations_format":"`+expectationsFormat+`",`)
		}},
		{"expectations trailing", func(v *verificationInputs) {
			v.Expectations = append(bytes.TrimSuffix(v.Expectations, []byte("\n")), []byte(" {}\n")...)
		}},
		{"expectations field reorder", func(v *verificationInputs) { v.Expectations = swapFirstTwoJSONFields(v.Expectations) }},
		{"expectations uppercase identity", func(v *verificationInputs) {
			v.Expectations = bytes.Replace(v.Expectations, []byte(strings.Repeat("1", 64)), []byte(strings.Repeat("A", 64)), 1)
		}},
		{"legacy raw HMAC", func(v *verificationInputs) {
			v.Detached = addNamedField(v.Detached, `"legacy_raw_artifact_hmac_sha256":"`+strings.Repeat("0", 64)+`"`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneInputs(in)
			test.mutate(&candidate)
			if receipt, err := verifyAll(candidate); err == nil || len(receipt) != 0 {
				t.Fatalf("accepted malformed input: receipt=%q err=%v", receipt, err)
			}
		})
	}
}

func TestSourceSchemasAndStructuralDenials(t *testing.T) {
	valid := ndjsonLine(testTenant1, testDevice1, provenanceOrphan, algorithmEd25519, "hex:00")
	tests := []struct {
		name, format, source string
	}{
		{"duplicate", "ndjson", strings.Replace(valid, `"tenant_id":`, `"tenant_id":"`+testTenant1+`","tenant_id":`, 1)},
		{"unknown", "ndjson", strings.TrimSuffix(valid, "}\n") + `,"secret":"x"}` + "\n"},
		{"missing", "ndjson", strings.Replace(valid, `,"public_key":"hex:00"`, "", 1)},
		{"trailing", "ndjson", strings.TrimSuffix(valid, "\n") + " {}\n"},
		{"null", "ndjson", strings.Replace(valid, `"public_key":"hex:00"`, `"public_key":null`, 1)},
		{"nul", "ndjson", valid + "\x00"},
		{"empty line", "ndjson", "\n"},
		{"intermediate blank", "ndjson", valid + "\n" + valid},
		{"multi final LF", "ndjson", valid + "\n"},
		{"bad UUID", "ndjson", strings.Replace(valid, testTenant1, "A"+testTenant1[1:], 1)},
		{"duplicate id", "ndjson", valid + valid},
		{"bad provenance", "ndjson", strings.Replace(valid, provenanceOrphan, "unknown", 1)},
		{"TSV header", "tsv", "tenant\tid\n" + testTenant1 + "\t" + testDevice1 + "\n"},
		{"TSV extra", "tsv", "tenant_id\tid\tjoin_provenance\tkey_algorithm\tpublic_key\n" + testTenant1 + "\t" + testDevice1 + "\torphan\ted25519\thex:00\textra\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if artifact, _, err := regenerateArtifact([]byte(test.source), test.format); err == nil || len(artifact) != 0 {
				t.Fatalf("artifact=%q err=%v", artifact, err)
			}
		})
	}
	for _, test := range []struct {
		name   string
		source []byte
	}{
		{"empty", nil},
		{"invalid UTF-8", []byte{0xff, '\n'}},
		{"oversize source", bytes.Repeat([]byte{'x'}, maxSourceBytes+1)},
		{"oversize line", append(bytes.Repeat([]byte{'x'}, maxLineBytes+1), '\n')},
		{"too many records", bytes.Repeat([]byte("{}\n"), maxRecords+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if artifact, _, err := regenerateArtifact(test.source, "ndjson"); err == nil || len(artifact) != 0 {
				t.Fatalf("artifact=%q err=%v", artifact, err)
			}
		})
	}
}

func TestSourceExactBytesCRLFAndFinalLFChangeArtifactBinding(t *testing.T) {
	line := strings.TrimSuffix(ndjsonLine(testTenant1, testDevice1, provenanceOrphan, algorithmEd25519, "hex:00"), "\n")
	variants := [][]byte{[]byte(line), []byte(line + "\n"), []byte(line + "\r\n")}
	seen := map[string]bool{}
	for _, source := range variants {
		artifact, counts, err := regenerateArtifact(source, "ndjson")
		if err != nil || counts.Orphan != 1 {
			t.Fatalf("source=%q counts=%#v err=%v", source[len(source)-min(len(source), 2):], counts, err)
		}
		digest := sha256.Sum256(artifact)
		seen[hex.EncodeToString(digest[:])] = true
	}
	if len(seen) != len(variants) {
		t.Fatalf("exact source bytes did not produce distinct artifact bindings: %v", seen)
	}
}

func TestTSVAndFlexibleNDJSONInputProduceCanonicalArtifacts(t *testing.T) {
	tsv := []byte("tenant_id\tid\tjoin_provenance\tkey_algorithm\tpublic_key\r\n" +
		testTenant1 + "\t" + testDevice1 + "\torphan\ted25519\thex:00\r\n")
	artifact, counts, err := regenerateArtifact(tsv, "tsv")
	if err != nil || counts.Orphan != 1 || !bytes.HasSuffix(artifact, []byte("\n")) {
		t.Fatalf("TSV artifact=%q counts=%#v err=%v", artifact, counts, err)
	}
	ndjson := []byte(` { "public_key" : "hex:00", "key_algorithm" : "ed25519", "join_provenance" : "orphan", "id" : "` +
		testDevice1 + `", "tenant_id" : "` + testTenant1 + `" }`)
	artifact, counts, err = regenerateArtifact(ndjson, "ndjson")
	if err != nil || counts.Orphan != 1 || !bytes.HasSuffix(artifact, []byte("\n")) {
		t.Fatalf("NDJSON artifact=%q counts=%#v err=%v", artifact, counts, err)
	}
}

func TestVerificationDeniesBindingAndKeyFailures(t *testing.T) {
	base := goldenInputs(t)
	tests := []struct {
		name   string
		mutate func(*verificationInputs)
	}{
		{"source mismatch", func(v *verificationInputs) { v.Source[0] ^= 1 }},
		{"artifact mismatch", func(v *verificationInputs) { v.Artifact[1] ^= 1 }},
		{"detached metadata", func(v *verificationInputs) { rewriteDetached(t, v, func(d *detachedManifest) { d.SourceLength++ }) }},
		{"expectations metadata", func(v *verificationInputs) {
			rewriteExpectations(t, v, func(e *releaseExpectations) { e.OutputRows++ })
		}},
		{"artifact format drift", func(v *verificationInputs) {
			rewriteDetached(t, v, func(d *detachedManifest) { d.ArtifactFormat = "legacy" })
		}},
		{"HMAC version drift", func(v *verificationInputs) {
			rewriteExpectations(t, v, func(e *releaseExpectations) { e.ArtifactHMACVersion = "legacy" })
		}},
		{"HMAC mismatch", func(v *verificationInputs) {
			rewriteDetached(t, v, func(d *detachedManifest) { d.ArtifactHMACSHA256 = strings.Repeat("0", 64) })
			rewriteExpectations(t, v, func(e *releaseExpectations) { e.ArtifactHMACSHA256 = strings.Repeat("0", 64) })
		}},
		{"same key id", func(v *verificationInputs) {
			v.EvidenceKeyID = v.ArtifactKeyID
			rewriteExpectations(t, v, func(e *releaseExpectations) { e.EvidenceHMACKeyID = v.EvidenceKeyID })
		}},
		{"same key material", func(v *verificationInputs) { v.EvidenceKey = append([]byte(nil), v.ArtifactKey...) }},
		{"swapped key material", func(v *verificationInputs) { v.ArtifactKey, v.EvidenceKey = v.EvidenceKey, v.ArtifactKey }},
		{"raw key in argv", func(v *verificationInputs) { v.Argv = []string{"prefix" + string(v.ArtifactKey) + "suffix"} }},
		{"hex key in environment", func(v *verificationInputs) {
			v.Environment = []string{"LEAK=" + hex.EncodeToString(v.EvidenceKey)}
		}},
		{"base64 key in environment", func(v *verificationInputs) {
			v.Environment = []string{"LEAK=" + base64.StdEncoding.EncodeToString(v.ArtifactKey)}
		}},
		{"short artifact key", func(v *verificationInputs) { v.ArtifactKey = v.ArtifactKey[:31] }},
		{"invalid key id", func(v *verificationInputs) {
			v.ArtifactKeyID = "Bad"
			rewriteDetached(t, v, func(d *detachedManifest) { d.ArtifactHMACKeyID = v.ArtifactKeyID })
			rewriteExpectations(t, v, func(e *releaseExpectations) { e.ArtifactHMACKeyID = v.ArtifactKeyID })
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneInputs(base)
			test.mutate(&candidate)
			if receipt, err := verifyAll(candidate); err == nil || receipt != nil {
				t.Fatalf("receipt=%q err=%v", receipt, err)
			}
		})
	}
}

func TestFramingPrimitiveVectorsAndSeparation(t *testing.T) {
	artifactKey := make([]byte, 32)
	for i := range artifactKey {
		artifactKey[i] = byte(i)
	}
	evidenceKey := bytes.Repeat([]byte{0xa5}, 32)
	for _, test := range []struct {
		artifact []byte
		want     string
	}{
		{nil, "1a8a9a2c4ee75fd679467f61edd8fd00918dcb2548af0fd6ec8c85bae2bf559f"},
		{[]byte("{}\n"), "dedbfc2835f734833e5cfc83a7bb190df0e18a35f96c4e81bf786383bffd6eba"},
	} {
		digest, err := evidencecodec.ComputeSeparatedArtifactDigest("artifact-2026-01", artifactKey, "evidence-2026-01", evidenceKey, artifactHMACVersion, artifactFormat, test.artifact)
		if err != nil || hex.EncodeToString(digest.HMAC[:]) != test.want {
			t.Fatalf("artifact=%q digest=%x err=%v", test.artifact, digest.HMAC, err)
		}
	}
	if _, err := evidencecodec.ComputeSeparatedArtifactDigest("same", artifactKey, "same", evidenceKey, artifactHMACVersion, artifactFormat, nil); err == nil {
		t.Fatal("same key IDs accepted")
	}
	if _, err := evidencecodec.ComputeSeparatedArtifactDigest("artifact-2026-01", artifactKey, "evidence-2026-01", artifactKey, artifactHMACVersion, artifactFormat, nil); err == nil {
		t.Fatal("same key material accepted")
	}
}

func TestStrictJSONDepthLimit(t *testing.T) {
	deep := bytes.Repeat([]byte{'['}, 18)
	deep = append(deep, '0')
	deep = append(deep, bytes.Repeat([]byte{']'}, 18)...)
	if err := rejectDuplicateKeys(deep); err == nil {
		t.Fatal("deep JSON nesting accepted")
	}
}

func TestCLIExactGrammarAndFailureMapping(t *testing.T) {
	valid := validArgs()
	config, err := parseCLI(valid)
	if err != nil || config.SourceFD != 3 || config.EvidenceKeyFD != 8 {
		t.Fatalf("valid CLI: config=%#v err=%v", config, err)
	}
	tests := [][]string{
		valid[:len(valid)-2],
		append(append([]string(nil), valid...), "positional", "x"),
		append([]string{"-source-format=ndjson"}, valid[2:]...),
		withArg(valid, 2, "-source-format"),
		withArg(valid, 3, "2"),
		withArg(valid, 5, "3"),
		withArg(valid, 3, "+3"),
		withArg(valid, 3, "03"),
		withArg(valid, 3, " 3"),
		withArg(valid, 3, "３"),
		withArg(valid, 3, "9223372036854775808"),
		withArg(valid, 0, "--source-format"),
		withArg(valid, 0, "--"),
		swapFirstTwoPairs(valid),
	}
	for i, args := range tests {
		if _, err := parseCLI(args); err == nil {
			t.Errorf("case %d accepted args %#v", i, args)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"bad"}, &stdout, &stderr); code != exitUsage || stdout.Len() != 0 || stderr.String() != "verifier=DENY reason=invalid_arguments\n" {
		t.Fatalf("usage mapping: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if runtime.GOOS != "linux" {
		stdout.Reset()
		stderr.Reset()
		if code := run(valid, &stdout, &stderr); code != exitInternal || stdout.Len() != 0 || stderr.String() != "verifier=DENY reason=internal_failure\n" {
			t.Fatalf("non-linux mapping: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	}
}

func TestWriteFullMapsShortWrite(t *testing.T) {
	if !errors.Is(writeFull(shortWriter{}, []byte("receipt\n")), io.ErrShortWrite) {
		t.Fatal("short stdout write was not detected")
	}
}

func FuzzParseSource(f *testing.F) {
	f.Add([]byte(ndjsonLine(testTenant1, testDevice1, provenanceOrphan, algorithmEd25519, "hex:00")), "ndjson")
	f.Add([]byte("tenant_id\tid\tjoin_provenance\tkey_algorithm\tpublic_key\n"+testTenant1+"\t"+testDevice1+"\torphan\ted25519\thex:00\n"), "tsv")
	f.Fuzz(func(t *testing.T, source []byte, format string) {
		_, _, _ = regenerateArtifact(source, format)
	})
}

func FuzzStrictArtifactParser(f *testing.F) {
	artifact, err := os.ReadFile("../pandora-device-key-classifier/testdata/golden20-artifact.json")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(artifact)
	f.Fuzz(func(t *testing.T, data []byte) {
		var value classificationArtifact
		_ = parseCanonicalJSON(data, &value)
	})
}

func FuzzStrictDetachedParser(f *testing.F) {
	in := goldenInputsForFuzz(f)
	f.Add(in.Detached)
	f.Fuzz(func(t *testing.T, data []byte) {
		var value detachedManifest
		_ = parseCanonicalJSON(data, &value)
	})
}

func goldenInputsForFuzz(f *testing.F) verificationInputs {
	f.Helper()
	source, err := os.ReadFile("../pandora-device-key-classifier/testdata/golden20-source.ndjson")
	if err != nil {
		f.Fatal(err)
	}
	artifact, err := os.ReadFile("../pandora-device-key-classifier/testdata/golden20-artifact.json")
	if err != nil {
		f.Fatal(err)
	}
	artifactKey := make([]byte, 32)
	for i := range artifactKey {
		artifactKey[i] = byte(i)
	}
	detached, err := marshalCanonicalJSON(detachedManifest{
		ManifestFormat: detachedFormat, ArtifactHMACVersion: artifactHMACVersion,
		ArtifactHMACKeyID: "artifact-2026-01", ArtifactFormat: artifactFormat,
		SourceFormat: "ndjson", SourceLength: 784, ArtifactLength: 1530,
		SourceSHA256: goldenSourceSHA, ArtifactSHA256: goldenArtifactSHA,
		ArtifactHMACSHA256: goldenArtifactHMAC, InputRows: 4, OutputRows: 4,
		Provable: 1, Orphan: 1, CrossTenant: 1, UnprovableKey: 1,
	})
	if err != nil {
		f.Fatal(err)
	}
	return verificationInputs{SourceFormat: "ndjson", Source: source, Artifact: artifact, Detached: detached,
		ArtifactKeyID: "artifact-2026-01", ArtifactKey: artifactKey,
		EvidenceKeyID: "evidence-2026-01", EvidenceKey: bytes.Repeat([]byte{0xa5}, 32)}
}

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func goldenInputs(t *testing.T) verificationInputs {
	t.Helper()
	source, err := os.ReadFile("../pandora-device-key-classifier/testdata/golden20-source.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := os.ReadFile("../pandora-device-key-classifier/testdata/golden20-artifact.json")
	if err != nil {
		t.Fatal(err)
	}
	artifactKey := make([]byte, 32)
	for i := range artifactKey {
		artifactKey[i] = byte(i)
	}
	evidenceKey := bytes.Repeat([]byte{0xa5}, 32)
	detached := detachedManifest{
		ManifestFormat: detachedFormat, ArtifactHMACVersion: artifactHMACVersion, ArtifactHMACKeyID: "artifact-2026-01",
		ArtifactFormat: artifactFormat, SourceFormat: "ndjson", SourceLength: 784, ArtifactLength: 1530,
		SourceSHA256: goldenSourceSHA, ArtifactSHA256: goldenArtifactSHA, ArtifactHMACSHA256: goldenArtifactHMAC,
		InputRows: 4, OutputRows: 4, Provable: 1, Orphan: 1, CrossTenant: 1, UnprovableKey: 1,
	}
	detachedBytes, err := marshalCanonicalJSON(detached)
	if err != nil {
		t.Fatal(err)
	}
	expectations := releaseExpectations{
		ExpectationsFormat: expectationsFormat, ReleaseID: "release-2026-07-31",
		ContractSHA256: strings.Repeat("1", 64), ClassifierSHA256: strings.Repeat("2", 64), VerifierSHA256: strings.Repeat("3", 64),
		RuleVersion: ruleVersion, SourceFormat: "ndjson", SourceLength: 784, SourceSHA256: goldenSourceSHA,
		ArtifactFormat: artifactFormat, ArtifactLength: 1530, ArtifactSHA256: goldenArtifactSHA,
		ArtifactHMACVersion: artifactHMACVersion, ArtifactHMACKeyID: "artifact-2026-01", ArtifactHMACSHA256: goldenArtifactHMAC,
		EvidenceHMACKeyID: "evidence-2026-01", InputRows: 4, OutputRows: 4, Provable: 1, Orphan: 1, CrossTenant: 1, UnprovableKey: 1,
		DetachedManifestFormat: detachedFormat, ReceiptFormat: receiptFormat,
	}
	expectationBytes, err := marshalCanonicalJSON(expectations)
	if err != nil {
		t.Fatal(err)
	}
	return verificationInputs{
		SourceFormat: "ndjson", Source: source, Artifact: artifact, Detached: detachedBytes, Expectations: expectationBytes,
		ArtifactKeyID: "artifact-2026-01", ArtifactKey: artifactKey, EvidenceKeyID: "evidence-2026-01", EvidenceKey: evidenceKey,
	}
}

func cloneInputs(in verificationInputs) verificationInputs {
	in.Source = append([]byte(nil), in.Source...)
	in.Artifact = append([]byte(nil), in.Artifact...)
	in.Detached = append([]byte(nil), in.Detached...)
	in.Expectations = append([]byte(nil), in.Expectations...)
	in.ArtifactKey = append([]byte(nil), in.ArtifactKey...)
	in.EvidenceKey = append([]byte(nil), in.EvidenceKey...)
	return in
}

func rewriteDetached(t *testing.T, in *verificationInputs, mutate func(*detachedManifest)) {
	t.Helper()
	var value detachedManifest
	if err := json.Unmarshal(in.Detached, &value); err != nil {
		t.Fatal(err)
	}
	mutate(&value)
	in.Detached, _ = marshalCanonicalJSON(value)
}

func rewriteExpectations(t *testing.T, in *verificationInputs, mutate func(*releaseExpectations)) {
	t.Helper()
	var value releaseExpectations
	if err := json.Unmarshal(in.Expectations, &value); err != nil {
		t.Fatal(err)
	}
	mutate(&value)
	in.Expectations, _ = marshalCanonicalJSON(value)
}

func duplicateFirstField(data []byte) []byte {
	comma := bytes.IndexByte(data, ',')
	if comma < 0 {
		panic("object has no first field delimiter")
	}
	out := append([]byte{'{'}, data[1:comma+1]...)
	return append(out, data[1:]...)
}

func addUnknownField(data []byte) []byte { return addNamedField(data, `"unknown":0`) }

func addNamedField(data []byte, field string) []byte {
	return append([]byte("{"+field+","), data[1:]...)
}

func removeField(data []byte, field string) []byte {
	return bytes.Replace(data, []byte(field), nil, 1)
}

func swapFirstTwoJSONFields(data []byte) []byte {
	first := bytes.IndexByte(data, ',')
	if first < 0 {
		return data
	}
	secondRelative := bytes.IndexByte(data[first+1:], ',')
	if secondRelative < 0 {
		return data
	}
	second := first + 1 + secondRelative
	out := make([]byte, 0, len(data))
	out = append(out, '{')
	out = append(out, data[first+1:second]...)
	out = append(out, ',')
	out = append(out, data[1:first]...)
	out = append(out, data[second:]...)
	return out
}

func replaceFirstFingerprintWithNull(data []byte) []byte {
	marker := []byte(`"fingerprint_sha256":"`)
	start := bytes.Index(data, marker)
	if start < 0 {
		return data
	}
	valueStart := start + len(marker)
	endRelative := bytes.IndexByte(data[valueStart:], '"')
	if endRelative < 0 {
		return data
	}
	end := valueStart + endRelative + 1
	out := append([]byte(nil), data[:start]...)
	out = append(out, []byte(`"fingerprint_sha256":null`)...)
	out = append(out, data[end:]...)
	return out
}

func ndjsonLine(tenant, id, provenance, algorithm, publicKey string) string {
	wire := struct {
		TenantID       string `json:"tenant_id"`
		ID             string `json:"id"`
		JoinProvenance string `json:"join_provenance"`
		KeyAlgorithm   string `json:"key_algorithm"`
		PublicKey      string `json:"public_key"`
	}{tenant, id, provenance, algorithm, publicKey}
	encoded, err := json.Marshal(wire)
	if err != nil {
		panic(err)
	}
	return string(encoded) + "\n"
}

func assertDigest(t *testing.T, value []byte, want string) {
	t.Helper()
	digest := sha256.Sum256(value)
	if got := hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("digest = %s, want %s", got, want)
	}
}

func validArgs() []string {
	return []string{
		"-source-format", "ndjson", "-source-fd", "3", "-artifact-fd", "4",
		"-detached-manifest-fd", "5", "-release-expectations-fd", "6",
		"-artifact-hmac-key-id", "artifact-2026-01", "-artifact-hmac-key-fd", "7",
		"-evidence-hmac-key-id", "evidence-2026-01", "-evidence-hmac-key-fd", "8",
	}
}

func withArg(args []string, index int, value string) []string {
	out := append([]string(nil), args...)
	out[index] = value
	return out
}

func swapFirstTwoPairs(args []string) []string {
	out := append([]string(nil), args...)
	copy(out[0:2], args[2:4])
	copy(out[2:4], args[0:2])
	return out
}

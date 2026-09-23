package ca44runner

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func testManifest(t *testing.T) (Manifest, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	seed := sha256.Sum256([]byte("ca44-root-runner-policy-test-signer"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	manifest := Manifest{
		Architecture:              "amd64",
		RunnerSHA256:              strings.Repeat("1", 64),
		ReleaseID:                 "release-2026-07-31",
		ContractSHA256:            ContractSHA256,
		ClassifierSHA256:          strings.Repeat("2", 64),
		VerifierSHA256:            strings.Repeat("3", 64),
		SourceFormat:              "ndjson",
		SourceLength:              784,
		SourceSHA256:              strings.Repeat("4", 64),
		ArtifactLength:            1530,
		ArtifactSHA256:            strings.Repeat("5", 64),
		ArtifactHMACKeyID:         "artifact-2026-01",
		ArtifactHMACSHA256:        strings.Repeat("6", 64),
		EvidenceHMACKeyID:         "evidence-2026-01",
		InputRows:                 20,
		OutputRows:                20,
		Provable:                  5,
		Orphan:                    5,
		CrossTenant:               5,
		UnprovableKey:             5,
		ReleaseExpectationsSHA256: strings.Repeat("0", 64),
	}
	expectations, err := manifest.ExpectationsBytes()
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(expectations)
	manifest.ReleaseExpectationsSHA256 = hex.EncodeToString(digest[:])
	return manifest, publicKey, privateKey
}

func signedManifestBytes(t *testing.T, manifest Manifest, privateKey ed25519.PrivateKey) []byte {
	t.Helper()
	values := []string{
		ReleaseManifestFormat, SignatureAlgorithm, TrustedStatus, manifest.Architecture,
		manifest.RunnerSHA256, ExpectationsFormat, manifest.ReleaseID, manifest.ContractSHA256,
		manifest.ClassifierSHA256, manifest.VerifierSHA256, RuleVersion, manifest.SourceFormat,
		strconv.FormatUint(manifest.SourceLength, 10), manifest.SourceSHA256, ArtifactFormat,
		strconv.FormatUint(manifest.ArtifactLength, 10), manifest.ArtifactSHA256,
		ArtifactHMACVersion, manifest.ArtifactHMACKeyID, manifest.ArtifactHMACSHA256,
		manifest.EvidenceHMACKeyID, strconv.FormatUint(manifest.InputRows, 10),
		strconv.FormatUint(manifest.OutputRows, 10), strconv.FormatUint(manifest.Provable, 10),
		strconv.FormatUint(manifest.Orphan, 10), strconv.FormatUint(manifest.CrossTenant, 10),
		strconv.FormatUint(manifest.UnprovableKey, 10),
		DetachedFormat, ReceiptFormat, manifest.ReleaseExpectationsSHA256,
	}
	var payload bytes.Buffer
	for i, value := range values {
		payload.WriteString(manifestKeys[i])
		payload.WriteByte('=')
		payload.WriteString(value)
		payload.WriteByte('\n')
	}
	signature := ed25519.Sign(privateKey, payload.Bytes())
	payload.WriteString("signature_b64=")
	payload.WriteString(base64.StdEncoding.EncodeToString(signature))
	payload.WriteByte('\n')
	return payload.Bytes()
}

func parseFixture(t *testing.T, data []byte, publicKey ed25519.PublicKey, arch string) (Manifest, error) {
	t.Helper()
	manifestDigest := sha256.Sum256(data)
	signerDigest := sha256.Sum256(publicKey)
	return ParseAndVerifyManifest(data, publicKey, manifestDigest, signerDigest, arch)
}

func TestParseAndVerifyManifestAndCanonicalDerivations(t *testing.T) {
	manifest, publicKey, privateKey := testManifest(t)
	data := signedManifestBytes(t, manifest, privateKey)
	parsed, err := parseFixture(t, data, publicKey, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if parsed != manifest {
		t.Fatalf("manifest mismatch\n got=%+v\nwant=%+v", parsed, manifest)
	}
	expectations, err := parsed.ExpectationsBytes()
	if err != nil {
		t.Fatal(err)
	}
	if digest := sha256.Sum256(expectations); hex.EncodeToString(digest[:]) != parsed.ReleaseExpectationsSHA256 {
		t.Fatal("expectations digest mismatch")
	}
	detached, err := parsed.DetachedBytes()
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := parsed.ExpectedReceiptBytes()
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string][]byte{"expectations": expectations, "detached": detached, "receipt": receipt} {
		if len(value) == 0 || value[len(value)-1] != '\n' || bytes.Count(value, []byte{'\n'}) != 1 {
			t.Fatalf("%s is not one-line canonical JSON", name)
		}
	}
	if !bytes.Contains(receipt, []byte(`"decision":"VERIFIED"`)) {
		t.Fatal("receipt decision missing")
	}
	expectedExpectations := fmt.Sprintf(
		`{"expectations_format":"%s","release_id":"release-2026-07-31","contract_sha256":"%s","classifier_sha256":"%s","verifier_sha256":"%s","rule_version":"%s","source_format":"ndjson","source_length":784,"source_sha256":"%s","artifact_format":"%s","artifact_length":1530,"artifact_sha256":"%s","artifact_hmac_version":"%s","artifact_hmac_key_id":"artifact-2026-01","artifact_hmac_sha256":"%s","evidence_hmac_key_id":"evidence-2026-01","input_rows":20,"output_rows":20,"provable":5,"orphan":5,"cross_tenant":5,"unprovable_key":5,"detached_manifest_format":"%s","receipt_format":"%s"}`+"\n",
		ExpectationsFormat, ContractSHA256, strings.Repeat("2", 64), strings.Repeat("3", 64),
		RuleVersion, strings.Repeat("4", 64), ArtifactFormat, strings.Repeat("5", 64),
		ArtifactHMACVersion, strings.Repeat("6", 64), DetachedFormat, ReceiptFormat,
	)
	if string(expectations) != expectedExpectations {
		t.Fatalf("expectations wire drift\n got=%s\nwant=%s", expectations, expectedExpectations)
	}
	expectedDetached := fmt.Sprintf(
		`{"manifest_format":"%s","artifact_hmac_version":"%s","artifact_hmac_key_id":"artifact-2026-01","artifact_format":"%s","source_format":"ndjson","source_length":784,"artifact_length":1530,"source_sha256":"%s","artifact_sha256":"%s","artifact_hmac_sha256":"%s","input_rows":20,"output_rows":20,"provable":5,"orphan":5,"cross_tenant":5,"unprovable_key":5}`+"\n",
		DetachedFormat, ArtifactHMACVersion, ArtifactFormat, strings.Repeat("4", 64),
		strings.Repeat("5", 64), strings.Repeat("6", 64),
	)
	if string(detached) != expectedDetached {
		t.Fatalf("detached wire drift\n got=%s\nwant=%s", detached, expectedDetached)
	}
	detachedDigest := sha256.Sum256(detached)
	expectationsDigest := sha256.Sum256(expectations)
	expectedReceipt := fmt.Sprintf(
		`{"receipt_format":"%s","decision":"VERIFIED","release_id":"release-2026-07-31","contract_sha256":"%s","classifier_sha256":"%s","verifier_sha256":"%s","rule_version":"%s","source_format":"ndjson","source_length":784,"source_sha256":"%s","artifact_format":"%s","artifact_length":1530,"artifact_sha256":"%s","artifact_hmac_version":"%s","artifact_hmac_key_id":"artifact-2026-01","artifact_hmac_sha256":"%s","evidence_hmac_key_id":"evidence-2026-01","input_rows":20,"output_rows":20,"provable":5,"orphan":5,"cross_tenant":5,"unprovable_key":5,"detached_manifest_sha256":"%s","release_expectations_sha256":"%s"}`+"\n",
		ReceiptFormat, ContractSHA256, strings.Repeat("2", 64), strings.Repeat("3", 64),
		RuleVersion, strings.Repeat("4", 64), ArtifactFormat, strings.Repeat("5", 64),
		ArtifactHMACVersion, strings.Repeat("6", 64), hex.EncodeToString(detachedDigest[:]),
		hex.EncodeToString(expectationsDigest[:]),
	)
	if string(receipt) != expectedReceipt {
		t.Fatalf("receipt wire drift\n got=%s\nwant=%s", receipt, expectedReceipt)
	}
}

func TestManifestRejectsEnvelopeIdentitySignatureAndCanonicalDrift(t *testing.T) {
	manifest, publicKey, privateKey := testManifest(t)
	good := signedManifestBytes(t, manifest, privateKey)
	tests := map[string][]byte{
		"missing final LF": good[:len(good)-1],
		"CR":               bytes.Replace(good, []byte("\n"), []byte("\r\n"), 1),
		"NUL":              append(append([]byte(nil), good...), 0),
		"BOM":              append([]byte{0xef, 0xbb, 0xbf}, good...),
		"extra line":       append(append([]byte(nil), good...), []byte("unknown=value\n")...),
		"reordered":        bytes.Replace(good, []byte("format="), []byte("status="), 1),
		"leading integer":  bytes.Replace(good, []byte("source_length=784"), []byte("source_length=0784"), 1),
		"uppercase hash":   bytes.Replace(good, []byte(strings.Repeat("1", 64)), []byte(strings.Repeat("A", 64)), 1),
		"tampered":         bytes.Replace(good, []byte("source_format=ndjson"), []byte("source_format=tsv"), 1),
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			digest := sha256.Sum256(value)
			signer := sha256.Sum256(publicKey)
			if _, err := ParseAndVerifyManifest(value, publicKey, digest, signer, "amd64"); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
	goodDigest := sha256.Sum256(good)
	badDigest := goodDigest
	badDigest[0] ^= 0xff
	signerDigest := sha256.Sum256(publicKey)
	if _, err := ParseAndVerifyManifest(good, publicKey, badDigest, signerDigest, "amd64"); err == nil {
		t.Fatal("manifest SHA mismatch accepted")
	}
	badSigner := signerDigest
	badSigner[0] ^= 0xff
	if _, err := ParseAndVerifyManifest(good, publicKey, goodDigest, badSigner, "amd64"); err == nil {
		t.Fatal("signer SHA mismatch accepted")
	}
	if _, err := parseFixture(t, good, publicKey, "arm64"); err == nil {
		t.Fatal("architecture mismatch accepted")
	}
}

func TestManifestRejectsCountKeyAndExpectationsDrift(t *testing.T) {
	manifest, publicKey, privateKey := testManifest(t)
	manifest.EvidenceHMACKeyID = manifest.ArtifactHMACKeyID
	data := signedManifestBytes(t, manifest, privateKey)
	if _, err := parseFixture(t, data, publicKey, "amd64"); err == nil {
		t.Fatal("equal key ids accepted")
	}
	manifest, publicKey, privateKey = testManifest(t)
	manifest.Provable = 6
	data = signedManifestBytes(t, manifest, privateKey)
	if _, err := parseFixture(t, data, publicKey, "amd64"); err == nil {
		t.Fatal("count mismatch accepted")
	}
	manifest, publicKey, privateKey = testManifest(t)
	manifest.Provable = ^uint64(0)
	manifest.Orphan = 1
	manifest.CrossTenant = 0
	manifest.UnprovableKey = 0
	manifest.InputRows = 1
	manifest.OutputRows = 1
	data = signedManifestBytes(t, manifest, privateKey)
	if _, err := parseFixture(t, data, publicKey, "amd64"); err == nil {
		t.Fatal("count overflow accepted")
	}
	manifest, publicKey, privateKey = testManifest(t)
	manifest.ReleaseExpectationsSHA256 = strings.Repeat("7", 64)
	data = signedManifestBytes(t, manifest, privateKey)
	if _, err := parseFixture(t, data, publicKey, "amd64"); err == nil {
		t.Fatal("expectations identity mismatch accepted")
	}
}

func TestManifestRejectsResourceLimitDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"source oversize", func(m *Manifest) { m.SourceLength = MaxSourceBytes + 1 }},
		{"artifact oversize", func(m *Manifest) { m.ArtifactLength = MaxArtifactBytes + 1 }},
		{"record oversize", func(m *Manifest) {
			m.InputRows, m.OutputRows, m.Provable = MaxRecords+1, MaxRecords+1, MaxRecords+1
			m.Orphan, m.CrossTenant, m.UnprovableKey = 0, 0, 0
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest, publicKey, privateKey := testManifest(t)
			test.mutate(&manifest)
			data := signedManifestBytes(t, manifest, privateKey)
			if _, err := parseFixture(t, data, publicKey, "amd64"); err == nil {
				t.Fatal("resource limit drift accepted")
			}
		})
	}
}

func TestManifestResourceLimitBoundaries(t *testing.T) {
	manifest, _, _ := testManifest(t)
	manifest.SourceLength = MaxSourceBytes
	manifest.ArtifactLength = MaxArtifactBytes
	manifest.InputRows = MaxRecords
	manifest.OutputRows = MaxRecords
	manifest.Provable = MaxRecords
	manifest.Orphan = 0
	manifest.CrossTenant = 0
	manifest.UnprovableKey = 0
	if err := manifest.validate("amd64"); err != nil {
		t.Fatalf("exact resource maxima rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"zero source", func(m *Manifest) { m.SourceLength = 0 }},
		{"zero artifact", func(m *Manifest) { m.ArtifactLength = 0 }},
		{"zero records", func(m *Manifest) {
			m.InputRows, m.OutputRows = 0, 0
			m.Provable, m.Orphan, m.CrossTenant, m.UnprovableKey = 0, 0, 0, 0
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := manifest
			test.mutate(&candidate)
			if err := candidate.validate("amd64"); err == nil {
				t.Fatal("zero resource boundary accepted")
			}
		})
	}
}

func TestSeparatedKeys(t *testing.T) {
	left := bytes.Repeat([]byte{1}, 32)
	right := bytes.Repeat([]byte{2}, 32)
	if err := ValidateSeparatedKeys(left, right); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSeparatedKeys(left, append([]byte(nil), left...)); err == nil {
		t.Fatal("equal keys accepted")
	}
	if err := ValidateSeparatedKeys(left[:31], right); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestBoundedCaptureDrainsAndMatchesExactly(t *testing.T) {
	capture := NewBoundedCapture(4)
	if n, err := capture.Write([]byte("abcdef")); err != nil || n != 6 {
		t.Fatalf("write=%d err=%v", n, err)
	}
	data, total, overflow := capture.Snapshot()
	if string(data) != "abcd" || total != 6 || !overflow {
		t.Fatalf("data=%q total=%d overflow=%v", data, total, overflow)
	}
	if err := capture.MatchExact([]byte("abcd")); err == nil {
		t.Fatal("overflow capture matched")
	}
	exact := NewBoundedCapture(4)
	_, _ = exact.Write([]byte("ab"))
	_, _ = exact.Write([]byte("cd"))
	if err := exact.MatchExact([]byte("abcd")); err != nil {
		t.Fatal(err)
	}
}

func TestFailureTaxonomyIsSanitized(t *testing.T) {
	tests := []struct {
		kind FailureKind
		code int
		line string
	}{
		{FailureUsage, 64, "invalid_arguments"},
		{FailureInternal, 70, "internal_failure"},
		{FailureIO, 74, "io_failure"},
		{FailureTrust, 78, "trust_denied"},
		{FailureClassifier, 78, "classifier_denied"},
		{FailureVerifier, 78, "verifier_denied"},
		{FailureInterrupted, 130, "interrupted"},
	}
	for _, test := range tests {
		failure := &Failure{Kind: test.kind, Stage: StageManifest, Err: bytes.ErrTooLarge}
		if failure.ExitCode() != test.code || failure.SanitizedLine() != "root_runner=DENY stage=manifest reason="+test.line+"\n" {
			t.Fatalf("kind=%d code=%d line=%q", test.kind, failure.ExitCode(), failure.SanitizedLine())
		}
		if strings.Contains(failure.SanitizedLine(), failure.Err.Error()) {
			t.Fatal("raw error leaked")
		}
	}
	interrupted := &Failure{Kind: FailureInterrupted, Stage: StageClassifier, Signal: 15}
	if interrupted.ExitCode() != 143 {
		t.Fatalf("signal exit=%d", interrupted.ExitCode())
	}
	injected := &Failure{
		Kind:  FailureTrust,
		Stage: FailureStage("manifest\nsecret=leak"),
		Err:   bytes.ErrTooLarge,
	}
	if got := injected.SanitizedLine(); got != "root_runner=DENY stage=bootstrap reason=trust_denied\n" {
		t.Fatalf("unsafe stage was not normalized: %q", got)
	}
	if strings.Contains(injected.SanitizedLine(), "secret") || bytes.Count([]byte(injected.SanitizedLine()), []byte{'\n'}) != 1 {
		t.Fatal("stage injection leaked into sanitized line")
	}
}

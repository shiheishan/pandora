package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCLIRejectsArgumentsAndMalformedManifest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"unexpected"}, strings.NewReader(""), &stdout, &stderr); code != 64 || stdout.Len() != 0 {
		t.Fatalf("argument rejection code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(nil, strings.NewReader("{}\n"), &stdout, &stderr); code != exitDenied || stdout.Len() != 0 || stderr.String() != "client_auth_00042_manifest_verifier=DENY reason=verification_failed\n" {
		t.Fatalf("verification rejection code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestCLIRejectsOversizeInput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	input := strings.NewReader(strings.Repeat("x", 16<<20+1))
	if code := run(nil, input, &stdout, &stderr); code != exitDenied || stdout.Len() != 0 {
		t.Fatalf("oversize code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

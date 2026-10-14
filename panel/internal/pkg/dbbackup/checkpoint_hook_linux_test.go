//go:build linux

package dbbackup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExecuteTrustedCheckpointHookUsesVerifiedFD(t *testing.T) {
	dir := secureTempDir(t)
	hookPath := filepath.Join(dir, "replicate-checkpoint")
	want := "AEPB-CHECKPOINT-RECEIPT-V1 0000000000000000000000000000000000000000000000000000000000000000\n"
	script := "#!/bin/sh\nprintf '%s' '" + want + "'\n"
	if err := os.WriteFile(hookPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	hook, err := os.Open(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	defer hook.Close()
	got, err := executeTrustedCheckpointHook(context.Background(), hook, "/tmp/checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("receipt=%q want=%q", got, want)
	}
}

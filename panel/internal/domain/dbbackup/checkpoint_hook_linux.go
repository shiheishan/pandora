//go:build linux

package dbbackup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
)

type checkpointReceiptWriter struct {
	buffer bytes.Buffer
	limit  int
}

func (w *checkpointReceiptWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.buffer.Len() {
		return 0, errors.New("独立检查点回执过大")
	}
	return w.buffer.Write(p)
}

func executeTrustedCheckpointHook(ctx context.Context, hook *os.File, checkpointPath string) (string, error) {
	receipt := &checkpointReceiptWriter{limit: 256}
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3", checkpointPath)
	cmd.Dir = "/"
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.ExtraFiles = []*os.File{hook}
	cmd.Stdin = nil
	cmd.Stdout = receipt
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return receipt.buffer.String(), nil
}

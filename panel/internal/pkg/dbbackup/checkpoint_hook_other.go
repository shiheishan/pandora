//go:build !linux

package dbbackup

import (
	"context"
	"errors"
	"os"
)

func executeTrustedCheckpointHook(context.Context, *os.File, string) (string, error) {
	return "", errors.New("独立检查点复制 Hook 只支持 Linux")
}

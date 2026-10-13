//go:build !linux

package dbbackup

import "os"

func openPathReadOnly(name string) (*os.File, error) { return os.Open(name) } //nolint:gosec // 调用方先核过父目录属主与权限

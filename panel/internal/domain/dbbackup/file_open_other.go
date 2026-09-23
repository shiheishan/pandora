//go:build !linux

package dbbackup

import "os"

func openPathReadOnly(name string) (*os.File, error) { return os.Open(name) }

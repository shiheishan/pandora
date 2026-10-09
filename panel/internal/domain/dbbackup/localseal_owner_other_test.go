//go:build !linux

package dbbackup

import "testing"

// 非 Linux 上不核属主
func sealTestTrustCurrentUser(*testing.T) {}

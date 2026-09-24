//go:build !linux

// [INPUT]: 无
// [OUTPUT]: 对外提供测试辅助 trustCurrentUserAsSecureOwner 的空实现
// [POS]: dbbackup 属主校验在非 Linux 上的测试桩，与 file_owner_linux_test.go 成对；非 Linux 的属主校验本就是空实现
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package dbbackup

import "testing"

func trustCurrentUserAsSecureOwner(*testing.T) {}

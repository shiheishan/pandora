//go:build linux

package dbbackup

import (
	"os"
	"testing"
)

// Linux 上私密文件要求 root 所有：测试以当前用户跑时把可信属主临时换成当前用户（与 file_owner_linux_test.go 同法）
func sealTestTrustCurrentUser(t *testing.T) { withSecureFileOwner(t, uint32(os.Geteuid())) }

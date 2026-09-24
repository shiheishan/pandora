//go:build linux

// [INPUT]: 依赖 file_owner_linux.go 的 secureFileOwnerUID/requireSecureFileOwner，依赖 config.go 的 readPrivateFile
// [OUTPUT]: 对外提供测试辅助 trustCurrentUserAsSecureOwner/withSecureFileOwner，及非 root 属主被拒的反例测试
// [POS]: dbbackup 属主校验的 Linux 测试面，secure_tempdir_test.go 经它让非 root 的 CI 也能走完私密路径校验
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package dbbackup

import (
	"os"
	"path/filepath"
	"testing"
)

// trustCurrentUserAsSecureOwner 让本测试内「可信属主」等于运行测试的用户。
//
// 生产只信 root；CI（GitHub runner）与开发机以普通用户跑测试，临时文件
// 归该用户所有，不换掉可信属主，所有私密路径在属主这一关就被拒，
// 后面的权限位、硬链接、符号链接校验根本走不到——既让正向用例全红，
// 也让反向用例「因错误的理由通过」。
func trustCurrentUserAsSecureOwner(t *testing.T) {
	t.Helper()
	withSecureFileOwner(t, uint32(os.Geteuid()))
}

// withSecureFileOwner 与 withCheckpointHookRoot 同理：改包级变量、
// t.Cleanup 复原，所以用到它的用例不能标 t.Parallel()。
func withSecureFileOwner(t *testing.T, uid uint32) {
	t.Helper()
	previous := secureFileOwnerUID
	secureFileOwnerUID = uid
	t.Cleanup(func() { secureFileOwnerUID = previous })
}

func TestSecurePathOwnedByNonRootIsRejected(t *testing.T) {
	if secureFileOwnerUID != 0 {
		t.Fatalf("生产默认的可信属主必须是 root，实际为 uid %d", secureFileOwnerUID)
	}
	// 不用 secureTempDir：它会把当前用户设为可信属主，这里要的恰恰是生产默认值。
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := uint32(os.Geteuid())
	if owner == 0 {
		// 以 root 跑时新建文件本就归 root，改给 nobody 才构成「非 root 所有」的反例。
		const nobody = 65534
		for _, path := range []string{dir, secret} {
			if err := os.Chown(path, nobody, nobody); err != nil {
				t.Fatal(err)
			}
		}
		owner = nobody
	}

	info, err := os.Stat(secret)
	if err != nil {
		t.Fatal(err)
	}
	if err := requireSecureFileOwner(info); err == nil {
		t.Fatalf("uid %d 所有的私密文件通过了属主校验", owner)
	}
	if _, err := readPrivateFile(secret, 32); err == nil || err.Error() != "私密路径必须由 root 所有" {
		t.Fatalf("uid %d 所有的私密路径未因属主被拒: %v", owner, err)
	}

	// 对照：只把可信属主换成实际属主，其余一概不动，同一文件立即可读——
	// 证明上面的拒绝来自属主校验，而不是权限位或路径的其他问题。
	withSecureFileOwner(t, owner)
	raw, err := readPrivateFile(secret, 32)
	if err != nil || string(raw) != "secret" {
		t.Fatalf("可信属主下读取失败: raw=%q err=%v", raw, err)
	}
}

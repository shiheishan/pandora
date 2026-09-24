//go:build linux

// [INPUT]: 依赖 syscall 的 Stat_t（属主 UID、硬链接数），依赖 checkpoint_hook.go 的 checkpointHookRoot
// [OUTPUT]: 对外提供 RequireRootRuntime；包内提供私密路径的属主、权限位、单链接、父目录解析校验
// [POS]: dbbackup 私密文件校验的 Linux 实现，被 config.go/manifest.go 的 openSecureRegular 调用，与 file_owner_other.go 的空实现成对
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package dbbackup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// secureFileOwnerUID 是私密路径唯一可信的属主，生产上恒为 root。
//
// 做成包级变量而非字面量 0，只为让测试在非 root 的 Linux 上把「可信属主」
// 换成当前用户（见 file_owner_linux_test.go），从而真正走到后面的权限位、
// 硬链接、符号链接等校验。它不经任何配置、环境变量或导出接口暴露，
// 生产二进制里没有代码会改写它。
var secureFileOwnerUID uint32 = 0

func requireSecureFileOwner(info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != secureFileOwnerUID {
		return errors.New("私密路径必须由 root 所有")
	}
	return nil
}

func requireSecureParentMode(info fs.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("私密文件父目录必须是受保护目录")
	}
	return nil
}

func requireSecureFileMode(info fs.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("私密文件必须是非共享的普通文件")
	}
	return nil
}

func requireSecureResolvedParent(parent string) error {
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(parent) {
		return errors.New("私密文件父目录不能经过符号链接")
	}
	return nil
}

func validateCheckpointHookPath(path string) error {
	if filepath.Clean(filepath.Dir(path)) != checkpointHookRoot {
		return errors.New("独立检查点复制 Hook 必须位于固定受保护目录")
	}
	return nil
}

func requireSecureSingleLink(info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("私密文件必须不存在额外硬链接")
	}
	return nil
}

func requireRootRuntime() error {
	if os.Geteuid() != 0 {
		return errors.New("WebDAV 备份上传器必须由 root 运行")
	}
	return nil
}

func RequireRootRuntime() error { return requireRootRuntime() }

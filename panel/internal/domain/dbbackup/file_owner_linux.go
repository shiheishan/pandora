//go:build linux

package dbbackup

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func requireSecureFileOwner(info fs.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
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

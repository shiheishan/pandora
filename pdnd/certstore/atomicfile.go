package certstore

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// tempPrefix 是原子写盘的临时文件前缀，清理时据此识别崩溃遗留。
const tempPrefix = ".certstore-"

// writeFileAtomic 与 pdnd/panel 的 writeFileAtomic 同一做法：
// 同目录临时文件 → 设权限 → 写入 → fsync 文件 → rename → fsync 目录。
// 父目录必须已存在（由调用方按 0700 建好），这里不顺手建目录，免得权限被 umask 带偏。
func writeFileAtomic(path string, body []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, tempPrefix+"*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	ok = true
	return nil
}

// syncDir 把目录项（rename、新建子目录）持久化到盘上。
// Windows 不支持对目录句柄 fsync，只在那里放过权限错误，与 pdnd/panel 一致。
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !(runtime.GOOS == "windows" && os.IsPermission(err)) {
		return err
	}
	return nil
}

// ensurePrivateDir 保证 root 下的 dir（含中间各层）都是 0700 的真实目录；root 本身只要求存在，
// 不改它和它上级的权限。路径上出现符号链接或普通文件即拒绝，防止托管目录被引到别处。
// 新建目录后 fsync 父目录，保证目录项落盘。
func ensurePrivateDir(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("certstore: %s is outside %s", dir, root)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		parent := cur
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		switch {
		case err == nil:
			if !info.IsDir() {
				return fmt.Errorf("certstore: %s is not a plain directory", cur)
			}
		case os.IsNotExist(err):
			if err := os.Mkdir(cur, 0o700); err != nil {
				return err
			}
			if err := syncDir(parent); err != nil {
				return err
			}
		default:
			return err
		}
		if err := os.Chmod(cur, 0o700); err != nil {
			return err
		}
	}
	return nil
}

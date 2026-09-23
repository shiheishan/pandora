//go:build linux

package dbbackup

import (
	"errors"
	"os"
	"syscall"
)

func acquireCheckpointLock(f *os.File) error {
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("已有 WebDAV 备份上传任务在运行")
	}
	return nil
}

func releaseCheckpointLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

func replaceCheckpoint(temp, final string) error {
	if err := os.Rename(temp, final); err != nil {
		return errors.New("原子发布备份清单检查点失败")
	}
	return nil
}

func syncCheckpointDirectory(parent string) error {
	dir, err := os.Open(parent)
	if err != nil {
		return errors.New("打开备份清单状态目录失败")
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return errors.New("同步备份清单状态目录失败")
	}
	return nil
}

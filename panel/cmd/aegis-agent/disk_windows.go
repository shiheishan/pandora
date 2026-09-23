//go:build windows

package main

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

func diskUsage(path string) (totalGB, usedGB int) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return 0, 0
	}
	name, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return 0, 0
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(name, &available, &total, &free); err != nil {
		return 0, 0
	}
	const gb = 1 << 30
	return int(total / gb), int((total - available) / gb)
}

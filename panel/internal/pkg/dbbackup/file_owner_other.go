//go:build !linux

package dbbackup

import (
	"errors"
	"io/fs"
)

func requireSecureFileOwner(fs.FileInfo) error { return nil }

func requireSecureSingleLink(fs.FileInfo) error { return nil }

func requireSecureParentMode(fs.FileInfo) error { return nil }

func requireSecureFileMode(fs.FileInfo) error { return nil }

func requireSecureResolvedParent(string) error { return nil }

func validateCheckpointHookPath(string) error { return nil }

func requireRootRuntime() error { return errors.New("WebDAV 备份上传器只支持 Linux") }

func RequireRootRuntime() error { return requireRootRuntime() }

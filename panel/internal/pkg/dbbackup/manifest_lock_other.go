//go:build !linux

package dbbackup

import "os"

func acquireCheckpointLock(*os.File) error { return nil }

func releaseCheckpointLock(*os.File) error { return nil }

func replaceCheckpoint(temp, final string) error {
	_ = os.Remove(final)
	return os.Rename(temp, final)
}

func syncCheckpointDirectory(string) error { return nil }

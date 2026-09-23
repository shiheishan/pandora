//go:build !linux

package ca42runner

import (
	"crypto/sha256"
	"os"
)

func openFixedTrustedDirectory(string) (*os.File, error)      { return nil, errUnsupportedPlatform }
func openTrustedChildDirectory(int, string) (*os.File, error) { return nil, errUnsupportedPlatform }
func readRootOwnedRegularAt(int, string, int64, *[sha256.Size]byte) ([]byte, [sha256.Size]byte, error) {
	return nil, [sha256.Size]byte{}, errUnsupportedPlatform
}
func runningExecutableSHA256() ([sha256.Size]byte, error) {
	return [sha256.Size]byte{}, errUnsupportedPlatform
}

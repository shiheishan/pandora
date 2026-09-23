//go:build !linux

package main

import "errors"

func readRootOwnedSecretFD(int) ([]byte, error) {
	return nil, errors.New("linux_required")
}

func writeRootOwnedArtifact(int, string, []byte) error {
	return errors.New("linux_required")
}

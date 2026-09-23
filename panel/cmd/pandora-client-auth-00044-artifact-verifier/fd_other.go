//go:build !linux

package main

import "errors"

func executePlatform(cliConfig) ([]byte, error) {
	return nil, internalFailure(errors.New("linux-only verifier"))
}

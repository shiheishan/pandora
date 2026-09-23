//go:build !linux || (!amd64 && !arm64)

package ca42credential

import (
	"errors"
	"time"
)

type CommitmentKeyFD struct{}

func LoadCommitmentKeyFD(string) (*CommitmentKeyFD, error) {
	return nil, errors.New("credential commitment kernel keyring unsupported")
}

func (*CommitmentKeyFD) VerifyCredential([]byte, Descriptor, time.Time) error {
	return errors.New("credential commitment kernel keyring unsupported")
}

func (*CommitmentKeyFD) Close() error { return nil }

//go:build !linux

package ca42storage

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"time"
)

func VerifyFD(context.Context, BoundEntry, *os.File, time.Time) error {
	return errors.New("fs-verity verification requires native Linux")
}

func MeasureVerity(*os.File) ([sha256.Size]byte, error) {
	return [sha256.Size]byte{}, errors.New("fs-verity measurement requires native Linux")
}

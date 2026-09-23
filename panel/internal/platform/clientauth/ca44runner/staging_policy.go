package ca44runner

import (
	"crypto/rand"
	"errors"
	"io"
	"path"
	"strings"
)

const (
	runPrefix     = "run."
	runTokenBytes = 16
)

func splitRelativeLinux(value string) ([]string, error) {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") ||
		strings.ContainsRune(value, 0) || path.Clean(value) != value {
		return nil, errors.New("relative path is not canonical")
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if !validComponent(part) {
			return nil, errors.New("relative path component is invalid")
		}
	}
	return parts, nil
}

func validComponent(value string) bool {
	return value != "" && value != "." && value != ".." &&
		!strings.ContainsAny(value, "/\\") && !strings.ContainsRune(value, 0)
}

func randomRunName() (string, error) {
	value := make([]byte, runTokenBytes)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	const hexDigits = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for i, b := range value {
		encoded[i*2], encoded[i*2+1] = hexDigits[b>>4], hexDigits[b&0xf]
	}
	return runPrefix + string(encoded), nil
}

func validRunName(name string) bool {
	if len(name) != len(runPrefix)+runTokenBytes*2 || !strings.HasPrefix(name, runPrefix) {
		return false
	}
	for _, ch := range name[len(runPrefix):] {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}

func syncVerifiedExistingBundle(bundleFD, publishFD int, syncFn func(...int) error) error {
	if bundleFD < 0 || publishFD < 0 || syncFn == nil {
		return errors.New("invalid existing bundle durability check")
	}
	if err := syncFn(bundleFD, publishFD); err != nil {
		return errors.New("existing bundle durability check failed")
	}
	return nil
}

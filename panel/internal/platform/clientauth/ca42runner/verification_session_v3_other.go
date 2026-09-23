//go:build !linux || (!amd64 && !arm64)

package ca42runner

import (
	"context"
	"errors"
)

type V3VerificationSession struct{}

func OpenProductionV3VerificationSession(context.Context, string) (*V3VerificationSession, error) {
	return nil, errors.New("CA42 v3 production verification session unsupported")
}

func (*V3VerificationSession) Verify(context.Context) error {
	return errors.New("CA42 v3 production verification session unsupported")
}

func (*V3VerificationSession) Close() error { return nil }

//go:build !linux || (linux && !amd64 && !arm64)

package ca42runner

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
)

func RunReadOnlyVerification(context.Context, Command, ca42authority.RootKeyset, time.Time) (VerifiedBundle, error) {
	return VerifiedBundle{}, errUnsupportedPlatform
}

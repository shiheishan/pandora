//go:build linux && (amd64 || arm64)

package ca42runner

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
)

// RunReadOnlyVerification verifies only the authority/release/execution-plan/
// trust-capsule/external-manifest contract and computes the next ledger record
// in memory. It does not yet retain and verify every executable/evidence/dump
// artifact named by those contracts, and therefore is not an execution
// authorization gate. It never touches Docker, PostgreSQL, the durable
// authority ledger, or production.
func RunReadOnlyVerification(ctx context.Context, command Command, roots ca42authority.RootKeyset, now time.Time) (VerifiedBundle, error) {
	session, err := OpenVerificationSession(ctx, command, roots, now)
	if err != nil {
		return VerifiedBundle{}, err
	}
	defer session.Close()
	return session.Bundle()
}

// Kept local to avoid exporting release parser sizing policy through runner API.
func ca42releaseMaxBytes() int64 { return 64 << 10 }

func ca42executionMaxBytes() int64 { return 64 << 10 }

func ca42capsuleMaxBytes() int64 { return 64 << 10 }

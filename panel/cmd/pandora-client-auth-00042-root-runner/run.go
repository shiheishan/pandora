package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42runner"
)

const (
	exitUsage       = 64
	exitUnavailable = 78
)

type rootLoader func() (ca42authority.RootKeyset, error)
type readOnlyVerifier func(context.Context, ca42runner.Command, ca42authority.RootKeyset, time.Time) (ca42runner.VerifiedBundle, error)

func runCLI(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return runCLIWith(ctx, args, stdout, stderr, ca42runner.CompiledRootKeyset,
		ca42runner.RunReadOnlyVerification, time.Now().UTC())
}

func runCLIWith(ctx context.Context, args []string, stdout, stderr io.Writer,
	loadRoots rootLoader, verify readOnlyVerifier, now time.Time) int {
	command, err := ca42runner.ParseCLI(args)
	if err != nil {
		_, _ = io.WriteString(stderr, "root_runner=DENY stage=bootstrap reason=invalid_arguments\n")
		return exitUsage
	}
	roots, err := loadRoots()
	if err != nil {
		_, _ = io.WriteString(stderr, "root_runner=DENY stage=bootstrap reason=authority_roots_unprovisioned\n")
		return exitUnavailable
	}
	verified, err := verify(ctx, command, roots, now)
	if err != nil {
		_, _ = io.WriteString(stderr, "root_runner=DENY stage=verification reason=trust_denied\n")
		return exitUnavailable
	}
	_, _ = fmt.Fprintf(stdout,
		"root_runner=VERIFIED attempt_id=%s authority_epoch=%d authority_sequence=%d execution=NOT_RUN authorization=NONE\n",
		verified.Authority.AttemptID, verified.Authority.AuthorityEpoch, verified.Authority.AuthoritySequence)
	return exitUnavailable
}

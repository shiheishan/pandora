package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42runner"
)

func TestRunCLIFailsClosedAndNeverReportsExecution(t *testing.T) {
	now := time.Unix(1700000100, 0).UTC()
	validRoots := func() (ca42authority.RootKeyset, error) { return ca42authority.RootKeyset{}, nil }
	tests := []struct {
		name       string
		args       []string
		loader     rootLoader
		verifier   readOnlyVerifier
		wantCode   int
		wantOutput string
	}{
		{"invalid arguments", []string{"execute"}, validRoots, nil, exitUsage, "reason=invalid_arguments"},
		{"roots unprovisioned", []string{"execute", "--attempt-id", "attempt-1"}, func() (ca42authority.RootKeyset, error) {
			return ca42authority.RootKeyset{}, errors.New("not provisioned")
		}, nil, exitUnavailable, "reason=authority_roots_unprovisioned"},
		{"verification denied", []string{"execute", "--attempt-id", "attempt-1"}, validRoots,
			func(context.Context, ca42runner.Command, ca42authority.RootKeyset, time.Time) (ca42runner.VerifiedBundle, error) {
				return ca42runner.VerifiedBundle{}, errors.New("denied")
			}, exitUnavailable, "reason=trust_denied"},
		{"read only success", []string{"execute", "--attempt-id", "attempt-1"}, validRoots,
			func(context.Context, ca42runner.Command, ca42authority.RootKeyset, time.Time) (ca42runner.VerifiedBundle, error) {
				return ca42runner.VerifiedBundle{Authority: ca42authority.Descriptor{
					AttemptID: "attempt-1", AuthorityEpoch: 1, AuthoritySequence: 2,
				}}, nil
			}, exitUnavailable, "execution=NOT_RUN authorization=NONE"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runCLIWith(context.Background(), test.args, &stdout, &stderr,
				test.loader, test.verifier, now)
			combined := stdout.String() + stderr.String()
			if code != test.wantCode || !strings.Contains(combined, test.wantOutput) || strings.Contains(combined, "authorization=GRANTED") {
				t.Fatalf("unexpected CLI result: code=%d output=%q", code, combined)
			}
		})
	}
}

package certs

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-acme/lego/v5/acme"
)

func TestClassify(t *testing.T) {
	inner := errors.New("port in use")
	cases := []struct {
		name       string
		err        error
		wantCode   string
		wantRetry  time.Duration
		wantDetail string
	}{
		{
			name:     "issueError",
			err:      &issueError{code: ReasonChallengePortBusy, err: inner},
			wantCode: ReasonChallengePortBusy, wantDetail: "port in use",
		},
		{
			name:     "issueError wrapped",
			err:      fmt.Errorf("wrap: %w", &issueError{code: ReasonStoreFailed, err: errors.New("disk full")}),
			wantCode: ReasonStoreFailed, wantDetail: "disk full",
		},
		{
			name: "connection problem",
			err: &acme.ProblemDetails{
				Type:   acme.ConnectionErrorType,
				Detail: "timeout connecting",
			},
			wantCode: ReasonValidationFailed, wantDetail: "timeout connecting",
		},
		{
			name: "subproblem",
			err: &acme.ProblemDetails{
				Type:   acme.CompoundErrorType,
				Detail: "parent",
				SubProblems: []acme.SubProblem{{
					Type:   acme.DNSErrorType,
					Detail: "nxdomain",
				}},
			},
			wantCode: ReasonDNSFailed, wantDetail: "parent; nxdomain",
		},
		{
			name: "rate limited",
			err: &acme.RateLimitedError{
				ProblemDetails: &acme.ProblemDetails{
					Type:   acme.RateLimitedErrorType,
					Detail: "too many",
				},
				RetryAfter: time.Hour,
			},
			wantCode: ReasonRateLimited, wantDetail: "too many", wantRetry: time.Hour,
		},
		{
			name: "rate limited paused",
			err: &acme.RateLimitedError{
				ProblemDetails: &acme.ProblemDetails{
					Type:   acme.RateLimitedErrorType,
					Detail: "Identifier paused for abuse",
				},
				RetryAfter: 2 * time.Hour,
			},
			wantCode: ReasonPaused, wantDetail: "Identifier paused for abuse", wantRetry: 2 * time.Hour,
		},
		{
			name:     "deadline",
			err:      context.DeadlineExceeded,
			wantCode: ReasonACMEUnreachable,
		},
		{
			name:     "plain",
			err:      errors.New("something else"),
			wantCode: ReasonUnknown, wantDetail: "something else",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, detail, retry := classify(tc.err)
			if code != tc.wantCode {
				t.Fatalf("code: got %q want %q", code, tc.wantCode)
			}
			if retry != tc.wantRetry {
				t.Fatalf("retryAfter: got %v want %v", retry, tc.wantRetry)
			}
			if tc.wantDetail != "" && detail != tc.wantDetail {
				t.Fatalf("detail: got %q want %q", detail, tc.wantDetail)
			}
		})
	}
}

func TestCleanDetail(t *testing.T) {
	long := make([]byte, maxDetailBytes+50)
	for i := range long {
		long[i] = 'a'
	}
	got := cleanDetail("ok\x00\xff" + string(long) + "tail")
	if len(got) != maxDetailBytes {
		t.Fatalf("len %d want %d", len(got), maxDetailBytes)
	}
	for _, r := range got {
		if r < 0x20 || r > 0x7e {
			t.Fatalf("non-printable %q in %q", r, got)
		}
	}
	if cleanDetail("hello") != "hello" {
		t.Fatal("ascii kept")
	}
}

package service

import (
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestNextQueryAtBacksOffAndKeepsALastCall(t *testing.T) {
	p := defaultPatrol
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	for attempts, want := range map[int]time.Duration{
		1: 5 * time.Minute, 2: 10 * time.Minute, 3: 20 * time.Minute, 4: 40 * time.Minute,
	} {
		if got := nextQueryAt(now, attempts, nil, p).Sub(now); got != want {
			t.Errorf("attempts=%d delay=%s want=%s", attempts, got, want)
		}
	}
	if got := nextQueryAt(now, 200, nil, p); !got.After(now) {
		t.Fatalf("huge attempts must still schedule in the future, got %s", got)
	}

	expires := now.Add(15 * time.Minute)
	if got, want := nextQueryAt(now, 3, &expires, p), expires.Add(-90*time.Second); !got.Equal(want) {
		t.Fatalf("last call=%s want=%s", got, want)
	}
	if got, want := nextQueryAt(now, 1, &expires, p), now.Add(5*time.Minute); !got.Equal(want) {
		t.Fatalf("early attempt=%s want=%s", got, want)
	}
	soon := now.Add(2 * time.Minute)
	if got := nextQueryAt(now, 1, &soon, p); !got.After(soon) {
		t.Fatalf("past the last call the next query must land after expiry, got %s", got)
	}
	tiny := p
	tiny.FirstDelay = time.Second
	if got := nextQueryAt(now, 1, nil, tiny).Sub(now); got < queryMinGap || queryMinGap <= channelQueryTimeout {
		t.Fatalf("lease=%s min_gap=%s query_timeout=%s", got, queryMinGap, channelQueryTimeout)
	}
}

func TestCanTransitionIntent(t *testing.T) {
	cases := []struct {
		from, to IntentStatus
		want     bool
	}{
		{IntentRequiresAction, IntentSucceeded, true},
		{IntentRequiresAction, IntentCancelled, true},
		{IntentRequiresAction, IntentExpired, true},
		{IntentSucceeded, IntentCancelled, false},
		{IntentCancelled, IntentRequiresAction, false},
		{IntentExpired, IntentSucceeded, false},
	}
	for _, tc := range cases {
		if got := CanTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("CanTransition(%s, %s)=%v want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestCanTransitionLate(t *testing.T) {
	cases := []struct {
		from, to LateStatus
		want     bool
	}{
		{LateSuspense, LateApplied, true},
		{LateApplied, LateSuspense, false},
		{LateApplied, LateApplied, false},
	}
	for _, tc := range cases {
		if got := CanTransitionLate(tc.from, tc.to); got != tc.want {
			t.Errorf("CanTransitionLate(%s, %s)=%v want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

func TestNotPayableError(t *testing.T) {
	err := NotPayable(CaseReleasedOrder, "cancelled")
	if !errors.Is(err, errOrderNotPayable) || !IsNotPayable(err) {
		t.Fatalf("NotPayable 构造的错误应被识别")
	}
	var npe *NotPayableError
	if !errors.As(err, &npe) {
		t.Fatalf("errors.As(*NotPayableError) false")
	}
	if npe.Kind != CaseReleasedOrder || npe.OrderStatus != "cancelled" {
		t.Fatalf("got kind=%s status=%s", npe.Kind, npe.OrderStatus)
	}
}

func TestOrderQueryResultNamesEveryOutcome(t *testing.T) {
	cases := []struct {
		res  QueryResult
		err  error
		want string
	}{
		{QueryResult{ChannelStatus: ChannelPaid, Reconciled: true}, nil, "reconciled"},
		{QueryResult{ChannelStatus: ChannelPaid, AlreadyRecorded: true}, nil, "already_recorded"},
		{QueryResult{ChannelStatus: ChannelPaid}, nil, "paid"},
		{QueryResult{ChannelStatus: ChannelUnpaid}, nil, "unpaid"},
		{QueryResult{ChannelStatus: ChannelNotFound}, nil, "not_found"},
		{QueryResult{}, errs.Newf(errs.Unavailable, "渠道查单失败，请稍后再试"), "failed"},
	}
	for _, tc := range cases {
		if got := orderQueryResult(tc.res, tc.err); got != tc.want {
			t.Errorf("orderQueryResult(%+v, %v)=%q want %q", tc.res, tc.err, got, tc.want)
		}
	}
}

package ca42runner

import (
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42authority"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42execution"
	"github.com/aegispanel/aegis/internal/platform/clientauth/ca42release"
)

func TestAdmissionTimeWindowRechecksExpiryAndRollback(t *testing.T) {
	opened := time.Unix(1_700_000_100, 0).UTC()
	validBundle := func() VerifiedBundle {
		return VerifiedBundle{
			Authority: ca42authority.Descriptor{
				ClockFloor: opened.Add(-time.Minute),
				NotBefore:  opened.Add(-time.Minute),
				NotAfter:   opened.Add(time.Hour),
			},
			Release: ca42release.Manifest{
				NotBefore: opened.Add(-time.Minute),
				NotAfter:  opened.Add(time.Hour),
			},
			Execution: ca42execution.Plan{
				NotBefore: opened.Add(-time.Minute),
				NotAfter:  opened.Add(time.Hour),
			},
		}
	}
	cases := []struct {
		name   string
		now    time.Time
		mutate func(*VerifiedBundle)
		ok     bool
	}{
		{name: "inside_window", now: opened, ok: true},
		{name: "verification_clock_rollback", now: opened.Add(-time.Second)},
		{name: "authority_clock_floor", now: opened, mutate: func(bundle *VerifiedBundle) { bundle.Authority.ClockFloor = opened.Add(time.Second) }},
		{name: "authority_not_before", now: opened, mutate: func(bundle *VerifiedBundle) { bundle.Authority.NotBefore = opened.Add(time.Second) }},
		{name: "release_not_before", now: opened, mutate: func(bundle *VerifiedBundle) { bundle.Release.NotBefore = opened.Add(time.Second) }},
		{name: "execution_not_before", now: opened, mutate: func(bundle *VerifiedBundle) { bundle.Execution.NotBefore = opened.Add(time.Second) }},
		{name: "authority_expired", now: opened, mutate: func(bundle *VerifiedBundle) { bundle.Authority.NotAfter = opened }},
		{name: "release_expired", now: opened, mutate: func(bundle *VerifiedBundle) { bundle.Release.NotAfter = opened }},
		{name: "execution_expired", now: opened, mutate: func(bundle *VerifiedBundle) { bundle.Execution.NotAfter = opened }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			bundle := validBundle()
			if test.mutate != nil {
				test.mutate(&bundle)
			}
			err := validateAdmissionTimeWindow(bundle, opened, test.now)
			if (err == nil) != test.ok {
				t.Fatalf("accepted=%v want=%v err=%v", err == nil, test.ok, err)
			}
		})
	}
	zero := time.Time{}
	afterZero := time.Unix(1, 0).UTC()
	zeroBundle := VerifiedBundle{
		Authority: ca42authority.Descriptor{ClockFloor: zero, NotBefore: zero, NotAfter: afterZero},
		Release:   ca42release.Manifest{NotBefore: zero, NotAfter: afterZero},
		Execution: ca42execution.Plan{NotBefore: zero, NotAfter: afterZero},
	}
	if err := validateAdmissionTimeWindow(zeroBundle, zero, zero); err == nil {
		t.Fatal("zero trusted clock accepted")
	}
}

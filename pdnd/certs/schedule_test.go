package certs

import (
	"testing"
	"time"
)

func TestProfileFor(t *testing.T) {
	if profileFor("192.0.2.10") != ProfileShortlived {
		t.Fatal("ipv4")
	}
	if profileFor("2001:db8::1") != ProfileShortlived {
		t.Fatal("ipv6")
	}
	if profileFor("node1.example.com") != "" {
		t.Fatal("dns")
	}
}

func TestFallbackRenewAt(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// 160h shortlived -> +80h
	shortEnd := start.Add(160 * time.Hour)
	at := fallbackRenewAt(start, shortEnd, ProfileShortlived)
	wantShort := start.Add(80 * time.Hour)
	if !at.Equal(wantShort) {
		t.Fatalf("shortlived: got %v want %v", at, wantShort)
	}
	// 90d classic -> +60d
	longEnd := start.Add(90 * 24 * time.Hour)
	at = fallbackRenewAt(start, longEnd, "")
	wantLong := start.Add(60 * 24 * time.Hour)
	if !at.Equal(wantLong) {
		t.Fatalf("default profile: got %v want %v", at, wantLong)
	}
}

func TestRetryDelay(t *testing.T) {
	old := jitter
	jitter = func(d time.Duration) time.Duration { return d }
	t.Cleanup(func() { jitter = old })

	if d := retryDelay(ReasonChallengePortBusy, 1, 0); d != portBusyRetry {
		t.Fatalf("port busy: %v", d)
	}
	if d := retryDelay(ReasonRateLimited, 1, time.Hour); d != time.Hour {
		t.Fatalf("rate limit retry: %v", d)
	}
	if d := retryDelay(ReasonRateLimited, 1, 5*time.Minute); d != firstRetry {
		t.Fatalf("rate limit floor: %v", d)
	}
	if d := retryDelay("other", 1, 0); d != firstRetry {
		t.Fatalf("fail 1: %v", d)
	}
	if d := retryDelay("other", 2, 0); d != 30*time.Minute {
		t.Fatalf("fail 2: %v", d)
	}
	if d := retryDelay("other", 5, 0); d != maxRetry {
		t.Fatalf("fail 5 cap: %v", d)
	}
	if d := retryDelay("other", 0, 0); d != firstRetry {
		t.Fatalf("fail 0 treated as 1: %v", d)
	}
	if d := retryDelay("other", 1000, 0); d != maxRetry {
		t.Fatalf("overflow: %v", d)
	}
}

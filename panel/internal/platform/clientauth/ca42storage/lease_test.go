package ca42storage

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

type leaseProbe struct {
	mu          sync.Mutex
	path        string
	dupCalls    int
	verifyCalls int
	dupErrAt    map[int]error
	verifyErrAt map[int]error
	duplicates  []*os.File
}

func (probe *leaseProbe) ops() leaseOps {
	return leaseOps{
		duplicate: func(*os.File) (*os.File, error) {
			probe.mu.Lock()
			defer probe.mu.Unlock()
			probe.dupCalls++
			if err := probe.dupErrAt[probe.dupCalls]; err != nil {
				return nil, err
			}
			file, err := os.Open(probe.path)
			if err == nil {
				probe.duplicates = append(probe.duplicates, file)
			}
			return file, err
		},
		verify: func(ctx context.Context, _ BoundEntry, _ *os.File, _ time.Time) error {
			probe.mu.Lock()
			defer probe.mu.Unlock()
			probe.verifyCalls++
			if err := ctx.Err(); err != nil {
				return err
			}
			return probe.verifyErrAt[probe.verifyCalls]
		},
	}
}

func leaseFixture(t *testing.T) (BoundEntry, time.Time) {
	t.Helper()
	data, digest := storageFixture(t)
	descriptor, err := Parse(data, digest)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000100, 0).UTC()
	bound := BoundDescriptor{descriptor: descriptor, planSHA: digest,
		planNotBefore: time.Unix(1700000000, 0).UTC(), planNotAfter: time.Unix(1700003600, 0).UTC(), bound: true}
	entry, err := bound.EntryAt(0, now)
	if err != nil {
		t.Fatal(err)
	}
	return entry, now
}

func leaseSource(t *testing.T) *os.File {
	t.Helper()
	created, err := os.CreateTemp(t.TempDir(), "ca42-lease-*")
	if err != nil {
		t.Fatal(err)
	}
	name := created.Name()
	if _, err := created.Write([]byte("lease fixture")); err != nil {
		t.Fatal(err)
	}
	if err := created.Close(); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestFDLeaseOwnsOnlyDuplicates(t *testing.T) {
	entry, now := leaseFixture(t)
	source := leaseSource(t)
	probe := &leaseProbe{path: source.Name(), dupErrAt: map[int]error{}, verifyErrAt: map[int]error{}}
	lease, err := retainVerifiedFDWithOps(context.Background(), entry, source, now, probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.RevalidateAt(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal("second close was not idempotent")
	}
	if _, err := source.Stat(); err != nil {
		t.Fatalf("lease closed caller source: %v", err)
	}
	if probe.dupCalls != 1 || probe.verifyCalls != 2 {
		t.Fatalf("unexpected lease operations: dup=%d verify=%d", probe.dupCalls, probe.verifyCalls)
	}
}

func TestFDLeaseAcquireRollbackAndCancellation(t *testing.T) {
	entry, now := leaseFixture(t)
	source := leaseSource(t)
	sentinel := errors.New("sentinel")
	for name, probe := range map[string]*leaseProbe{
		"duplicate": {path: source.Name(), dupErrAt: map[int]error{1: sentinel}, verifyErrAt: map[int]error{}},
		"verify":    {path: source.Name(), dupErrAt: map[int]error{}, verifyErrAt: map[int]error{1: sentinel}},
	} {
		t.Run(name, func(t *testing.T) {
			lease, err := retainVerifiedFDWithOps(context.Background(), entry, source, now, probe.ops())
			if lease != nil || !errors.Is(err, sentinel) {
				t.Fatalf("rollback failed: lease=%v err=%v", lease, err)
			}
			if _, err := source.Stat(); err != nil {
				t.Fatalf("rollback closed source: %v", err)
			}
			if name == "verify" {
				if len(probe.duplicates) != 1 {
					t.Fatalf("verification rollback duplicate count: %d", len(probe.duplicates))
				}
				if _, err := probe.duplicates[0].Stat(); err == nil {
					t.Fatal("verification rollback leaked owned duplicate")
				}
			}
		})
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	probe := &leaseProbe{path: source.Name(), dupErrAt: map[int]error{}, verifyErrAt: map[int]error{}}
	if lease, err := retainVerifiedFDWithOps(cancelled, entry, source, now, probe.ops()); lease != nil || !errors.Is(err, context.Canceled) || probe.dupCalls != 0 {
		t.Fatalf("pre-cancel acquired resources: lease=%v err=%v dup=%d", lease, err, probe.dupCalls)
	}
	postVerify, cancelPostVerify := context.WithCancel(context.Background())
	var duplicate *os.File
	ops := leaseOps{
		duplicate: func(*os.File) (*os.File, error) {
			var err error
			duplicate, err = os.Open(source.Name())
			return duplicate, err
		},
		verify: func(context.Context, BoundEntry, *os.File, time.Time) error {
			cancelPostVerify()
			return nil
		},
	}
	if lease, err := retainVerifiedFDWithOps(postVerify, entry, source, now, ops); lease != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("post-verify cancellation published lease: lease=%v err=%v", lease, err)
	}
	if duplicate == nil {
		t.Fatal("post-verify cancellation did not acquire test duplicate")
	}
	if _, err := duplicate.Stat(); err == nil {
		t.Fatal("post-verify cancellation leaked duplicate")
	}
}

func TestFDLeasePoisonState(t *testing.T) {
	entry, now := leaseFixture(t)
	source := leaseSource(t)
	sentinel := errors.New("integrity failure")
	probe := &leaseProbe{path: source.Name(), dupErrAt: map[int]error{}, verifyErrAt: map[int]error{2: sentinel}}
	lease, err := retainVerifiedFDWithOps(context.Background(), entry, source, now, probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.RevalidateAt(context.Background(), now); !errors.Is(err, sentinel) {
		t.Fatalf("revalidation failure lost: %v", err)
	}
	if len(probe.duplicates) != 1 {
		t.Fatalf("poison retained duplicate count: %d", len(probe.duplicates))
	}
	if _, err := probe.duplicates[0].Stat(); err == nil {
		t.Fatal("poisoned lease retained duplicate remained open")
	}
	if err := lease.RevalidateAt(context.Background(), now); !errors.Is(err, errLeasePoisoned) {
		t.Fatalf("poisoned lease reused: %v", err)
	}
	_ = lease.Close()
}

func TestFDLeaseZeroClosedAndExpiredFailClosed(t *testing.T) {
	if err := (*FDLease)(nil).Close(); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000100, 0).UTC()
	if err := (&FDLease{}).RevalidateAt(context.Background(), now); err == nil {
		t.Fatal("zero lease revalidated")
	}
	entry, _ := leaseFixture(t)
	source := leaseSource(t)
	probe := &leaseProbe{path: source.Name(), dupErrAt: map[int]error{}, verifyErrAt: map[int]error{}}
	if lease, err := retainVerifiedFDWithOps(context.Background(), entry, source, time.Unix(1700003600, 0).UTC(), probe.ops()); lease != nil || err == nil || probe.dupCalls != 0 {
		t.Fatalf("expired entry acquired lease: lease=%v err=%v dup=%d", lease, err, probe.dupCalls)
	}
	probe = &leaseProbe{path: source.Name(), dupErrAt: map[int]error{}, verifyErrAt: map[int]error{}}
	lease, err := retainVerifiedFDWithOps(context.Background(), entry, source, now, probe.ops())
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.RevalidateAt(context.Background(), time.Unix(1700003600, 0).UTC()); err == nil {
		t.Fatal("established lease survived plan not-after")
	}
	if len(probe.duplicates) != 1 {
		t.Fatalf("expired lease duplicate count: %d", len(probe.duplicates))
	}
	if _, err := probe.duplicates[0].Stat(); err == nil {
		t.Fatal("expired lease retained duplicate remained open")
	}
	_ = lease.Close()
}

func TestFDLeaseCloseSerializesWithRevalidation(t *testing.T) {
	entry, now := leaseFixture(t)
	source := leaseSource(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	verifyCalls := 0
	ops := leaseOps{
		duplicate: func(*os.File) (*os.File, error) { return os.Open(source.Name()) },
		verify: func(context.Context, BoundEntry, *os.File, time.Time) error {
			mu.Lock()
			verifyCalls++
			call := verifyCalls
			mu.Unlock()
			if call == 2 {
				close(entered)
				<-release
			}
			return nil
		},
	}
	lease, err := retainVerifiedFDWithOps(context.Background(), entry, source, now, ops)
	if err != nil {
		t.Fatal(err)
	}
	revalidated := make(chan error, 1)
	go func() { revalidated <- lease.RevalidateAt(context.Background(), now) }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- lease.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close raced ahead of active revalidation: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-revalidated; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := lease.RevalidateAt(context.Background(), now); !errors.Is(err, errLeaseClosed) {
		t.Fatalf("closed lease revalidated: %v", err)
	}
}

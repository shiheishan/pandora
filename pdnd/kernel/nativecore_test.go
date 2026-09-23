package kernel

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/outbound"
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
)

func TestNativeCoreLifecycleAndAtomicRoutingSwap(t *testing.T) {
	registry := NewAdapterRegistry()
	adapter := &nativeCoreTestAdapter{}
	if err := registry.Register("reality", func(InboundSpec) (Adapter, error) {
		return adapter, nil
	}); err != nil {
		t.Fatal(err)
	}
	c := NewNativeCore(registry)
	if err := c.InboundReady("in-1"); err == nil {
		t.Fatal("inbound was ready before core start")
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg := &core.InboundConfig{Tag: "in-1", Protocol: "reality", Port: 443}
	if err := c.AddInbound(cfg); err != nil {
		t.Fatal(err)
	}
	if err := c.InboundReady("in-1"); err != nil {
		t.Fatalf("published inbound was not ready: %v", err)
	}
	if err := c.AddUsers("in-1", []core.User{{UUID: "u1"}}); err != nil {
		t.Fatal(err)
	}
	if adapter.users != 1 {
		t.Fatalf("AddUsers count = %d, want 1", adapter.users)
	}
	if err := c.SetRouting("in-1", &core.Routing{
		Routes: []core.Route{{Matcher: map[string]any{"domain": "blocked.example"}, OutboundTag: BlockTag}},
	}); err != nil {
		t.Fatal(err)
	}
	_, err := adapter.hooks.DataPlane.DialTCP(context.Background(), route.Meta{Domain: "blocked.example"}, M.ParseSocksaddrHostPort("blocked.example", 443))
	if !errors.Is(err, outbound.ErrBlocked) {
		t.Fatalf("blocked routed dial = %v", err)
	}
	if err := c.DelInbound("in-1"); err != nil {
		t.Fatal(err)
	}
	if err := c.InboundReady("in-1"); err == nil {
		t.Fatal("deleted inbound remained ready")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeCoreConcurrentInboundOperations(t *testing.T) {
	registry := NewAdapterRegistry()
	if err := registry.Register("concurrent", func(InboundSpec) (Adapter, error) {
		return &concurrentNativeAdapter{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	c := NewNativeCore(registry)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := c.AddInbound(&core.InboundConfig{Tag: "concurrent", Protocol: "concurrent", Port: 443}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var firstErr error
	var errMu sync.Mutex
	recordErr := func(err error) {
		if err == nil {
			return
		}
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for iteration := 0; iteration < 20; iteration++ {
				recordErr(c.AddUsers("concurrent", []core.User{{ID: int64(worker), UUID: fmt.Sprintf("u-%d-%d", worker, iteration)}}))
				recordErr(c.DelUsers("concurrent", []string{"missing"}))
				recordErr(c.SetRouting("concurrent", &core.Routing{Routes: []core.Route{{Matcher: map[string]any{"domain": "blocked.example"}, OutboundTag: BlockTag}}}))
				_, _ = c.GetTraffic("concurrent")
				_ = c.OnlineIPs("concurrent")
			}
		}(worker)
	}
	wg.Wait()
	if firstErr != nil {
		t.Fatal(firstErr)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeCoreSerializesSameTagInboundStarts(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	registry := NewAdapterRegistry()
	if err := registry.Register("gated", func(InboundSpec) (Adapter, error) {
		return &gatedNativeAdapter{started: started, release: release}, nil
	}); err != nil {
		t.Fatal(err)
	}
	c := NewNativeCore(registry)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() {
		first <- c.AddInbound(&core.InboundConfig{Tag: "same", Protocol: "gated", Port: 18081})
	}()
	<-started
	go func() {
		second <- c.AddInbound(&core.InboundConfig{Tag: "same", Protocol: "gated", Port: 18082})
	}()
	select {
	case <-started:
		t.Fatal("second same-tag generation started before the first completed")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)

	if err := <-first; err != nil {
		t.Fatalf("first inbound start failed: %v", err)
	}
	<-started
	if err := <-second; err != nil {
		t.Fatalf("newest inbound start failed: %v", err)
	}
	if _, err := c.GetTraffic("same"); err != nil {
		t.Fatalf("newest inbound was not retained: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeCoreDoesNotResurrectAfterCloseDuringStart(t *testing.T) {
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	registry := NewAdapterRegistry()
	if err := registry.Register("gated", func(InboundSpec) (Adapter, error) {
		return &gatedNativeAdapter{started: started, release: release}, nil
	}); err != nil {
		t.Fatal(err)
	}
	c := NewNativeCore(registry)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- c.AddInbound(&core.InboundConfig{Tag: "closing", Protocol: "gated", Port: 18083})
	}()
	<-started
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-result; err == nil {
		t.Fatal("inbound start succeeded after core close")
	}
}

type concurrentNativeAdapter struct{}

func (*concurrentNativeAdapter) Protocol() string           { return "concurrent" }
func (*concurrentNativeAdapter) Validate(InboundSpec) error { return nil }
func (*concurrentNativeAdapter) Start(context.Context, InboundSpec, AdapterHooks) error {
	return nil
}
func (*concurrentNativeAdapter) Close() error                  { return nil }
func (*concurrentNativeAdapter) AddUsers([]core.User) error    { return nil }
func (*concurrentNativeAdapter) UpsertUsers([]core.User) error { return nil }
func (*concurrentNativeAdapter) DelUsers([]string) error       { return nil }
func (*concurrentNativeAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	return nil, nil
}
func (*concurrentNativeAdapter) OnlineIPs() map[int64][]string { return nil }

type gatedNativeAdapter struct {
	started chan<- struct{}
	release <-chan struct{}
	once    sync.Once
}

func (*gatedNativeAdapter) Protocol() string           { return "gated" }
func (*gatedNativeAdapter) Validate(InboundSpec) error { return nil }
func (a *gatedNativeAdapter) Start(context.Context, InboundSpec, AdapterHooks) error {
	a.started <- struct{}{}
	<-a.release
	return nil
}
func (a *gatedNativeAdapter) Close() error {
	a.once.Do(func() {})
	return nil
}
func (*gatedNativeAdapter) AddUsers([]core.User) error    { return nil }
func (*gatedNativeAdapter) UpsertUsers([]core.User) error { return nil }
func (*gatedNativeAdapter) DelUsers([]string) error       { return nil }
func (*gatedNativeAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	return nil, nil
}
func (*gatedNativeAdapter) OnlineIPs() map[int64][]string { return nil }

type nativeCoreTestAdapter struct {
	hooks AdapterHooks
	users int
}

func (*nativeCoreTestAdapter) Protocol() string           { return "reality" }
func (*nativeCoreTestAdapter) Validate(InboundSpec) error { return nil }
func (a *nativeCoreTestAdapter) Start(_ context.Context, _ InboundSpec, hooks AdapterHooks) error {
	a.hooks = hooks
	return nil
}
func (*nativeCoreTestAdapter) Close() error { return nil }
func (a *nativeCoreTestAdapter) AddUsers(users []core.User) error {
	a.users += len(users)
	return nil
}
func (a *nativeCoreTestAdapter) UpsertUsers(users []core.User) error {
	a.users += len(users)
	return nil
}
func (*nativeCoreTestAdapter) DelUsers([]string) error                      { return nil }
func (*nativeCoreTestAdapter) SnapshotTraffic() ([]core.UserTraffic, error) { return nil, nil }
func (*nativeCoreTestAdapter) OnlineIPs() map[int64][]string                { return nil }

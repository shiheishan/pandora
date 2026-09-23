package node

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

func TestEffectiveHealthRequiresRunningStabilityWindow(t *testing.T) {
	now := time.Now()
	kernel := &rollbackCore{ready: true}
	n := &Node{started: true, appliedAt: now, kernel: kernel, tag: "node-n1"}
	if n.effectiveHealthReady(now.Add(effectiveHealthStabilityWindow - time.Millisecond)) {
		t.Fatal("effective config was declared healthy before the stability window")
	}
	if !n.effectiveHealthReady(now.Add(effectiveHealthStabilityWindow)) {
		t.Fatal("effective config was not declared healthy after the stability window")
	}
	n.started = false
	if n.effectiveHealthReady(now.Add(2 * effectiveHealthStabilityWindow)) {
		t.Fatal("stopped node was declared healthy")
	}
	n.started = true
	kernel.ready = false
	if n.effectiveHealthReady(now.Add(2 * effectiveHealthStabilityWindow)) {
		t.Fatal("unready inbound was declared healthy")
	}
	n.kernel = &coreWithoutReadiness{Core: kernel}
	if n.effectiveHealthReady(now.Add(2 * effectiveHealthStabilityWindow)) {
		t.Fatal("runtime without a readiness contract was declared healthy")
	}
}

type rollbackCore struct {
	current          *core.InboundConfig
	routing          *core.Routing
	users            []core.User
	failRoutingCount int
	ready            bool
}

type coreWithoutReadiness struct{ core.Core }

func (c *rollbackCore) InboundReady(string) error {
	if !c.ready {
		return errors.New("inbound is not ready")
	}
	return nil
}

type preservingCore struct {
	rollbackCore
	applyCalls int
}

func (c *preservingCore) ApplyInbound(cfg *core.InboundConfig, routing *core.Routing) error {
	c.applyCalls++
	if c.applyCalls > 1 {
		return &core.ConfigApplyError{Err: errors.New("preflight rejected"), PreviousPreserved: true}
	}
	if err := c.AddInbound(cfg); err != nil {
		return err
	}
	return c.SetRouting(cfg.Tag, routing)
}

func (c *rollbackCore) Type() string                                  { return "rollback-test" }
func (c *rollbackCore) Start(context.Context) error                   { return nil }
func (c *rollbackCore) Close() error                                  { return nil }
func (c *rollbackCore) DelUsers(string, []string) error               { return nil }
func (c *rollbackCore) GetTraffic(string) ([]core.UserTraffic, error) { return nil, nil }
func (c *rollbackCore) OnlineIPs(string) map[int64][]string           { return nil }

func (c *rollbackCore) AddInbound(cfg *core.InboundConfig) error {
	copyCfg := *cfg
	c.current = &copyCfg
	c.users = nil
	return nil
}

func (c *rollbackCore) DelInbound(string) error {
	c.current = nil
	c.routing = nil
	c.users = nil
	return nil
}

func (c *rollbackCore) AddUsers(_ string, users []core.User) error {
	c.users = append(c.users, users...)
	return nil
}

func (c *rollbackCore) UpsertUsers(_ string, users []core.User) error {
	c.users = append([]core.User(nil), users...)
	return nil
}

func (c *rollbackCore) SetRouting(_ string, routing *core.Routing) error {
	if c.failRoutingCount > 0 {
		c.failRoutingCount--
		return errors.New("rejected routing")
	}
	c.routing = routing
	return nil
}

func TestApplyConfigRestoresPreviousInboundAndUsers(t *testing.T) {
	kernel := &rollbackCore{}
	client := panel.New(panel.Options{
		BaseURL: "http://127.0.0.1", NodeID: "n1", NodeType: "vless", Token: "token",
	})
	n := New(client, kernel, testLogger())
	oldConfig := map[string]any{
		"server_port": float64(18080), "protocol": "vless",
		"base_config": map[string]any{"pull_interval": float64(30), "push_interval": float64(40)},
	}
	if err := n.applyConfig(oldConfig); err != nil {
		t.Fatal(err)
	}
	user := core.User{ID: 1, UUID: "user-1"}
	n.known[user.UUID] = user
	if err := kernel.AddUsers(n.tag, []core.User{user}); err != nil {
		t.Fatal(err)
	}

	kernel.failRoutingCount = 1
	err := n.applyConfig(map[string]any{
		"server_port": float64(18081), "protocol": "trojan",
		"base_config": map[string]any{"pull_interval": float64(5), "push_interval": float64(5)},
	})
	if err == nil {
		t.Fatal("invalid replacement unexpectedly succeeded")
	}
	if kernel.current == nil || kernel.current.Port != 18080 || kernel.current.Protocol != "vless" {
		t.Fatalf("previous inbound was not restored: %+v", kernel.current)
	}
	if len(kernel.users) != 1 || kernel.users[0].UUID != user.UUID {
		t.Fatalf("previous users were not restored: %+v", kernel.users)
	}
	if n.pullInterval.Seconds() != 30 || n.pushInterval.Seconds() != 40 {
		t.Fatalf("intervals were not restored: pull=%s push=%s", n.pullInterval, n.pushInterval)
	}
	if intFrom(n.activeConfig, "server_port") != 18080 || !n.started {
		t.Fatalf("active config changed after rollback: %+v", n.activeConfig)
	}
}

func TestApplyConfigMarksNodeStoppedWhenRollbackFails(t *testing.T) {
	kernel := &rollbackCore{}
	client := panel.New(panel.Options{
		BaseURL: "http://127.0.0.1", NodeID: "n1", NodeType: "vless", Token: "token",
	})
	n := New(client, kernel, testLogger())
	if err := n.applyConfig(map[string]any{"server_port": float64(18080), "protocol": "vless"}); err != nil {
		t.Fatal(err)
	}
	kernel.failRoutingCount = 2
	err := n.applyConfig(map[string]any{"server_port": float64(18081), "protocol": "trojan"})
	if err == nil || n.started {
		t.Fatalf("rollback failure did not stop node: started=%v err=%v", n.started, err)
	}
}

func TestApplyConfigDoesNotReinstallPreservedGeneration(t *testing.T) {
	kernel := &preservingCore{}
	client := panel.New(panel.Options{
		BaseURL: "http://127.0.0.1", NodeID: "n1", NodeType: "vless", Token: "token",
	})
	n := New(client, kernel, testLogger())
	if err := n.applyConfig(map[string]any{"server_port": float64(18080), "protocol": "vless"}); err != nil {
		t.Fatal(err)
	}
	err := n.applyConfig(map[string]any{"server_port": float64(18081), "protocol": "trojan"})
	if err == nil {
		t.Fatal("invalid replacement unexpectedly succeeded")
	}
	if kernel.applyCalls != 2 {
		t.Fatalf("preserved generation was reinstalled: apply calls=%d", kernel.applyCalls)
	}
	if kernel.current == nil || kernel.current.Port != 18080 || !n.started {
		t.Fatalf("previous generation was not preserved: %+v", kernel.current)
	}
}

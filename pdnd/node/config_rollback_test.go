package node

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// health_passed 要入站在服务、切换后稳定够久、内核确认就绪。
func TestHealthPassedRequiresStabilityAndReadiness(t *testing.T) {
	kernel := &rollbackCore{ready: true}
	ts := newTestServer(t, kernel, "")
	ts.welcome()
	nc := portConfig(t, "n1", "vless", 18080)
	ts.config(nc)
	n := ts.node(t, "n1")
	now := n.appliedAt
	hp := "CONFIG_PHASE_HEALTH_PASSED:" + nc.ContentSha256[:8]
	hasHealth := func() bool { return slices.Contains(ts.link.acks("n1"), hp) }

	ts.reportHealth(n, now.Add(healthStabilityWindow-time.Millisecond))
	if hasHealth() {
		t.Fatal("稳定窗口内就报了 health_passed")
	}
	n.started = false
	ts.reportHealth(n, now.Add(2*healthStabilityWindow))
	if hasHealth() {
		t.Fatal("入站没在服务也报了 health_passed")
	}
	n.started = true
	kernel.ready = false
	ts.reportHealth(n, now.Add(2*healthStabilityWindow))
	if hasHealth() {
		t.Fatal("内核没就绪也报了 health_passed")
	}
	kernel.ready = true
	ts.reportHealth(n, now.Add(healthStabilityWindow))
	if !hasHealth() || n.healthPending {
		t.Fatal("稳定窗口过后没报 health_passed")
	}
}

// rollbackNode 建一个还没有入站的节点 n1。
func rollbackNode(t *testing.T, kernel core.Core) *Node {
	t.Helper()
	ts := newTestServer(t, kernel, "")
	n := ts.newNode("n1", "vless")
	ts.nodes["n1"] = n
	return n
}

type rollbackCore struct {
	current          *core.InboundConfig
	routing          *core.Routing
	users            []core.User
	failRoutingCount int
	ready            bool
}

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
	return c.rollbackCore.ApplyInbound(cfg, routing)
}

func (c *preservingCore) ApplyInboundWithUsers(cfg *core.InboundConfig, routing *core.Routing, users []core.User) error {
	if err := c.ApplyInbound(cfg, routing); err != nil {
		return err
	}
	return c.AddUsers(cfg.Tag, users)
}

func (c *rollbackCore) Type() string                                  { return "rollback-test" }
func (c *rollbackCore) Start(context.Context) error                   { return nil }
func (c *rollbackCore) Close() error                                  { return nil }
func (c *rollbackCore) DelUsers(string, []string) error               { return nil }
func (c *rollbackCore) GetTraffic(string) ([]core.UserTraffic, error) { return nil, nil }
func (c *rollbackCore) OnlineIPs(string) map[int64][]string           { return nil }
func (c *rollbackCore) SetInboundOwner(string, string, string)        {}

func (c *rollbackCore) AddInbound(cfg *core.InboundConfig) error {
	copyCfg := *cfg
	c.current = &copyCfg
	c.users = nil
	return nil
}

// ApplyInbound 整代装入站：建入站、设分流；分流被拒就撤掉新入站（不保留旧一代）。
func (c *rollbackCore) ApplyInbound(cfg *core.InboundConfig, routing *core.Routing) error {
	if err := c.AddInbound(cfg); err != nil {
		return err
	}
	if err := c.SetRouting(cfg.Tag, routing); err != nil {
		_ = c.DelInbound(cfg.Tag)
		return err
	}
	return nil
}

// ApplyInboundWithUsers 先整代装入站，再装名单。
func (c *rollbackCore) ApplyInboundWithUsers(cfg *core.InboundConfig, routing *core.Routing, users []core.User) error {
	if err := c.ApplyInbound(cfg, routing); err != nil {
		return err
	}
	return c.AddUsers(cfg.Tag, users)
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
	n := rollbackNode(t, kernel)
	oldConfig := map[string]any{"server_port": float64(18080)}
	if err := n.applyConfig(oldConfig, "vless"); err != nil {
		t.Fatal(err)
	}
	user := core.User{ID: 1, UUID: "user-1"}
	n.known[user.ID] = user
	if err := kernel.AddUsers(n.tag, []core.User{user}); err != nil {
		t.Fatal(err)
	}

	kernel.failRoutingCount = 1
	err := n.applyConfig(map[string]any{"server_port": float64(18081)}, "trojan")
	if err == nil {
		t.Fatal("invalid replacement unexpectedly succeeded")
	}
	if kernel.current == nil || kernel.current.Port != 18080 || kernel.current.Protocol != "vless" {
		t.Fatalf("previous inbound was not restored: %+v", kernel.current)
	}
	if len(kernel.users) != 1 || kernel.users[0].UUID != user.UUID {
		t.Fatalf("previous users were not restored: %+v", kernel.users)
	}
	if intFrom(n.activeConfig, "server_port") != 18080 || !n.started {
		t.Fatalf("active config changed after rollback: %+v", n.activeConfig)
	}
}

func TestApplyConfigMarksNodeStoppedWhenRollbackFails(t *testing.T) {
	kernel := &rollbackCore{}
	n := rollbackNode(t, kernel)
	if err := n.applyConfig(map[string]any{"server_port": float64(18080)}, "vless"); err != nil {
		t.Fatal(err)
	}
	kernel.failRoutingCount = 2
	err := n.applyConfig(map[string]any{"server_port": float64(18081)}, "trojan")
	if err == nil || n.started {
		t.Fatalf("rollback failure did not stop node: started=%v err=%v", n.started, err)
	}
}

func TestApplyConfigDoesNotReinstallPreservedGeneration(t *testing.T) {
	kernel := &preservingCore{}
	n := rollbackNode(t, kernel)
	if err := n.applyConfig(map[string]any{"server_port": float64(18080)}, "vless"); err != nil {
		t.Fatal(err)
	}
	err := n.applyConfig(map[string]any{"server_port": float64(18081)}, "trojan")
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

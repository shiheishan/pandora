package main

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/panel"
	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	t := c.t
	c.mu.Unlock()
	return t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

func startSimWith(t *testing.T, opts SimOptions) (*Sim, *fakeLink, *Board, context.CancelFunc) {
	t.Helper()
	if opts.Name == "" {
		opts.Name = simName
	}
	if opts.Board == nil {
		opts.Board = NewBoard("", nil)
	}
	if opts.ReportInterval <= 0 {
		opts.ReportInterval = 20 * time.Millisecond
	}
	if opts.TrafficUsers <= 0 {
		opts.TrafficUsers = 5
	}
	sim := NewSim(opts)
	link := newFakeLink()
	ctx, cancel := context.WithCancel(context.Background())
	go sim.Run(ctx, link)
	return sim, link, opts.Board, cancel
}

func TestSimConnectedAtAndDisconnectedAt(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)}
	_, link, board, cancel := startSimWith(t, SimOptions{Now: clock.Now})
	defer cancel()

	t1 := time.Date(2026, 3, 1, 10, 1, 0, 0, time.UTC)
	clock.Set(t1)
	link.events <- panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{SessionId: "s1"}}
	waitUntil(t, 2*time.Second, func() bool {
		st := board.Snapshot().Servers[simName]
		return st != nil && st.ConnectedAt.Equal(t1) && st.FirstConnectedAt.Equal(t1)
	}, "Welcome 应写入 ConnectedAt 与 FirstConnectedAt")

	t2 := time.Date(2026, 3, 1, 10, 2, 0, 0, time.UTC)
	clock.Set(t2)
	link.events <- panel.Event{Kind: panel.EventDisconnected, Err: fmt.Errorf("reset")}
	waitUntil(t, 2*time.Second, func() bool {
		st := board.Snapshot().Servers[simName]
		return st != nil && st.DisconnectedAt.Equal(t2)
	}, "断开应写入 DisconnectedAt")

	t3 := time.Date(2026, 3, 1, 10, 3, 0, 0, time.UTC)
	clock.Set(t3)
	link.events <- panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{SessionId: "s2"}}
	waitUntil(t, 2*time.Second, func() bool {
		st := board.Snapshot().Servers[simName]
		return st != nil && st.ConnectedAt.Equal(t3) && st.FirstConnectedAt.Equal(t1) && st.Epoch == 2
	}, "再次 Welcome 应更新 ConnectedAt、保留 FirstConnectedAt")
}

func TestSimRosterAppliedAtAndCounts(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)}
	_, link, board, cancel := startSimWith(t, SimOptions{Now: clock.Now})
	defer cancel()

	link.events <- panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{}}
	nc := nodeConfigJSON(t, "n1", "vless", 20001)
	link.events <- panel.Event{Kind: panel.EventConfig, Config: &nodev1.ServerConfig{Nodes: []*nodev1.NodeConfig{nc}}}
	waitUntil(t, 2*time.Second, func() bool { return len(link.configAckPhases("n1")) >= 2 }, "等配置")

	t1 := time.Date(2026, 4, 1, 1, 0, 0, 0, time.UTC)
	clock.Set(t1)
	link.events <- panel.Event{Kind: panel.EventUsersFull, UsersFull: &panel.UsersFull{
		NodeID: "n1", Version: "v1",
		Users: []*nodev1.User{{Uid: 1, Uuid: "a"}},
	}}
	waitUntil(t, 2*time.Second, func() bool {
		n := board.Snapshot().Servers[simName].Nodes["n1"]
		return n != nil && n.RosterVersion == "v1" && n.RosterAppliedAt.Equal(t1) &&
			n.FullsReceived == 1 && n.DeltasReceived == 0 && len(n.RosterHistory) == 1 && n.RosterHistory[0].Via == "full"
	}, "UsersFull v1")

	t2 := time.Date(2026, 4, 1, 2, 0, 0, 0, time.UTC)
	clock.Set(t2)
	link.events <- panel.Event{Kind: panel.EventUsersFull, UsersFull: &panel.UsersFull{
		NodeID: "n1", Version: "v1",
		Users: []*nodev1.User{{Uid: 1, Uuid: "a"}},
	}}
	waitUntil(t, 2*time.Second, func() bool {
		n := board.Snapshot().Servers[simName].Nodes["n1"]
		return n != nil && n.FullsReceived == 2 && n.RosterAppliedAt.Equal(t1) && len(n.RosterHistory) == 1
	}, "同版本 UsersFull 不更新 RosterAppliedAt")

	t3 := time.Date(2026, 4, 1, 3, 0, 0, 0, time.UTC)
	clock.Set(t3)
	link.events <- panel.Event{Kind: panel.EventUsersDelta, UsersDelta: &nodev1.UsersDelta{
		NodeId: "n1", FromVersion: "v1", ToVersion: "v2",
		Upserted: []*nodev1.User{{Uid: 2, Uuid: "b"}},
	}}
	waitUntil(t, 2*time.Second, func() bool {
		n := board.Snapshot().Servers[simName].Nodes["n1"]
		return n != nil && n.RosterVersion == "v2" && n.DeltasReceived == 1 && n.RosterAppliedAt.Equal(t3) &&
			len(n.RosterHistory) == 2 && n.RosterHistory[1].Via == "delta"
	}, "增量 v1→v2")

	t4 := time.Date(2026, 4, 1, 4, 0, 0, 0, time.UTC)
	clock.Set(t4)
	link.events <- panel.Event{Kind: panel.EventUsersDelta, UsersDelta: &nodev1.UsersDelta{
		NodeId: "n1", FromVersion: "v0", ToVersion: "v5",
	}}
	waitUntil(t, 2*time.Second, func() bool {
		n := board.Snapshot().Servers[simName].Nodes["n1"]
		return n != nil && n.DeltasReceived == 2 && n.RosterVersion == "v2" && n.RosterAppliedAt.Equal(t3)
	}, "版本不对的增量只加 DeltasReceived")
}

func TestSimRosterHistoryCapped(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)}
	_, link, board, cancel := startSimWith(t, SimOptions{Now: clock.Now})
	defer cancel()

	link.events <- panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{}}
	nc := nodeConfigJSON(t, "n1", "vless", 20001)
	link.events <- panel.Event{Kind: panel.EventConfig, Config: &nodev1.ServerConfig{Nodes: []*nodev1.NodeConfig{nc}}}
	waitUntil(t, 2*time.Second, func() bool { return len(link.configAckPhases("n1")) >= 2 }, "等配置")

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	clock.Set(base)
	link.events <- panel.Event{Kind: panel.EventUsersFull, UsersFull: &panel.UsersFull{
		NodeID: "n1", Version: "v0", Users: []*nodev1.User{{Uid: 1, Uuid: "u"}},
	}}
	waitUntil(t, 2*time.Second, func() bool {
		n := board.Snapshot().Servers[simName].Nodes["n1"]
		return n != nil && n.RosterVersion == "v0"
	}, "全量 v0")

	for i := 0; i < 10; i++ {
		from := fmt.Sprintf("v%d", i)
		to := fmt.Sprintf("v%d", i+1)
		at := base.Add(time.Duration(i+1) * time.Minute)
		clock.Set(at)
		link.events <- panel.Event{Kind: panel.EventUsersDelta, UsersDelta: &nodev1.UsersDelta{
			NodeId: "n1", FromVersion: from, ToVersion: to,
			Upserted: []*nodev1.User{{Uid: int64(i + 2), Uuid: fmt.Sprintf("u%d", i+2)}},
		}}
		waitUntil(t, 2*time.Second, func() bool {
			n := board.Snapshot().Servers[simName].Nodes["n1"]
			return n != nil && n.RosterVersion == to
		}, "增量 "+from+"→"+to)
	}

	n := board.Snapshot().Servers[simName].Nodes["n1"]
	if n == nil {
		t.Fatal("节点 n1 缺失")
	}
	if len(n.RosterHistory) != 8 {
		t.Fatalf("History 长度 %d want 8", len(n.RosterHistory))
	}
	if n.RosterHistory[0].Version != "v3" || n.RosterHistory[7].Version != "v10" {
		t.Fatalf("History 首尾 = %s … %s want v3 … v10", n.RosterHistory[0].Version, n.RosterHistory[7].Version)
	}
}

func TestSimOmitStatusUsers(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)}
	_, link, board, cancel := startSimWith(t, SimOptions{Now: clock.Now, OmitStatusUsers: true})
	defer cancel()

	link.events <- panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{}}
	nc := nodeConfigJSON(t, "n1", "vless", 20001)
	link.events <- panel.Event{Kind: panel.EventConfig, Config: &nodev1.ServerConfig{Nodes: []*nodev1.NodeConfig{nc}}}
	waitUntil(t, 2*time.Second, func() bool { return len(link.configAckPhases("n1")) >= 2 }, "等配置")

	link.events <- panel.Event{Kind: panel.EventUsersFull, UsersFull: &panel.UsersFull{
		NodeID: "n1", Version: "v1",
		Users: []*nodev1.User{
			{Uid: 1, Uuid: "aaa"}, {Uid: 2, Uuid: "bbb"}, {Uid: 3, Uuid: "ccc"},
		},
	}}
	waitUntil(t, 2*time.Second, func() bool {
		n := board.Snapshot().Servers[simName].Nodes["n1"]
		return n != nil && n.UserCount == 3 && n.Users == nil
	}, "omit 时 Users 为 nil")

	cancel()

	clock2 := &fakeClock{t: time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)}
	_, link2, board2, cancel2 := startSimWith(t, SimOptions{Now: clock2.Now, OmitStatusUsers: false})
	defer cancel2()

	link2.events <- panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{}}
	link2.events <- panel.Event{Kind: panel.EventConfig, Config: &nodev1.ServerConfig{Nodes: []*nodev1.NodeConfig{nc}}}
	waitUntil(t, 2*time.Second, func() bool { return len(link2.configAckPhases("n1")) >= 2 }, "等配置")

	link2.events <- panel.Event{Kind: panel.EventUsersFull, UsersFull: &panel.UsersFull{
		NodeID: "n1", Version: "v1",
		Users: []*nodev1.User{
			{Uid: 1, Uuid: "aaa"}, {Uid: 2, Uuid: "bbb"}, {Uid: 3, Uuid: "ccc"},
		},
	}}
	waitUntil(t, 2*time.Second, func() bool {
		n := board2.Snapshot().Servers[simName].Nodes["n1"]
		return n != nil && n.UserCount == 3 && len(n.Users) == 3
	}, "未 omit 时 Users 长度 3")
}

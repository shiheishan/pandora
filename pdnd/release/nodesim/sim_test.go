package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/panel"
	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
)

const simName = "srv1"

// fakeLink 是测试用的 Link，记录发出的消息。
type fakeLink struct {
	events chan panel.Event

	mu         sync.Mutex
	heartbeats []*nodev1.Heartbeat
	traffic    []*nodev1.TrafficReport
	online     []*nodev1.OnlineIPs
	configAcks []*nodev1.ConfigAck
	usersAcks  []*nodev1.UsersAck
}

func newFakeLink() *fakeLink {
	return &fakeLink{events: make(chan panel.Event, 64)}
}

func (l *fakeLink) Events() <-chan panel.Event { return l.events }

func (l *fakeLink) record(f func()) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	f()
	return true
}

func (l *fakeLink) SendHeartbeat(m *nodev1.Heartbeat) bool {
	return l.record(func() { l.heartbeats = append(l.heartbeats, m) })
}
func (l *fakeLink) SendTraffic(m *nodev1.TrafficReport) bool {
	return l.record(func() { l.traffic = append(l.traffic, m) })
}
func (l *fakeLink) SendOnline(m *nodev1.OnlineIPs) bool {
	return l.record(func() { l.online = append(l.online, m) })
}
func (l *fakeLink) SendConfigAck(m *nodev1.ConfigAck) bool {
	return l.record(func() { l.configAcks = append(l.configAcks, m) })
}
func (l *fakeLink) SendUsersAck(m *nodev1.UsersAck) bool {
	return l.record(func() { l.usersAcks = append(l.usersAcks, m) })
}

func (l *fakeLink) heartbeatCount() int {
	l.mu.Lock()
	n := len(l.heartbeats)
	l.mu.Unlock()
	return n
}

func (l *fakeLink) trafficReports() []*nodev1.TrafficReport {
	l.mu.Lock()
	out := append([]*nodev1.TrafficReport(nil), l.traffic...)
	l.mu.Unlock()
	return out
}

func (l *fakeLink) lastTraffic() *nodev1.TrafficReport {
	rep := l.trafficReports()
	if len(rep) == 0 {
		return nil
	}
	return rep[len(rep)-1]
}

func (l *fakeLink) hasConfigAckReason(reason string) bool {
	l.mu.Lock()
	for _, a := range l.configAcks {
		if a.ReasonCode == reason {
			l.mu.Unlock()
			return true
		}
	}
	l.mu.Unlock()
	return false
}

func (l *fakeLink) onlineReports() []*nodev1.OnlineIPs {
	l.mu.Lock()
	out := append([]*nodev1.OnlineIPs(nil), l.online...)
	l.mu.Unlock()
	return out
}

func (l *fakeLink) configAckPhases(nodeID string) []nodev1.ConfigPhase {
	l.mu.Lock()
	var out []nodev1.ConfigPhase
	for _, a := range l.configAcks {
		if a.NodeId == nodeID {
			out = append(out, a.Phase)
		}
	}
	l.mu.Unlock()
	return out
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal(msg)
}

func startSim(t *testing.T) (*Sim, *fakeLink, *Board, context.CancelFunc) {
	t.Helper()
	board := NewBoard("", nil)
	sim := NewSim(SimOptions{
		Name:           simName,
		Board:          board,
		ReportInterval: 20 * time.Millisecond,
		TrafficUsers:   5,
	})
	link := newFakeLink()
	ctx, cancel := context.WithCancel(context.Background())
	go sim.Run(ctx, link)
	return sim, link, board, cancel
}

func nodeConfigJSON(t *testing.T, id, nodeType string, port int) *nodev1.NodeConfig {
	t.Helper()
	body, err := json.Marshal(map[string]any{"server_port": port})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return &nodev1.NodeConfig{
		NodeId: id, NodeType: nodeType, ConfigJson: body,
		ContentSha256: hex.EncodeToString(sum[:]),
	}
}

func TestSimWelcomeHeartbeatAndBoard(t *testing.T) {
	_, link, board, cancel := startSim(t)
	defer cancel()

	link.events <- panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{
		SessionId: "sess-1", HeartbeatIntervalSec: 1,
	}}

	waitUntil(t, 2*time.Second, func() bool { return link.heartbeatCount() >= 1 }, "Welcome 后应立刻有心跳")
	// Board 的 Connected 在事件处理完之后的 publish 里写，等它落下
	waitUntil(t, 2*time.Second, func() bool {
		st := board.Snapshot().Servers[simName]
		return st != nil && st.Connected && st.Epoch == 1
	}, "Board 应为已连接、Epoch=1")
}

func TestSimConfigTwoNodesThenPrune(t *testing.T) {
	sim, link, board, cancel := startSim(t)
	defer cancel()

	link.events <- panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{SessionId: "s"}}
	waitUntil(t, 2*time.Second, func() bool { return link.heartbeatCount() >= 1 }, "等 Welcome 处理")

	n1 := nodeConfigJSON(t, "n1", "vless", 20001)
	n2 := nodeConfigJSON(t, "n2", "trojan", 20002)
	link.events <- panel.Event{Kind: panel.EventConfig, Config: &nodev1.ServerConfig{Nodes: []*nodev1.NodeConfig{n1, n2}}}

	waitUntil(t, 2*time.Second, func() bool {
		p1, p2 := link.configAckPhases("n1"), link.configAckPhases("n2")
		return len(p1) >= 2 && len(p2) >= 2
	}, "应收到各节点 SWITCHED 与 HEALTH_PASSED")

	for _, id := range []string{"n1", "n2"} {
		phases := link.configAckPhases(id)
		if phases[0] != nodev1.ConfigPhase_CONFIG_PHASE_SWITCHED || phases[1] != nodev1.ConfigPhase_CONFIG_PHASE_HEALTH_PASSED {
			t.Fatalf("%s 回执阶段 = %v", id, phases)
		}
	}
	configs, _ := sim.Applied()
	if len(configs) != 2 {
		t.Fatalf("Applied configs = %d", len(configs))
	}

	link.events <- panel.Event{Kind: panel.EventConfig, Config: &nodev1.ServerConfig{Nodes: []*nodev1.NodeConfig{n1}}}
	waitUntil(t, 2*time.Second, func() bool {
		st := board.Snapshot().Servers[simName]
		return st != nil && st.Nodes["n2"] == nil && st.Nodes["n1"] != nil
	}, "n2 应从 Board 消失")
}

func TestSimConfigDigestMismatch(t *testing.T) {
	_, link, board, cancel := startSim(t)
	defer cancel()

	link.events <- panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{}}
	body := []byte(`{"server_port":1}`)
	bad := &nodev1.NodeConfig{
		NodeId: "n1", NodeType: "vless", ConfigJson: body, ContentSha256: "deadbeef",
	}
	link.events <- panel.Event{Kind: panel.EventConfig, Config: &nodev1.ServerConfig{Nodes: []*nodev1.NodeConfig{bad}}}

	waitUntil(t, 2*time.Second, func() bool { return link.hasConfigAckReason("digest_mismatch") }, "应有 digest_mismatch 回执")

	if board.Snapshot().Errors != 1 {
		t.Fatalf("Errors = %d", board.Snapshot().Errors)
	}
}

func TestSimUsersFullAndDelta(t *testing.T) {
	_, link, board, cancel := startSim(t)
	defer cancel()

	link.events <- panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{}}
	nc := nodeConfigJSON(t, "n1", "vless", 20001)
	link.events <- panel.Event{Kind: panel.EventConfig, Config: &nodev1.ServerConfig{Nodes: []*nodev1.NodeConfig{nc}}}
	waitUntil(t, 2*time.Second, func() bool { return len(link.configAckPhases("n1")) >= 2 }, "等配置")

	full := &panel.UsersFull{
		NodeID: "n1", Version: "v3",
		Users: []*nodev1.User{
			{Uid: 1, Uuid: "ccc"}, {Uid: 2, Uuid: "aaa"}, {Uid: 3, Uuid: "bbb"},
		},
	}
	link.events <- panel.Event{Kind: panel.EventUsersFull, UsersFull: full}
	waitUntil(t, 2*time.Second, func() bool {
		st := board.Snapshot().Servers[simName]
		return st != nil && st.Nodes["n1"] != nil && st.Nodes["n1"].RosterVersion == "v3" && len(st.Nodes["n1"].Users) == 3
	}, "等 UsersFull")

	wantUUIDs := []string{"aaa", "bbb", "ccc"}
	if got := board.Snapshot().Servers[simName].Nodes["n1"].Users; !sort.StringsAreSorted(got) || len(got) != 3 {
		t.Fatalf("Users = %v", got)
	}
	for i, u := range wantUUIDs {
		if board.Snapshot().Servers[simName].Nodes["n1"].Users[i] != u {
			t.Fatalf("Users = %v want %v", board.Snapshot().Servers[simName].Nodes["n1"].Users, wantUUIDs)
		}
	}

	errBefore := board.Snapshot().Errors
	link.events <- panel.Event{Kind: panel.EventUsersDelta, UsersDelta: &nodev1.UsersDelta{
		NodeId: "n1", FromVersion: "v3", ToVersion: "v4",
		Upserted: []*nodev1.User{{Uid: 4, Uuid: "ddd"}}, RemovedUids: []int64{2},
	}}
	waitUntil(t, 2*time.Second, func() bool {
		st := board.Snapshot().Servers[simName]
		if st == nil || st.Nodes["n1"] == nil {
			return false
		}
		n := st.Nodes["n1"]
		return n.RosterVersion == "v4" && len(n.Users) == 3 && n.Users[2] == "ddd"
	}, "等增量 ok")

	link.events <- panel.Event{Kind: panel.EventUsersDelta, UsersDelta: &nodev1.UsersDelta{
		NodeId: "n1", FromVersion: "v0", ToVersion: "v5",
	}}
	waitUntil(t, 2*time.Second, func() bool {
		st := board.Snapshot().Servers[simName]
		return st != nil && st.VersionMismatches == 1
	}, "等 version_mismatch")
	if board.Snapshot().Errors != errBefore {
		t.Fatalf("Errors 不应增加: before=%d after=%d", errBefore, board.Snapshot().Errors)
	}
}

func TestSimTrafficResendAndAck(t *testing.T) {
	_, link, board, cancel := startSim(t)
	defer cancel()

	link.events <- panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{}}
	nc := nodeConfigJSON(t, "n1", "vless", 20001)
	link.events <- panel.Event{Kind: panel.EventConfig, Config: &nodev1.ServerConfig{Nodes: []*nodev1.NodeConfig{nc}}}
	link.events <- panel.Event{Kind: panel.EventUsersFull, UsersFull: &panel.UsersFull{
		NodeID: "n1", Version: "v1",
		Users: []*nodev1.User{{Uid: 10, Uuid: "u10"}},
	}}

	var firstID string
	waitUntil(t, 2*time.Second, func() bool {
		rep := link.lastTraffic()
		if rep == nil || len(rep.Entries) == 0 {
			return false
		}
		e := rep.Entries[0]
		if e.NodeId != "n1" || e.Uid != 10 || e.Upload != 4096 || e.Download != 16384 {
			return false
		}
		firstID = rep.ReportId
		return firstID != ""
	}, "等流量上报")

	waitUntil(t, 2*time.Second, func() bool {
		if len(link.trafficReports()) < 2 {
			return false
		}
		prev := link.trafficReports()
		return prev[len(prev)-1].ReportId == firstID && prev[len(prev)-2].ReportId == firstID
	}, "未 ack 时应重发同一 report_id")

	on := link.onlineReports()
	if len(on) == 0 {
		t.Fatal("应有 OnlineIPs")
	}
	lastOn := on[len(on)-1]
	if len(lastOn.Entries) != 1 || lastOn.Entries[0].Ips[0] != ipv4(10) || lastOn.Entries[0].Ips[1] != ipv6(10) {
		t.Fatalf("在线地址 = %v", lastOn.Entries)
	}

	link.events <- panel.Event{Kind: panel.EventTrafficAck, ReportID: firstID}
	waitUntil(t, 2*time.Second, func() bool {
		st := board.Snapshot().Servers[simName]
		return st != nil && st.TrafficAcked == 1
	}, "等 TrafficAcked")

	waitUntil(t, 2*time.Second, func() bool {
		rep := link.lastTraffic()
		return rep != nil && rep.ReportId != "" && rep.ReportId != firstID
	}, "ack 后下一份 report_id 应不同")
}

func TestSimTombstone(t *testing.T) {
	_, link, board, cancel := startSim(t)
	defer cancel()

	link.events <- panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{}}
	nc := nodeConfigJSON(t, "n1", "vless", 20001)
	link.events <- panel.Event{Kind: panel.EventConfig, Config: &nodev1.ServerConfig{Nodes: []*nodev1.NodeConfig{nc}}}
	waitUntil(t, 2*time.Second, func() bool {
		st := board.Snapshot().Servers[simName]
		return st != nil && st.Nodes["n1"] != nil
	}, "等节点")

	link.events <- panel.Event{Kind: panel.EventTombstone, Reason: "revoked"}
	waitUntil(t, 2*time.Second, func() bool {
		st := board.Snapshot().Servers[simName]
		return st != nil && st.Tombstoned && !st.Connected && st.Errors >= 1 && len(st.Nodes) == 0
	}, "等 Tombstone 状态")
}

func TestSimDisconnected(t *testing.T) {
	_, link, board, cancel := startSim(t)
	defer cancel()

	link.events <- panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{}}
	waitUntil(t, 2*time.Second, func() bool {
		st := board.Snapshot().Servers[simName]
		return st != nil && st.Connected
	}, "等连接")

	link.events <- panel.Event{Kind: panel.EventDisconnected, Err: errors.New("reset")}
	waitUntil(t, 2*time.Second, func() bool {
		st := board.Snapshot().Servers[simName]
		return st != nil && !st.Connected && st.Errors >= 1
	}, "等断开状态")
}

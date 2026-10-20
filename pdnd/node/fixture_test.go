package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"testing"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// 编排层测试的公共夹具：假会话 fakeLink、按入站记账的假内核 fakeKernel、构造面板消息的帮手。
// 测试直接调 s.handleEvent / s.report / s.housekeeping 等，在测试 goroutine 里串行驱动，
// 与主循环单 goroutine 的语义一致，不起 Run。

// testServerID 是测试用的服务器身份。
const testServerID = "00000000-0000-4000-8000-000000000001"

// testLogger 丢弃所有输出：这些测试关心的是行为，不是日志内容。
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testClock 是可拨动的时钟。
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }
func newTestClock() *testClock               { return &testClock{t: time.Unix(1_800_000_000, 0)} }
func testMetrics() *nodev1.HostMetrics {
	return &nodev1.HostMetrics{CpuBp: 1234, MemTotalBytes: 1 << 30}
}
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	must(t, err)
	return b
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// ---------------------------------------------------------------------------
// 假会话
// ---------------------------------------------------------------------------

// fakeLink 实现 Link：记下编排层发出的每条消息。down 为真时 Send* 返回 false（发送队列满或流已断）。
type fakeLink struct {
	events chan panel.Event

	mu             sync.Mutex
	down           bool
	heartbeats     []*nodev1.Heartbeat
	traffic        []*nodev1.TrafficReport
	online         []*nodev1.OnlineIPs
	configAcks     []*nodev1.ConfigAck
	usersAcks      []*nodev1.UsersAck
	upgradeReports []*nodev1.UpgradeReport
}

func newFakeLink() *fakeLink { return &fakeLink{events: make(chan panel.Event, 64)} }

func (l *fakeLink) Events() <-chan panel.Event { return l.events }

func (l *fakeLink) record(f func()) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.down {
		return false
	}
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
func (l *fakeLink) SendUpgradeReport(m *nodev1.UpgradeReport) bool {
	return l.record(func() { l.upgradeReports = append(l.upgradeReports, m) })
}

func (l *fakeLink) setDown(down bool) { l.mu.Lock(); l.down = down; l.mu.Unlock() }

func (l *fakeLink) upgrades() []*nodev1.UpgradeReport {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*nodev1.UpgradeReport(nil), l.upgradeReports...)
}

// acks 返回某节点收到的配置回执，格式 "<阶段>:<sha 前 8 位>[:原因]"，便于比对。
func (l *fakeLink) acks(nodeID string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, a := range l.configAcks {
		if a.NodeId != nodeID {
			continue
		}
		s := a.Phase.String() + ":" + a.ContentSha256[:8]
		if a.ReasonCode != "" {
			s += ":" + a.ReasonCode
		}
		out = append(out, s)
	}
	return out
}

// usersAcksOf 返回某节点的名单回执，格式 "<版本>:ok" 或 "<版本>:<原因>"。
func (l *fakeLink) usersAcksOf(nodeID string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, a := range l.usersAcks {
		if a.NodeId != nodeID {
			continue
		}
		if a.Ok {
			out = append(out, a.Version+":ok")
		} else {
			out = append(out, a.Version+":"+a.ReasonCode)
		}
	}
	return out
}

func (l *fakeLink) lastHeartbeat() *nodev1.Heartbeat {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.heartbeats) == 0 {
		return nil
	}
	return l.heartbeats[len(l.heartbeats)-1]
}

func (l *fakeLink) trafficReports() []*nodev1.TrafficReport {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*nodev1.TrafficReport(nil), l.traffic...)
}

// ---------------------------------------------------------------------------
// 假内核：按入站 tag 记账
// ---------------------------------------------------------------------------

// fakeInbound 是假内核里的一个入站。
type fakeInbound struct {
	cfg     core.InboundConfig
	routing *core.Routing
	users   map[string]core.User // 按 UUID
	traffic []core.UserTraffic
	online  map[int64][]string
}

// fakeKernel 模拟 NativeCore 最关键的几点：整代装入站（ApplyInbound：建入站、设分流，分流被拒就撤掉入站）
// 清空该入站的用户表；ApplyInboundWithUsers 装好入站后随即装名单；同一端口先登记者赢；GetTraffic 取出即清零。
type fakeKernel struct {
	mu       sync.Mutex
	inbounds map[string]*fakeInbound
	// owners 是登记的入站归属（SetInboundOwner），端口冲突原因里写占用者节点 id 用。
	owners map[string]string
	// failAddInbound / failSetRouting / failAddUsers 不为 0 时对应调用报错（每次减一）。
	failAddInbound int
	failSetRouting int
	failAddUsers   int
	// notReady 为真时 InboundReady 报错。
	notReady bool
	// addInboundCalls 计数（看有没有重装）。
	addInboundCalls int
	// preload 是每次随入站装的名单，按调用顺序。
	preload [][]core.User
}

func newFakeKernel() *fakeKernel {
	return &fakeKernel{inbounds: make(map[string]*fakeInbound), owners: make(map[string]string)}
}

// portInUseError 是端口冲突，带内核口径的机器可读原因。
type portInUseError struct{ reason string }

func (e *portInUseError) Error() string         { return "端口被占用: " + e.reason }
func (e *portInUseError) RuntimeReason() string { return e.reason }

// usersPreloadError 是「入站已就绪、名单没装上」（对应 kernel.UsersPreloadError）。
type usersPreloadError struct{ err error }

func (e *usersPreloadError) Error() string            { return "装名单失败: " + e.err.Error() }
func (e *usersPreloadError) Unwrap() error            { return e.err }
func (e *usersPreloadError) UsersPreloadFailed() bool { return true }

func (k *fakeKernel) Type() string                { return "fake" }
func (k *fakeKernel) Start(context.Context) error { return nil }
func (k *fakeKernel) Close() error                { return nil }

func (k *fakeKernel) AddInbound(cfg *core.InboundConfig) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.addInboundCalls++
	if k.failAddInbound > 0 {
		k.failAddInbound--
		return errors.New("inbound rejected")
	}
	for tag, in := range k.inbounds {
		if tag != cfg.Tag && in.cfg.Port == cfg.Port {
			owner := k.owners[tag]
			if owner == "" {
				owner = "other"
			}
			return &portInUseError{reason: fmt.Sprintf("port_in_use:%d/tcp:%s", cfg.Port, owner)}
		}
	}
	k.inbounds[cfg.Tag] = &fakeInbound{cfg: *cfg, users: make(map[string]core.User)}
	return nil
}

func (k *fakeKernel) DelInbound(tag string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.inbounds, tag)
	return nil
}

func (k *fakeKernel) SetRouting(tag string, r *core.Routing) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failSetRouting > 0 {
		k.failSetRouting--
		return errors.New("rejected routing")
	}
	if in := k.inbounds[tag]; in != nil {
		in.routing = r
	}
	return nil
}

// ApplyInbound 按 NativeCore 口径整代装入站：建入站、设分流；分流被拒就撤掉新入站。
func (k *fakeKernel) ApplyInbound(cfg *core.InboundConfig, routing *core.Routing) error {
	if err := k.AddInbound(cfg); err != nil {
		return err
	}
	if err := k.SetRouting(cfg.Tag, routing); err != nil {
		_ = k.DelInbound(cfg.Tag)
		return err
	}
	return nil
}

// ApplyInboundWithUsers 先整代装入站，再把名单装进去并记进 preload。
// 名单装不上时入站已在服务，按 NativeCore 口径回 usersPreloadError。
func (k *fakeKernel) ApplyInboundWithUsers(cfg *core.InboundConfig, routing *core.Routing, users []core.User) error {
	if err := k.ApplyInbound(cfg, routing); err != nil {
		return err
	}
	k.mu.Lock()
	k.preload = append(k.preload, append([]core.User(nil), users...))
	k.mu.Unlock()
	if len(users) == 0 {
		return nil
	}
	if err := k.AddUsers(cfg.Tag, users); err != nil {
		return &usersPreloadError{err: err}
	}
	return nil
}

func (k *fakeKernel) AddUsers(tag string, users []core.User) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.failAddUsers > 0 {
		k.failAddUsers--
		return errors.New("rejected users")
	}
	in := k.inbounds[tag]
	if in == nil {
		return fmt.Errorf("入站 %s 不存在", tag)
	}
	for _, u := range users {
		in.users[u.UUID] = u
	}
	return nil
}

func (k *fakeKernel) UpsertUsers(tag string, users []core.User) error { return k.AddUsers(tag, users) }

func (k *fakeKernel) DelUsers(tag string, uuids []string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if in := k.inbounds[tag]; in != nil {
		for _, id := range uuids {
			delete(in.users, id)
		}
	}
	return nil
}

func (k *fakeKernel) GetTraffic(tag string) ([]core.UserTraffic, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	in := k.inbounds[tag]
	if in == nil {
		return nil, nil
	}
	out := in.traffic
	in.traffic = nil
	return out, nil
}

func (k *fakeKernel) OnlineIPs(tag string) map[int64][]string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if in := k.inbounds[tag]; in != nil {
		return in.online
	}
	return nil
}

func (k *fakeKernel) InboundReady(tag string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.notReady || k.inbounds[tag] == nil {
		return errors.New("inbound is not ready")
	}
	return nil
}

func (k *fakeKernel) SetInboundOwner(tag, _ string, nodeID string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.owners[tag] = nodeID
}

// feed 给某入站记一笔流量（下次 GetTraffic 取走）。
func (k *fakeKernel) feed(tag string, t ...core.UserTraffic) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if in := k.inbounds[tag]; in != nil {
		in.traffic = append(in.traffic, t...)
	}
}

// setOnline 设某入站的在线地址。
func (k *fakeKernel) setOnline(tag string, online map[int64][]string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if in := k.inbounds[tag]; in != nil {
		in.online = online
	}
}

// uuids 返回某入站内核里此刻真实存在的用户，排好序便于比对；入站不存在返回 nil。
func (k *fakeKernel) uuids(tag string) []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	in := k.inbounds[tag]
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in.users))
	for id := range in.users {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// port 返回某入站的端口，入站不存在返回 0。
func (k *fakeKernel) port(tag string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	if in := k.inbounds[tag]; in != nil {
		return in.cfg.Port
	}
	return 0
}

// protocol 返回某入站的协议。
func (k *fakeKernel) protocol(tag string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	if in := k.inbounds[tag]; in != nil {
		return in.cfg.Protocol
	}
	return ""
}

// ---------------------------------------------------------------------------
// 构造 Server 与面板消息
// ---------------------------------------------------------------------------

// testServer 把 Server、假会话、假时钟放在一起。
type testServer struct {
	*Server
	link  *fakeLink
	clock *testClock
}

// newTestServer 建 Server 并接上假会话（未 Welcome）。cacheDir 为空即不落盘。
func newTestServer(t *testing.T, kernel core.Core, cacheDir string) *testServer {
	t.Helper()
	clock := newTestClock()
	s := New(Options{
		ServerID: testServerID, Kernel: kernel, Logger: testLogger(), CacheDir: cacheDir,
		Now: clock.now, HostMetrics: testMetrics,
	})
	link := newFakeLink()
	s.link = link
	return &testServer{Server: s, link: link, clock: clock}
}

// welcome 模拟新流建立。
func (ts *testServer) welcome() {
	ts.handleEvent(panel.Event{Kind: panel.EventWelcome, Welcome: &nodev1.Welcome{SessionId: "s", PushIntervalSec: 60, HeartbeatIntervalSec: 30}})
}

// disconnect 模拟流断开。
func (ts *testServer) disconnect() {
	ts.handleEvent(panel.Event{Kind: panel.EventDisconnected, Err: errors.New("stream closed")})
}

// config 下发一份 ServerConfig（全量）。
func (ts *testServer) config(nodes ...*nodev1.NodeConfig) {
	ts.handleEvent(panel.Event{Kind: panel.EventConfig, Config: &nodev1.ServerConfig{ReleaseId: "r", Nodes: nodes}})
}

// usersFull 下发某节点的全量名单，用户号依次是 1、2、3…，UUID 即参数。
func (ts *testServer) usersFull(nodeID, version string, uuids ...string) {
	ts.handleEvent(usersFullEvent(nodeID, version, protoUsers(uuids...)))
}

// usersFullEvent 是一份已收齐的全量名单事件。
func usersFullEvent(nodeID, version string, users []*nodev1.User) panel.Event {
	return panel.Event{Kind: panel.EventUsersFull, UsersFull: &panel.UsersFull{NodeID: nodeID, Version: version, Users: users}}
}

// usersDelta 下发名单增量。upserted 的用户号由调用方给（protoUser）。
func (ts *testServer) usersDelta(nodeID, from, to string, upserted []*nodev1.User, removed ...int64) {
	ts.handleEvent(panel.Event{Kind: panel.EventUsersDelta, UsersDelta: &nodev1.UsersDelta{
		NodeId: nodeID, FromVersion: from, ToVersion: to, Upserted: upserted, RemovedUids: removed,
	}})
}

// node 取节点（不存在就失败）。
func (ts *testServer) node(t *testing.T, id string) *Node {
	t.Helper()
	n := ts.nodes[id]
	if n == nil {
		t.Fatalf("节点 %s 不存在", id)
	}
	return n
}

func protoUser(uid int64, uuid string) *nodev1.User { return &nodev1.User{Uid: uid, Uuid: uuid} }

func protoUsers(uuids ...string) []*nodev1.User {
	out := make([]*nodev1.User, 0, len(uuids))
	for i, id := range uuids {
		out = append(out, protoUser(int64(i+1), id))
	}
	return out
}

// nodeConfig 按配置文档造一份 NodeConfig（摘要按 json.Marshal 的字节算）。
func nodeConfig(t *testing.T, id, nodeType string, doc map[string]any) *nodev1.NodeConfig {
	t.Helper()
	body := mustJSON(t, doc)
	return &nodev1.NodeConfig{NodeId: id, NodeType: nodeType, ConfigJson: body, ContentSha256: sha(body)}
}

// portConfig 是最简单的配置文档：只有端口。
func portConfig(t *testing.T, id, nodeType string, port int) *nodev1.NodeConfig {
	t.Helper()
	return nodeConfig(t, id, nodeType, map[string]any{"server_port": port})
}

// assertStrings 比对两个字符串切片（顺序敏感）。
func assertStrings(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v，期望 %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v，期望 %v", what, got, want)
		}
	}
}

// trafficOf 造一条内核流量。
func trafficOf(uid, up, down int64) core.UserTraffic {
	return core.UserTraffic{ID: uid, Upload: up, Download: down}
}

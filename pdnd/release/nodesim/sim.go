package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aegispanel/nodeagent/panel"
	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"
)

const (
	defaultReportInterval = 10 * time.Second
	defaultTrafficUsers   = 5
	defaultHeartbeatEvery = 30 * time.Second
	maxTrafficEntries     = 5000
	maxOnlineEntries      = 5000
)

// Link 是模拟器看到的会话，*panel.Session 实现它；测试用假的。方法与 pdnd/node.Link 相同。
type Link interface {
	Events() <-chan panel.Event
	SendHeartbeat(*nodev1.Heartbeat) bool
	SendTraffic(*nodev1.TrafficReport) bool
	SendOnline(*nodev1.OnlineIPs) bool
	SendConfigAck(*nodev1.ConfigAck) bool
	SendUsersAck(*nodev1.UsersAck) bool
}

// SimOptions 是一台模拟服务器的参数。
type SimOptions struct {
	Name  string // 服务器名（Board 的键）
	Board *Board
	// ReportInterval 是流量与在线的上报节拍（CI 用短的压缩时间），<=0 用 10 秒。面板 Welcome 里的 push_interval_sec 不采用。
	ReportInterval time.Duration
	// TrafficUsers 是每个节点每轮上报几个用户，<=0 用 5。
	TrafficUsers    int
	Logger          *slog.Logger     // nil 用 slog.Default()
	Now             func() time.Time // nil 用 time.Now
	OmitStatusUsers bool             // 为真时 publish 不写 NodeStatus.Users
	// Spread 为真时按真实节点的形态错开：首轮上报在一个间隔内随机，之后上报与心跳每轮 ±10% 抖动
	// （同 pdnd node/server.go 的 Run：同一批接入的节点几轮之后就不再踩同一秒打面板）；
	// 每个节点从按服务器名散列出的位置开始轮报用户，不让所有服务器同时报同一批人。
	Spread bool
}

// simNode 是一个节点的本地镜像（只在 Run 的 goroutine 里访问）。
type simNode struct {
	nodeType        string
	configSHA       string
	rosterVersion   string
	rosterAppliedAt time.Time
	fulls           int
	deltas          int
	history         []RosterApply
	users           map[int64]*nodev1.User
	cursor          int
	uids            []int64  // 排好序的 uid 缓存，nil 表示要重算
	uuids           []string // 排好序的 uuid 缓存，nil 表示要重算
}

// Sim 是一台模拟服务器的编排：全部状态只在 Run 的 goroutine 里改；Applied 的快照单独加锁。
type Sim struct {
	name           string
	board          *Board
	log            *slog.Logger
	reportInterval time.Duration
	trafficUsers   int
	now            func() time.Time
	omitUsers      bool
	spread         bool
	startedAt      time.Time

	connected      bool
	nodes          map[string]*simNode
	order          []string
	heartbeatEvery time.Duration
	inflight       *nodev1.TrafficReport

	appliedMu sync.Mutex
	applied   appliedSnap
}

type appliedSnap struct {
	configs []*nodev1.AppliedConfig
	users   []*nodev1.AppliedUsers
}

// NewSim 建一台模拟服务器的编排。
func NewSim(o SimOptions) *Sim {
	ri := o.ReportInterval
	if ri <= 0 {
		ri = defaultReportInterval
	}
	u := o.TrafficUsers
	if u <= 0 {
		u = defaultTrafficUsers
	}
	log := o.Logger
	if log == nil {
		log = slog.Default()
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	return &Sim{
		name:           o.Name,
		board:          o.Board,
		log:            log,
		reportInterval: ri,
		trafficUsers:   u,
		now:            now,
		omitUsers:      o.OmitStatusUsers,
		spread:         o.Spread,
		startedAt:      now(),
		nodes:          make(map[string]*simNode),
		heartbeatEvery: defaultHeartbeatEvery,
	}
}

// Applied 返回 Hello 要报的已应用版本（panel.AppliedFunc），并发安全。
func (s *Sim) Applied() ([]*nodev1.AppliedConfig, []*nodev1.AppliedUsers) {
	s.appliedMu.Lock()
	defer s.appliedMu.Unlock()
	return s.applied.configs, s.applied.users
}

// Run 是主循环，一直跑到 ctx 结束。
func (s *Sim) Run(ctx context.Context, link Link) {
	heartbeat := time.NewTimer(s.nextHeartbeat())
	first := s.reportInterval
	if s.spread {
		first = time.Duration(rand.Int64N(int64(s.reportInterval))) + time.Millisecond
	}
	report := time.NewTimer(first)
	defer heartbeat.Stop()
	defer report.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-link.Events():
			s.handleEvent(link, ev, heartbeat)
		case <-heartbeat.C:
			s.tickHeartbeat(link)
			heartbeat.Reset(s.nextHeartbeat())
		case <-report.C:
			s.tickReport(link)
			report.Reset(s.nextReport())
		}
		s.publish()
	}
}

func (s *Sim) handleEvent(link Link, ev panel.Event, heartbeat *time.Timer) {
	switch ev.Kind {
	case panel.EventWelcome:
		s.onWelcome(link, ev.Welcome, heartbeat)
	case panel.EventConfig:
		s.onConfig(link, ev.Config)
	case panel.EventUsersFull:
		s.onUsersFull(link, ev.UsersFull)
	case panel.EventUsersDelta:
		s.onUsersDelta(link, ev.UsersDelta)
	case panel.EventTrafficAck:
		s.onTrafficAck(ev.ReportID)
	case panel.EventDisconnected:
		s.connected = false
		s.board.RecordError(s.name, fmt.Errorf("会话断开: %v", ev.Err))
		s.board.Update(s.name, func(st *ServerStatus) { st.DisconnectedAt = s.now() })
	case panel.EventTombstone:
		s.onTombstone(ev.Reason)
	}
}

func (s *Sim) onWelcome(link Link, w *nodev1.Welcome, heartbeat *time.Timer) {
	s.connected = true
	t := s.now()
	s.board.Update(s.name, func(st *ServerStatus) {
		st.ConnectedAt = t
		if st.FirstConnectedAt.IsZero() {
			st.FirstConnectedAt = t
		}
	})
	if w != nil {
		s.board.Update(s.name, func(st *ServerStatus) {
			st.Epoch++
			st.SessionID = w.GetSessionId()
		})
		if v := w.GetHeartbeatIntervalSec(); v > 0 {
			s.heartbeatEvery = time.Duration(v) * time.Second
			heartbeat.Reset(s.nextHeartbeat())
		}
	}
	s.sendHeartbeat(link)
	if s.inflight != nil {
		if link.SendTraffic(cloneTrafficReport(s.inflight)) {
			s.board.Update(s.name, func(st *ServerStatus) { st.TrafficSent++ })
		}
	}
}

func (s *Sim) onConfig(link Link, cfg *nodev1.ServerConfig) {
	if cfg == nil {
		return
	}
	s.board.Update(s.name, func(st *ServerStatus) { st.ReleaseID = cfg.GetReleaseId() })
	seen := make(map[string]bool)
	order := make([]string, 0, len(cfg.GetNodes()))
	for _, nc := range cfg.GetNodes() {
		id := nc.GetNodeId()
		if id == "" {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		order = append(order, id)
		n := s.nodes[id]
		if n == nil {
			n = &simNode{users: make(map[int64]*nodev1.User)}
			s.nodes[id] = n
		}
		sha := strings.ToLower(nc.GetContentSha256())
		jsonBytes := nc.GetConfigJson()
		if contentDigest(jsonBytes) != sha {
			err := errors.New("配置内容与 content_sha256 不符")
			s.board.RecordError(s.name, err)
			if link.SendConfigAck(&nodev1.ConfigAck{
				NodeId: id, ContentSha256: sha, Phase: nodev1.ConfigPhase_CONFIG_PHASE_FAILED,
				ReasonCode: "digest_mismatch",
			}) {
				s.board.Update(s.name, func(st *ServerStatus) { st.ConfigAcks++ })
			}
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal(jsonBytes, &obj); err != nil || obj == nil {
			e := fmt.Errorf("配置不是合法的 JSON 对象: %w", err)
			s.board.RecordError(s.name, e)
			if link.SendConfigAck(&nodev1.ConfigAck{
				NodeId: id, ContentSha256: sha, Phase: nodev1.ConfigPhase_CONFIG_PHASE_FAILED,
				ReasonCode: "invalid_json",
			}) {
				s.board.Update(s.name, func(st *ServerStatus) { st.ConfigAcks++ })
			}
			continue
		}
		n.nodeType = strings.TrimSpace(nc.GetNodeType())
		n.configSHA = sha
		s.sendConfigPhase(link, id, sha, nodev1.ConfigPhase_CONFIG_PHASE_SWITCHED)
		s.sendConfigPhase(link, id, sha, nodev1.ConfigPhase_CONFIG_PHASE_HEALTH_PASSED)
	}
	for id := range s.nodes {
		if !seen[id] {
			delete(s.nodes, id)
		}
	}
	s.order = order
	s.refreshApplied()
}

func (s *Sim) sendConfigPhase(link Link, nodeID, sha string, phase nodev1.ConfigPhase) {
	if link.SendConfigAck(&nodev1.ConfigAck{NodeId: nodeID, ContentSha256: sha, Phase: phase}) {
		s.board.Update(s.name, func(st *ServerStatus) { st.ConfigAcks++ })
	}
}

func (n *simNode) setRoster(version, via string, at time.Time) {
	if version == n.rosterVersion {
		return
	}
	n.rosterVersion = version
	n.rosterAppliedAt = at
	n.history = append(n.history, RosterApply{Version: version, AppliedAt: at, Via: via})
	if len(n.history) > rosterHistoryMax {
		n.history = n.history[len(n.history)-rosterHistoryMax:]
	}
}

func (n *simNode) invalidateUserCaches() {
	n.uids = nil
	n.uuids = nil
}

func (s *Sim) onUsersFull(link Link, full *panel.UsersFull) {
	if full == nil || full.NodeID == "" {
		return
	}
	n := s.nodes[full.NodeID]
	if n == nil {
		n = &simNode{users: make(map[int64]*nodev1.User)}
		s.nodes[full.NodeID] = n
	}
	n.users = make(map[int64]*nodev1.User)
	for _, u := range full.Users {
		if u != nil {
			n.users[u.GetUid()] = u
		}
	}
	n.invalidateUserCaches()
	n.fulls++
	n.setRoster(full.Version, "full", s.now())
	n.cursor = s.startCursor(full.NodeID, len(n.users))
	if link.SendUsersAck(&nodev1.UsersAck{NodeId: full.NodeID, Version: full.Version, Ok: true}) {
		s.board.Update(s.name, func(st *ServerStatus) { st.UsersAcks++ })
	}
	s.refreshApplied()
}

func (s *Sim) onUsersDelta(link Link, d *nodev1.UsersDelta) {
	if d == nil {
		return
	}
	nodeID := d.GetNodeId()
	toVer := d.GetToVersion()
	n := s.nodes[nodeID]
	if n != nil {
		n.deltas++
	}
	if n == nil || n.rosterVersion == "" || d.GetFromVersion() != n.rosterVersion {
		if link.SendUsersAck(&nodev1.UsersAck{
			NodeId: nodeID, Version: toVer, Ok: false, ReasonCode: panel.UsersReasonVersionMismatch,
		}) {
			s.board.Update(s.name, func(st *ServerStatus) {
				st.UsersAcks++
				st.VersionMismatches++
			})
		}
		return
	}
	for _, uid := range d.GetRemovedUids() {
		delete(n.users, uid)
	}
	for _, u := range d.GetUpserted() {
		if u != nil {
			n.users[u.GetUid()] = u
		}
	}
	n.invalidateUserCaches()
	n.setRoster(toVer, "delta", s.now())
	if link.SendUsersAck(&nodev1.UsersAck{NodeId: nodeID, Version: toVer, Ok: true}) {
		s.board.Update(s.name, func(st *ServerStatus) { st.UsersAcks++ })
	}
	s.refreshApplied()
}

func (s *Sim) onTrafficAck(reportID string) {
	if s.inflight == nil || s.inflight.GetReportId() != reportID {
		return
	}
	var bytes int64
	for _, e := range s.inflight.GetEntries() {
		bytes += int64(e.GetUpload() + e.GetDownload())
	}
	s.inflight = nil
	s.board.Update(s.name, func(st *ServerStatus) {
		st.TrafficAcked++
		st.TrafficBytes += bytes
	})
}

func (s *Sim) onTombstone(reason string) {
	s.connected = false
	s.nodes = make(map[string]*simNode)
	s.order = nil
	s.inflight = nil
	s.board.Update(s.name, func(st *ServerStatus) { st.Tombstoned = true })
	s.board.RecordError(s.name, fmt.Errorf("Tombstone: %s", reason))
	s.refreshApplied()
}

func (s *Sim) tickHeartbeat(link Link) {
	s.sendHeartbeat(link)
}

func (s *Sim) sendHeartbeat(link Link) {
	if !s.connected {
		return
	}
	uptime := uint64(time.Since(s.startedAt).Seconds())
	var nodes []*nodev1.NodeRuntime
	for _, id := range s.order {
		n := s.nodes[id]
		if n == nil || n.configSHA == "" {
			continue
		}
		nodes = append(nodes, &nodev1.NodeRuntime{
			NodeId: id,
			Status: nodev1.RuntimeStatus_RUNTIME_STATUS_RUNNING,
		})
	}
	if !link.SendHeartbeat(&nodev1.Heartbeat{
		Metrics: &nodev1.HostMetrics{
			CpuBp:          300,
			MemUsedBytes:   256 << 20,
			MemTotalBytes:  1 << 30,
			DiskUsedBytes:  2 << 30,
			DiskTotalBytes: 20 << 30,
			UptimeSec:      uptime,
		},
		Nodes: nodes,
	}) {
		return
	}
	s.board.Update(s.name, func(st *ServerStatus) { st.HeartbeatsSent++ })
}

func (s *Sim) tickReport(link Link) {
	if !s.connected {
		return
	}
	if s.inflight != nil {
		if link.SendTraffic(cloneTrafficReport(s.inflight)) {
			s.board.Update(s.name, func(st *ServerStatus) { st.TrafficSent++ })
		}
		return
	}
	entries, online := s.buildTrafficAndOnline()
	if len(entries) == 0 {
		return
	}
	if len(entries) > maxTrafficEntries {
		entries = entries[:maxTrafficEntries]
	}
	if len(online) > maxOnlineEntries {
		online = online[:maxOnlineEntries]
	}
	rep := &nodev1.TrafficReport{ReportId: uuid.NewString(), Entries: entries}
	s.inflight = rep
	if link.SendTraffic(cloneTrafficReport(rep)) {
		s.board.Update(s.name, func(st *ServerStatus) { st.TrafficSent++ })
	}
	if link.SendOnline(&nodev1.OnlineIPs{Entries: online}) {
		s.board.Update(s.name, func(st *ServerStatus) { st.OnlineSent++ })
	}
}

func (s *Sim) buildTrafficAndOnline() ([]*nodev1.TrafficEntry, []*nodev1.OnlineEntry) {
	var entries []*nodev1.TrafficEntry
	var online []*nodev1.OnlineEntry
	for _, id := range s.order {
		n := s.nodes[id]
		if n == nil || len(n.users) == 0 {
			continue
		}
		if n.uids == nil {
			n.uids = sortedUIDs(n.users)
		}
		uids := n.uids
		if len(uids) == 0 {
			continue
		}
		k := min(s.trafficUsers, len(uids))
		for i := 0; i < k; i++ {
			idx := (n.cursor + i) % len(uids)
			uid := uids[idx]
			entries = append(entries, &nodev1.TrafficEntry{
				NodeId: id, Uid: uid, Upload: 4096, Download: 16384,
			})
			online = append(online, &nodev1.OnlineEntry{
				NodeId: id, Uid: uid, Ips: []string{ipv4(uid), ipv6(uid)},
			})
		}
		n.cursor = (n.cursor + k) % len(uids)
	}
	return entries, online
}

// nextReport 是下一轮上报的等待：Spread 时与真实节点一样 ±10% 抖动。
func (s *Sim) nextReport() time.Duration {
	if s.spread {
		return panel.Jitter(s.reportInterval)
	}
	return s.reportInterval
}

// nextHeartbeat 是下一次心跳的等待：Spread 时与真实节点一样 ±10% 抖动。
func (s *Sim) nextHeartbeat() time.Duration {
	if s.spread {
		return panel.Jitter(s.heartbeatEvery)
	}
	return s.heartbeatEvery
}

// startCursor 是全量名单后从第几个用户开始轮报：Spread 时按服务器名与节点散列，否则从 0。
func (s *Sim) startCursor(nodeID string, n int) int {
	if !s.spread || n <= 0 {
		return 0
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(s.name + "/" + nodeID))
	return int(h.Sum64() % uint64(n))
}

func sortedUIDs(users map[int64]*nodev1.User) []int64 {
	uids := make([]int64, 0, len(users))
	for uid := range users {
		uids = append(uids, uid)
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	return uids
}

func sortedUUIDs(users map[int64]*nodev1.User) []string {
	uids := sortedUIDs(users)
	out := make([]string, 0, len(uids))
	for _, uid := range uids {
		if u := users[uid]; u != nil && u.GetUuid() != "" {
			out = append(out, u.GetUuid())
		}
	}
	sort.Strings(out)
	return out
}

func ipv4(uid int64) string {
	return "203.0.113." + strconv.FormatInt(uid%254+1, 10)
}

func ipv6(uid int64) string {
	return fmt.Sprintf("2001:db8::%x", uid%0xffff+1)
}

func contentDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func cloneTrafficReport(r *nodev1.TrafficReport) *nodev1.TrafficReport {
	if r == nil {
		return nil
	}
	cp := &nodev1.TrafficReport{ReportId: r.GetReportId()}
	for _, e := range r.GetEntries() {
		cp.Entries = append(cp.Entries, &nodev1.TrafficEntry{
			NodeId: e.GetNodeId(), Uid: e.GetUid(), Upload: e.GetUpload(), Download: e.GetDownload(),
		})
	}
	return cp
}

func (s *Sim) refreshApplied() {
	var snap appliedSnap
	for _, id := range s.order {
		n := s.nodes[id]
		if n == nil {
			continue
		}
		if n.configSHA != "" {
			snap.configs = append(snap.configs, &nodev1.AppliedConfig{NodeId: id, ContentSha256: n.configSHA})
		}
		if n.configSHA != "" && n.rosterVersion != "" {
			snap.users = append(snap.users, &nodev1.AppliedUsers{NodeId: id, Version: n.rosterVersion})
		}
	}
	s.appliedMu.Lock()
	s.applied = snap
	s.appliedMu.Unlock()
}

func (s *Sim) publish() {
	s.refreshApplied()
	s.board.Update(s.name, func(st *ServerStatus) {
		st.Connected = s.connected
		st.Nodes = make(map[string]*NodeStatus)
		for _, id := range s.order {
			n := s.nodes[id]
			if n == nil {
				continue
			}
			var users []string
			if !s.omitUsers {
				if n.uuids == nil {
					n.uuids = sortedUUIDs(n.users)
				}
				if len(n.uuids) == 0 {
					users = []string{}
				} else {
					users = append([]string(nil), n.uuids...)
				}
			}
			var hist []RosterApply
			if len(n.history) > 0 {
				hist = append([]RosterApply(nil), n.history...)
			}
			st.Nodes[id] = &NodeStatus{
				NodeType:        n.nodeType,
				ConfigSHA256:    n.configSHA,
				RosterVersion:   n.rosterVersion,
				RosterAppliedAt: n.rosterAppliedAt,
				FullsReceived:   n.fulls,
				DeltasReceived:  n.deltas,
				RosterHistory:   hist,
				UserCount:       len(n.users),
				Users:           users,
			}
		}
	})
}

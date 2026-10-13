// Package node 是节点端的编排：把面板会话（panel.Session）与内核（core.Core）粘起来。
//
// 一台服务器一个 Server、一条会话，名下多个节点（入站）各是一个 Node。全部状态只在
// Server 的主循环一个 goroutine 里改：会话事件、上报节拍、心跳节拍、内务节拍在同一个
// select 里串行处理，用户镜像因此不加锁。唯一的例外是给 Hello 用的已应用版本快照
// （applied），会话在自己的 goroutine 里读它，单独加锁。
package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/panel"
)

// 节拍的缺省值；Welcome 带了就用面板给的。
const (
	defaultPushInterval      = 60 * time.Second
	defaultHeartbeatInterval = 30 * time.Second
	// housekeepingInterval 是内务节拍：装失败的重试、health_passed 回执都在这里看到点没有。
	housekeepingInterval = time.Second
)

// Link 是编排层看到的会话，*panel.Session 实现它；测试用假的。
type Link interface {
	Events() <-chan panel.Event
	SendHeartbeat(*nodev1.Heartbeat) bool
	SendTraffic(*nodev1.TrafficReport) bool
	SendOnline(*nodev1.OnlineIPs) bool
	SendConfigAck(*nodev1.ConfigAck) bool
	SendUsersAck(*nodev1.UsersAck) bool
}

// Options 是 Server 的参数。
type Options struct {
	// ServerID 是本机身份里的服务器 id；落盘缓存按它认归属，换了身份的旧缓存丢弃。
	ServerID string
	Kernel   core.Core
	Logger   *slog.Logger
	// CacheDir 是落盘缓存根目录；空串不落盘（测试用）。main 传状态目录（缺省 DefaultCacheDir）。
	CacheDir string
	// Now 只给测试替换，nil 即 time.Now。
	Now func() time.Time
	// HostMetrics 只给测试替换，nil 即 panel.CollectHostMetrics。
	HostMetrics func() *nodev1.HostMetrics
}

// Server 是一台服务器的编排。
type Server struct {
	serverID string
	// scope 是登记入站归属用的不透明串（服务器 id 的哈希）：同一台服务器的节点之间
	// 端口冲突时可以写出对方节点 id。
	scope  string
	kernel core.Core
	log    *slog.Logger
	cache  *serverCache
	now    func() time.Time
	metric func() *nodev1.HostMetrics

	// link 在 Run 里设上；Start（冷启动）时还没有会话。
	link Link
	// connected 为真表示当前流已收到 Welcome、还没断。
	connected bool

	// nodes 按节点 id 记；order 是面板 ServerConfig 里的顺序（冷启动时是缓存里的顺序），
	// 装入站、心跳、上报都按它走，同机端口冲突时赢家因此固定。
	nodes map[string]*Node
	order []string

	pushInterval      time.Duration
	heartbeatInterval time.Duration

	// traffic 是还没被面板确认的流量，见 report.go。
	traffic trafficBuffer

	appliedMu sync.Mutex
	applied   appliedSnapshot
}

// appliedSnapshot 是 Hello 要报的已应用版本，主循环改完节点状态后经 publishApplied 刷新。
type appliedSnapshot struct {
	configs []*nodev1.AppliedConfig
	users   []*nodev1.AppliedUsers
}

// New 建编排层。不碰内核、不读缓存；冷启动见 Start。
func New(o Options) *Server {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	metric := o.HostMetrics
	if metric == nil {
		metric = panel.CollectHostMetrics
	}
	sum := sha256.Sum256([]byte("pandora-server\n" + o.ServerID))
	s := &Server{
		serverID:          o.ServerID,
		scope:             hex.EncodeToString(sum[:16]),
		kernel:            o.Kernel,
		log:               o.Logger.With("server", o.ServerID),
		now:               now,
		metric:            metric,
		nodes:             make(map[string]*Node),
		pushInterval:      defaultPushInterval,
		heartbeatInterval: defaultHeartbeatInterval,
	}
	if o.CacheDir != "" {
		s.cache = newServerCache(o.CacheDir, o.ServerID)
	}
	return s
}

// Applied 返回 Hello 里要报的已应用版本（panel.AppliedFunc），并发安全。
func (s *Server) Applied() ([]*nodev1.AppliedConfig, []*nodev1.AppliedUsers) {
	s.appliedMu.Lock()
	defer s.appliedMu.Unlock()
	return s.applied.configs, s.applied.users
}

// publishApplied 按当前节点状态重建 Hello 快照：在服务的节点报配置摘要，内核里有名单版本的报名单版本。
// 每次改了 appliedSHA、started、userVersion 之后调用。
func (s *Server) publishApplied() {
	var snap appliedSnapshot
	for _, id := range s.order {
		n := s.nodes[id]
		if n == nil {
			continue
		}
		if n.started && n.appliedSHA != "" {
			snap.configs = append(snap.configs, &nodev1.AppliedConfig{NodeId: n.id, ContentSha256: n.appliedSHA})
		}
		if n.started && n.userVersion != "" {
			snap.users = append(snap.users, &nodev1.AppliedUsers{NodeId: n.id, Version: n.userVersion})
		}
	}
	s.appliedMu.Lock()
	s.applied = snap
	s.appliedMu.Unlock()
}

func (s *Server) clock() time.Time { return s.now() }

// Run 是主循环，一直跑到 ctx 取消。冷启动（Start）要在它之前、会话建立之前做完，
// 这样第一条 Hello 就带着缓存里已装上的版本，面板只发不同的部分。
//
// 退出时不在这里做最后一次上报：要先关内核让在途连接把流量入账，由调用方随后调 Shutdown。
func (s *Server) Run(ctx context.Context, link Link) {
	s.link = link
	// 节拍用定时器而不是 ticker：每轮各自带 ±10% 抖动（panel.Jitter），同时装好的一批
	// 节点几轮之后就不再踩同一秒打面板。
	push := time.NewTimer(panel.Jitter(s.pushInterval))
	heartbeat := time.NewTimer(panel.Jitter(s.heartbeatInterval))
	house := time.NewTicker(housekeepingInterval)
	defer push.Stop()
	defer heartbeat.Stop()
	defer house.Stop()
	curPush, curHeartbeat := s.pushInterval, s.heartbeatInterval

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-link.Events():
			s.handleEvent(ev)
		case <-push.C:
			s.report()
			push.Reset(panel.Jitter(s.pushInterval))
		case <-heartbeat.C:
			s.sendHeartbeat()
			heartbeat.Reset(panel.Jitter(s.heartbeatInterval))
		case <-house.C:
			s.housekeeping()
		}
		// Welcome 可能改了节拍：定时器是按旧值排的，不在这里重排的话新值只是存了个变量。
		if s.pushInterval != curPush {
			curPush = s.pushInterval
			push.Reset(panel.Jitter(curPush))
			s.log.Info("上报间隔已调整", "秒", int(curPush.Seconds()))
		}
		if s.heartbeatInterval != curHeartbeat {
			curHeartbeat = s.heartbeatInterval
			heartbeat.Reset(panel.Jitter(curHeartbeat))
			s.log.Info("心跳间隔已调整", "秒", int(curHeartbeat.Seconds()))
		}
	}
}

// handleEvent 处理一条会话事件（主循环里调用）。
func (s *Server) handleEvent(ev panel.Event) {
	switch ev.Kind {
	case panel.EventWelcome:
		s.onWelcome(ev.Welcome)
	case panel.EventConfig:
		s.applyServerConfig(ev.Config)
	case panel.EventUsersFull:
		s.onUsersFull(ev.UsersFull)
	case panel.EventUsersDelta:
		s.onUsersDelta(ev.UsersDelta)
	case panel.EventTrafficAck:
		s.onTrafficAck(ev.ReportID)
	case panel.EventDisconnected:
		s.connected = false
	case panel.EventTombstone:
		s.connected = false
		s.log.Error("面板吊销了本机身份或删除了这台服务器：继续用磁盘缓存服务现有用户与配置，需要在后台生成新令牌重新接入",
			"reason", ev.Reason)
	}
}

// onWelcome：新流建立。复位各节点的回执台账（每条流各报一次），按面板给的节拍调整，
// 重发还没确认的流量，并立刻报一次心跳，让面板马上看到各节点的真实状态。
func (s *Server) onWelcome(w *nodev1.Welcome) {
	s.connected = true
	if v := w.GetPushIntervalSec(); v > 0 {
		s.pushInterval = time.Duration(v) * time.Second
	}
	if v := w.GetHeartbeatIntervalSec(); v > 0 {
		s.heartbeatInterval = time.Duration(v) * time.Second
	}
	if ms := w.GetServerTimeUnixMs(); ms > 0 {
		if skew := s.clock().Sub(time.UnixMilli(ms)); skew > time.Minute || skew < -time.Minute {
			s.log.Warn("本机时钟与面板相差超过 1 分钟，证书校验可能失败", "偏差", skew.Round(time.Second))
		}
	}
	// 面板认面板缓存里已装上的版本；从这一刻起不再算「只靠缓存在服务」。
	for _, n := range s.nodes {
		n.resetAcks()
		n.fromCache = false
	}
	s.flushTraffic()
	s.sendHeartbeat()
}

// housekeeping 是内务节拍：到点的装失败重试、到点的 health_passed 回执。
func (s *Server) housekeeping() {
	now := s.clock()
	for _, id := range s.order {
		n := s.nodes[id]
		if n == nil {
			continue
		}
		s.retryFailedConfig(n, now)
		s.reportHealth(n, now)
	}
}

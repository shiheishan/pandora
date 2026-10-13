package service

// 会话管理（p5-session）：按服务器维护一条在途控制流，负责对齐、名单与配置下发、回执、心跳、续期、吊销断流、
// 重启前错峰重连。传输层（grpc/control.go）只做收发与 proto 互转，业务都在这里。
//
// 并发模型：
//   - Sessions.mu 只护登记表（服务器 id → 在途会话）；Session.mu 护这条会话的发送队列与下发状态。
//     持有 Sessions.mu 时不调 Session 的方法，反之亦然，所以没有锁序问题。
//   - 名单与配置的计算（Hello 对齐、纪元轮次）各取一个递增序号；会话只接受比上次更大的序号，
//     先开始的计算晚到时直接丢掉，不会用旧数据覆盖新数据（变更在提交后才推进纪元，所以序号大的那次一定看得到它）。

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// FeatureUsersDelta 是「能收名单增量」的能力名（Hello.features / Welcome.features）。
const FeatureUsersDelta = "users_delta"

// UsersChunkSize 是 UsersFull 每块的人数。
const UsersChunkSize = 5000

// panelFeatures 是面板支持的可选能力。
var panelFeatures = []string{FeatureUsersDelta}

// 会话结束的原因（传输层译成 gRPC 状态码）。
var (
	// ErrSessionSuperseded：同一服务器的新流到了，旧流关闭（codes.Aborted）。
	ErrSessionSuperseded = errors.New("node: 会话被同一服务器的新流取代")
	// ErrSessionTombstoned：身份已吊销或服务器已删除，已发 Tombstone（codes.PermissionDenied）。
	ErrSessionTombstoned = errors.New("node: 接入身份已吊销或服务器已删除")
	// ErrSessionOverflow：发送队列满了，断流让节点重连后重新对齐（codes.ResourceExhausted）。
	ErrSessionOverflow = errors.New("node: 会话发送队列已满")
	// ErrSessionShutdown：面板正在停止，已发 reconnect（codes.Unavailable）。
	ErrSessionShutdown = errors.New("node: 面板正在停止")
	// ErrSessionPeerGone：节点断开或流的 ctx 结束。
	ErrSessionPeerGone = errors.New("node: 节点已断开")
)

// SessionBackend 是会话管理要用的数据面：名单与配置（p5-delivery、p5-routing）、上报入账（p5-traffic）、
// 续期（p5-enroll）、会话自己的落库（session_store.go）。*Service 实现它；测试换成假的。
type SessionBackend interface {
	Rosters(ctx context.Context, serverIDs []uuid.UUID) (map[uuid.UUID][]NodeRoster, error)
	ServerConfigs(ctx context.Context, serverIDs []uuid.UUID) (map[uuid.UUID][]NodeConfigDoc, error)
	IngestTraffic(ctx context.Context, serverID uuid.UUID, r TrafficReport, m RosterMembership) error
	IngestOnline(ctx context.Context, serverID uuid.UUID, entries []OnlineEntry, m RosterMembership) error
	RecordMetrics(ctx context.Context, serverID uuid.UUID, m HostMetrics, at time.Time) error
	RenewCertificate(ctx context.Context, serverID uuid.UUID, csrDER []byte) (chainPEM, caPEM []byte, err error)
	RecordHello(ctx context.Context, serverID uuid.UUID, h Hello, at time.Time) error
	RecordHeartbeat(ctx context.Context, serverID uuid.UUID, nodes []NodeRuntime, at time.Time) error
	RecordConfigAck(ctx context.Context, serverID uuid.UUID, ack ConfigAck, at time.Time) error
}

// SessionOptions 是会话管理的参数；零值项取缺省（见 withDefaults）。
type SessionOptions struct {
	// QueueLimit 是每条流发送队列的条数上限（配置与每个节点的名单各只占一条），缺省 256
	QueueLimit int
	// PushInterval、HeartbeatInterval 写进 Welcome，缺省 60 秒、30 秒
	PushInterval, HeartbeatInterval time.Duration
	// EpochPoll 是读纪元值的间隔，缺省 5 秒；FullResync 是 Redis 读不到时的全量比对间隔，缺省 15 秒
	EpochPoll, FullResync time.Duration
	// MinRoundGap 是两轮重算之间的最小间隔，缺省 1 秒；RetryDelay 是一轮失败后的重试间隔，缺省 5 秒
	MinRoundGap, RetryDelay time.Duration
	// ForcedFullGap 是同一节点因 UsersAck 失败而重发全量的最小间隔，缺省 5 秒（防节点反复装不上时空转）
	ForcedFullGap time.Duration
	// TerminateGrace 是发了 Tombstone / reconnect 之后等它发出去的最长时间，过了就强关，缺省 5 秒
	TerminateGrace time.Duration
	// ShutdownBase、ShutdownSpread：停机时第 i 个会话的 reconnect in_ms = Base + i×Spread/n，缺省 2 秒、10 秒
	ShutdownBase, ShutdownSpread time.Duration
}

func (o SessionOptions) withDefaults() SessionOptions {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	if o.QueueLimit <= 0 {
		o.QueueLimit = 256
	}
	def(&o.PushInterval, 60*time.Second)
	def(&o.HeartbeatInterval, 30*time.Second)
	def(&o.EpochPoll, 5*time.Second)
	def(&o.FullResync, 15*time.Second)
	def(&o.MinRoundGap, time.Second)
	def(&o.RetryDelay, 5*time.Second)
	def(&o.ForcedFullGap, 5*time.Second)
	def(&o.TerminateGrace, 5*time.Second)
	def(&o.ShutdownBase, 2*time.Second)
	def(&o.ShutdownSpread, 10*time.Second)
	return o
}

// Sessions 是会话管理器：按服务器登记在途流，纪元变化时重算名单与配置并下发。实现 SessionControl。
type Sessions struct {
	b   SessionBackend
	rdb *redis.Client
	opt SessionOptions
	now func() time.Time

	// seq 是名单与配置计算的序号（见文件头）
	seq atomic.Uint64

	mu       sync.Mutex
	byServer map[uuid.UUID]*Session
	closing  bool

	// 纪元轮次的待办（session_epoch.go）
	dirtyMu                 sync.Mutex
	dirtyUsers, dirtyConfig bool
	wake                    chan struct{}
}

// NewSessionManager 构造会话管理器。rdb 为 nil 时不订阅通知，按 FullResync 周期全量比对。
func NewSessionManager(b SessionBackend, rdb *redis.Client, opt SessionOptions) *Sessions {
	return &Sessions{
		b: b, rdb: rdb, opt: opt.withDefaults(), now: time.Now,
		byServer: map[uuid.UUID]*Session{},
		wake:     make(chan struct{}, 1),
	}
}

// NewSessions 用本 service 作数据面构造会话管理器。装配：m := svc.NewSessions(opt)；svc.UseSessions(m)；
// serve 里 go m.Run(ctx)，停机时在 gRPC 服务 GracefulStop 之前调 m.Shutdown(ctx)。
func (s *Service) NewSessions(opt SessionOptions) *Sessions {
	return NewSessionManager(s, s.rdb, opt)
}

// SessionManager 返回装配进来的会话管理器（UseSessions 传入的 *Sessions）；没装配返回 nil。
func (s *Service) SessionManager() *Sessions {
	m, _ := s.sessions.(*Sessions)
	return m
}

var _ SessionControl = (*Sessions)(nil)

// Len 是在途会话数。
func (m *Sessions) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.byServer)
}

// Online 报告这台服务器是否有在途会话。
func (m *Sessions) Online(serverID uuid.UUID) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.byServer[serverID]
	return ok
}

func (m *Sessions) get(serverID uuid.UUID) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.byServer[serverID]
}

// onlineIDs 是全部在途会话的服务器 id（升序，便于测试与日志）。
func (m *Sessions) onlineIDs() []uuid.UUID {
	m.mu.Lock()
	ids := make([]uuid.UUID, 0, len(m.byServer))
	for id := range m.byServer {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	slices.SortFunc(ids, sessionCmpID)
	return ids
}

// Open 登记一条新流（收到 Hello 之后调）：同一服务器的旧流被取代；记下 Hello 里的版本与主机信息，
// 排上 Welcome，再算这台服务器的配置与名单、只发与 Hello 报告不同的。出错时会话已关闭并移出登记。
func (m *Sessions) Open(ctx context.Context, serverID uuid.UUID, h Hello) (*Session, error) {
	sid, err := uuid.NewV7()
	if err != nil {
		sid = uuid.New()
	}
	s := newSession(m, serverID, sid.String(), h)
	log := plog.From(ctx).With(zap.String("server_id", serverID.String()), zap.String("session_id", s.id))

	m.mu.Lock()
	if m.closing {
		m.mu.Unlock()
		return nil, ErrSessionShutdown
	}
	old := m.byServer[serverID]
	m.byServer[serverID] = s
	m.mu.Unlock()
	if old != nil {
		log.Info("同一服务器的新流到达，关闭旧流", zap.String("old_session_id", old.id))
		old.closeWith(ErrSessionSuperseded)
	}

	at := m.now()
	s.enqueue(Outbound{Kind: OutWelcome, Welcome: Welcome{
		SessionID:         s.id,
		Features:          s.features,
		PushInterval:      m.opt.PushInterval,
		HeartbeatInterval: m.opt.HeartbeatInterval,
		ServerTime:        at,
	}})
	if err := m.b.RecordHello(ctx, serverID, h, at); err != nil {
		// 主机信息与心跳时间只是展示；对齐照常做
		log.Warn("记录 Hello 失败", zap.Error(err))
	}

	seq := m.seq.Add(1)
	ids := []uuid.UUID{serverID}
	cfgs, err := m.b.ServerConfigs(ctx, ids)
	if err != nil {
		s.Close(ErrSessionPeerGone)
		return nil, err
	}
	rosters, err := m.b.Rosters(ctx, ids)
	if err != nil {
		s.Close(ErrSessionPeerGone)
		return nil, err
	}
	// 先配置后名单：新节点先有入站再收用户
	s.applyConfig(seq, cfgs[serverID])
	s.applyRosters(seq, rosters[serverID])
	log.Info("节点会话已建立", zap.String("agent_version", h.AgentVersion), zap.Strings("features", s.features))
	return s, nil
}

// unregister 把 s 移出登记（只移除自己，不动取代者）。
func (m *Sessions) unregister(s *Session) {
	m.mu.Lock()
	if m.byServer[s.serverID] == s {
		delete(m.byServer, s.serverID)
	}
	m.mu.Unlock()
}

// Tombstone 给这台服务器的在途流发 Command.tombstone 再关闭它；没有在途流时什么也不做。
func (m *Sessions) Tombstone(serverID uuid.UUID, reason string) {
	s := m.get(serverID)
	if s == nil {
		return
	}
	s.terminate(Outbound{Kind: OutTombstone, Reason: reason}, ErrSessionTombstoned)
}

// Shutdown 在面板停止前调：不再接新流，给每个会话发 reconnect（in_ms 在 ShutdownSpread 内错开，带一点抖动），
// 发出后关流；等全部会话结束或 ctx 到期（到期时强关剩下的）。
func (m *Sessions) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closing = true
	all := make([]*Session, 0, len(m.byServer))
	for _, s := range m.byServer {
		all = append(all, s)
	}
	m.mu.Unlock()
	slices.SortFunc(all, func(a, b *Session) int { return sessionCmpID(a.serverID, b.serverID) })

	n := len(all)
	for i, s := range all {
		in := m.opt.ShutdownBase + time.Duration(int64(m.opt.ShutdownSpread)*int64(i)/int64(n))
		jitter := m.opt.ShutdownSpread / time.Duration(n)
		if jitter < 100*time.Millisecond {
			jitter = 100 * time.Millisecond
		}
		s.terminate(Outbound{Kind: OutReconnect, Reconnect: Reconnect{In: in, Jitter: jitter}}, ErrSessionShutdown)
	}

	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for m.Len() > 0 {
		select {
		case <-ctx.Done():
			for _, s := range all {
				s.closeWith(ErrSessionShutdown)
			}
			return ctx.Err()
		case <-tick.C:
		}
	}
	return nil
}

// sessionCmpID 按字节序比较两个 id（与 PostgreSQL uuid 的排序一致）。
func sessionCmpID(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }

var _ SessionBackend = (*Service)(nil)

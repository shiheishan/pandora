package service

// 会话管理：按服务器维护一条在途控制流，负责对齐、名单与配置下发、回执、心跳、续期、吊销断流、
// 重启前错峰重连。传输层（grpc/control.go）只做收发与 proto 互转，业务都在这里。
//
// 并发模型：
//   - Sessions.mu 只护登记表（服务器 id → 在途会话）；Session.mu 护这条会话的发送队列与下发状态。
//     持有 Sessions.mu 时不调 Session 的方法，反之亦然，所以没有锁序问题。
//   - 每次读名单或读配置之前各取一个新的递增序号（配置与名单分开取）；会话只接受比上次更大的序号的结果，
//     先取号的读晚到时直接丢掉。保证的是：取号之前已提交的变更，这次读一定看得到；取号之后才提交的变更，
//     提交后会推进纪元、触发新一轮，新一轮的序号更大，最终以它为准。序号大不代表读到的数据一定更新（两次读可能交错），
//     由「每次提交后必有一轮序号更大的重算」兜底。
//   - 建流对齐合批（session_align.go）：同一时段到达的建流共用一次配置与名单读，一批取一对序号（先配置后名单）；
//     批是串行的，同时只有一批在读库。
//   - 日志：server_id 只由 gRPC 鉴权拦截器挂在流 ctx 的 logger 上（grpc/interceptors.go），会话层只加 session_id。

import (
	"bytes"
	"context"
	"errors"
	"math/big"
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

// SessionBackend 是会话管理要用的数据面：名单与配置、上报入账、
// 续期、会话自己的落库（session_store.go）。*Service 实现它；测试换成假的。
type SessionBackend interface {
	Rosters(ctx context.Context, serverIDs []uuid.UUID) (map[uuid.UUID][]NodeRoster, error)
	ServerConfigs(ctx context.Context, serverIDs []uuid.UUID) (map[uuid.UUID][]NodeConfigDoc, error)
	IngestTraffic(ctx context.Context, serverID uuid.UUID, r TrafficReport, m RosterMembership) error
	IngestOnline(ctx context.Context, serverID uuid.UUID, entries []OnlineEntry, m RosterMembership) error
	RecordMetrics(ctx context.Context, serverID uuid.UUID, m HostMetrics, at time.Time) error
	RenewCertificate(ctx context.Context, serverID uuid.UUID, certSerial *big.Int, csrDER []byte) (chainPEM, caPEM []byte, err error)
	RecordHello(ctx context.Context, serverID uuid.UUID, h Hello, at time.Time) error
	RecordHeartbeat(ctx context.Context, serverID uuid.UUID, nodes []NodeRuntime, at time.Time) error
	RecordConfigAck(ctx context.Context, serverID uuid.UUID, ack ConfigAck, at time.Time) error
}

// SessionOptions 是会话管理的参数；零值项取缺省（见 withDefaults）。
type SessionOptions struct {
	// QueueLimit 是每条流发送队列里普通消息（Welcome、TrafficAck、Certificate）的条数上限，缺省 256；
	// 配置只留一份、名单每节点只留一份，以节点数为界，不计入
	QueueLimit int
	// PushInterval、HeartbeatInterval 写进 Welcome，缺省 60 秒、30 秒
	PushInterval, HeartbeatInterval time.Duration
	// EpochPoll 是读纪元值的间隔，缺省 5 秒；FullResync 是 Redis 读不到时的全量比对间隔，缺省 15 秒
	EpochPoll, FullResync time.Duration
	// MinRoundGap 是两轮重算之间的最小间隔，缺省 1 秒；RetryDelay 是一轮失败后的重试间隔，缺省 5 秒
	MinRoundGap, RetryDelay time.Duration
	// ForcedFullGap 是同一节点因 UsersAck 失败而重发全量的起始间隔，缺省 5 秒；连续失败每次翻倍，封顶 ForcedFullMax，成功回执复位
	ForcedFullGap time.Duration
	// ForcedFullMax 是 UsersAck 失败后强制重发全量的退避上限，缺省 5 分钟
	ForcedFullMax time.Duration
	// TerminateGrace 是发了 Tombstone / reconnect 之后等它发出去的最长时间，过了就强关，缺省 5 秒
	TerminateGrace time.Duration
	// ShutdownBase、ShutdownSpread：停机时第 i 个会话的 reconnect in_ms = Base + i×Spread/n，缺省 2 秒、10 秒
	ShutdownBase, ShutdownSpread time.Duration
	// AlignTimeout 是建流对齐（记 Hello、等这批读完）的总时限，也是一批读库的时限；缺省 10 秒，短于节点端等 Welcome 的 15 秒。
	// 超时回 Unavailable，节点退避重连
	AlignTimeout time.Duration
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
	def(&o.ForcedFullMax, 5*time.Minute)
	def(&o.TerminateGrace, 5*time.Second)
	def(&o.ShutdownBase, 2*time.Second)
	def(&o.ShutdownSpread, 10*time.Second)
	def(&o.AlignTimeout, 10*time.Second)
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

	// align 是建流对齐合批的排队状态（session_align.go）
	align alignQueue

	// 纪元轮次的待办（session_epoch.go）
	dirtyMu                 sync.Mutex
	dirtyUsers, dirtyConfig bool
	wake                    chan struct{}
}

// NewSessionManager 构造会话管理器。rdb 为 nil 时不订阅通知，按 FullResync 周期全量比对。
func NewSessionManager(b SessionBackend, rdb *redis.Client, opt SessionOptions) *Sessions {
	opt = opt.withDefaults()
	return &Sessions{
		b: b, rdb: rdb, opt: opt, now: time.Now,
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

// Open 登记一条新流（收到 Hello 之后调）：Welcome 在登记前排进队列，所以一定是第一条；传输层在 Open 返回后才起发送与接收循环，
// 所以这次对齐（配置与名单的首读）在任何消息发出、任何上报被处理之前已经完成，每个节点的 latest 名单已就位，
// 新会话一开始收到的流量上报照样能按名单计费。同一服务器的旧流被取代；出错时会话已关闭并移出登记。
func (m *Sessions) Open(ctx context.Context, serverID uuid.UUID, h Hello) (*Session, error) {
	return m.OpenVerified(ctx, serverID, h, nil)
}

// ErrAlignBusy 是建流对齐在 AlignTimeout 内没等到结果（前面的批还没读完，或这批没读完）：回 Unavailable，节点退避重连。
var ErrAlignBusy = errors.New("node: 建流对齐超时")

// OpenVerified 同 Open，另在两处调 verify 复核身份（传输层传 AuthorizeSession；nil 表示不复核）：
//   - 登记前：已失效的身份不取代合法的在途流，直接回 ErrSessionRevoked，不留任何副作用；
//   - 登记后、记 Hello 与对齐读之前：与吊销并发时，要么吊销方的 Tombstone 找得到已登记的流，要么这次复核看到吊销。
//     复核失败时会话已登记并已 Tombstone（发出后断流），返回的会话照常交给收发循环送出 Tombstone，不做对齐读、不记 Hello。
func (m *Sessions) OpenVerified(ctx context.Context, serverID uuid.UUID, h Hello, verify func(context.Context) error) (*Session, error) {
	if verify != nil {
		if err := verify(ctx); err != nil {
			return nil, err
		}
	}
	sid, err := uuid.NewV7()
	if err != nil {
		sid = uuid.New()
	}
	s := newSession(m, serverID, sid.String(), h)
	// server_id 已由 gRPC 鉴权拦截器挂在 ctx 的 logger 上
	log := plog.From(ctx).With(zap.String("session_id", s.id))

	at := m.now()
	s.enqueue(Outbound{Kind: OutWelcome, Welcome: Welcome{
		SessionID:         s.id,
		Features:          s.features,
		PushInterval:      m.opt.PushInterval,
		HeartbeatInterval: m.opt.HeartbeatInterval,
		ServerTime:        at,
	}})

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
	if verify != nil {
		if err := verify(ctx); err != nil {
			if errors.Is(err, ErrSessionRevoked) {
				log.Warn("登记后复核发现身份已失效，发 Tombstone")
				s.Tombstone(TombstoneIdentityRevoked)
				return s, nil
			}
			s.Close(ErrSessionPeerGone)
			return nil, err
		}
	}

	// 从这里起是建流对齐，记 Hello 与等这批读完共用一个期限 AlignTimeout
	actx, cancel := context.WithTimeout(ctx, m.opt.AlignTimeout)
	defer cancel()
	if err := m.b.RecordHello(actx, serverID, h, at); err != nil {
		// 主机信息与心跳时间只是展示；对齐照常做
		log.Warn("记录 Hello 失败", zap.Error(err))
	}
	// 配置与名单的首读交给对齐器合批（session_align.go）
	r, err := m.awaitAlign(ctx, actx, serverID)
	if err != nil {
		s.Close(ErrSessionPeerGone)
		return nil, err
	}
	// 先配置后名单：新节点先有入站再收用户
	s.applyConfig(r.cfgSeq, r.cfgs)
	s.applyRosters(r.usersSeq, r.rosters)
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

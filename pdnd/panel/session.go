package panel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
)

// 会话的缺省节拍（契约：docs/architecture.md 11.3、p5-common「节点通道契约」）。
const (
	backoffBase           = time.Second
	backoffMax            = 30 * time.Second
	keepaliveTime         = 30 * time.Second
	keepaliveTimeout      = 10 * time.Second
	defaultWelcomeTimeout = 15 * time.Second
	defaultRenewCheck     = time.Hour
	defaultQueueSize      = 256
	// stableStreamAfter：流在 Welcome 之后活够这么久才算健康、把退避复位。面板一收 Hello 就发
	// Welcome、随后又把流踢掉（比如处理 Hello 出错）时，不复位才不会每秒重连一次。
	stableStreamAfter = 30 * time.Second
	// renewTimeoutChecks：发了 RenewRequest 后这么多个检查周期还没收到证书，就丢掉待配对私钥重新申请。
	renewTimeoutChecks = 2
	// expiredLogEvery：证书已过期时 Error 日志的最短间隔（重连照常，不刷屏）。
	expiredLogEvery = time.Hour
	eventBuffer     = 64
)

var (
	errSendQueueFull          = errors.New("发送队列已满，断流重连后重新对齐")
	errReconnectRequested     = errors.New("面板要求重连")
	errFirstMessageNotWelcome = errors.New("面板的第一条消息不是 Welcome")
)

// SessionOptions 是会话参数。带「测试」的项留零即用缺省值。
type SessionOptions struct {
	// Address 是面板节点通道 host:port（只给 host 时补 DefaultPanelPort）。
	Address  string
	Identity *Identity
	// AgentVersion、BinarySHA256 随 Hello 自报，面板只用来在后台提示版本不一致。
	AgentVersion string
	BinarySHA256 string
	// Features 是本端支持的可选能力，nil 即 [users_delta]。
	Features []string
	// Host 每次 Hello 取一次机器信息，nil 即 CollectHostInfo。
	Host func() *nodev1.HostInfo
	// Applied 返回已应用的配置与名单版本，必填，必须并发安全。
	Applied AppliedFunc
	Logger  *slog.Logger
	// Dialer 测试注入（bufconn）；nil 用系统拨号。
	Dialer Dialer

	// 测试：时钟、等 Welcome 的上限、续期检查间隔、发送队列长度、退避随机源。
	Now            func() time.Time
	WelcomeTimeout time.Duration
	RenewCheck     time.Duration
	QueueSize      int
	Rand           func(n int64) int64
}

// Session 是一台服务器到面板的控制会话：建流、Hello、收发、重连、续期。
//
// 收到的配置、名单、回执经 Events 交给编排层；编排层经 Send* 发心跳、流量、在线与回执。
// 只有收到 Welcome 之后 Send* 才会入队（返回 true），流断了即返回 false，编排层自己决定
// 要不要在下一条流上重发（流量按 report_id 重发，回执按 Hello 对齐重来）。
type Session struct {
	o        SessionOptions
	hostport string
	host     string
	log      *slog.Logger
	events   chan Event

	mu  sync.Mutex
	cur *liveStream

	// expiredLoggedAt 是上次记「证书已过期」的时刻（只在 Run 的 goroutine 里读写）。
	expiredLoggedAt time.Time
}

// liveStream 是一条已收到 Welcome 的流的发送端。
type liveStream struct {
	q      chan *nodev1.SessionRequest
	cancel context.CancelCauseFunc
}

// streamResult 是一条流结束的原因。
type streamResult struct {
	welcomed bool
	// welcomedAt 是收到 Welcome 的时刻，退避复位按它算这条流活了多久。
	welcomedAt time.Time
	err        error
	reconnect  *nodev1.Reconnect
	tombstone  bool
}

// NewSession 校验参数并建会话（不拨号，Run 才拨）。
func NewSession(o SessionOptions) (*Session, error) {
	if o.Identity == nil {
		return nil, errors.New("会话缺少身份")
	}
	if o.Applied == nil {
		return nil, errors.New("会话缺少已应用版本的来源")
	}
	hostport, host, err := PanelHost(o.Address)
	if err != nil {
		return nil, err
	}
	if o.Features == nil {
		o.Features = []string{FeatureUsersDelta}
	}
	if o.Host == nil {
		o.Host = CollectHostInfo
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.WelcomeTimeout <= 0 {
		o.WelcomeTimeout = defaultWelcomeTimeout
	}
	if o.RenewCheck <= 0 {
		o.RenewCheck = defaultRenewCheck
	}
	if o.QueueSize <= 0 {
		o.QueueSize = defaultQueueSize
	}
	if o.Rand == nil {
		o.Rand = rand.Int64N
	}
	return &Session{
		o: o, hostport: hostport, host: host,
		log:    o.Logger.With("panel", hostport, "server", o.Identity.ServerID()),
		events: make(chan Event, eventBuffer),
	}, nil
}

// Events 是交给编排层的事件。Run 返回后不再有新事件，通道不关闭。
func (s *Session) Events() <-chan Event { return s.events }

// Connected 报告当前是否有一条已收到 Welcome 的流。
func (s *Session) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur != nil
}

// SendHeartbeat 发心跳；没有可用的流或入队失败返回 false。
func (s *Session) SendHeartbeat(m *nodev1.Heartbeat) bool {
	return s.send(&nodev1.SessionRequest{Body: &nodev1.SessionRequest_Heartbeat{Heartbeat: m}})
}

// SendTraffic 发一份流量上报。
func (s *Session) SendTraffic(m *nodev1.TrafficReport) bool {
	return s.send(&nodev1.SessionRequest{Body: &nodev1.SessionRequest_Traffic{Traffic: m}})
}

// SendOnline 发在线 IP。
func (s *Session) SendOnline(m *nodev1.OnlineIPs) bool {
	return s.send(&nodev1.SessionRequest{Body: &nodev1.SessionRequest_Online{Online: m}})
}

// SendConfigAck 发配置回执。
func (s *Session) SendConfigAck(m *nodev1.ConfigAck) bool {
	return s.send(&nodev1.SessionRequest{Body: &nodev1.SessionRequest_ConfigAck{ConfigAck: m}})
}

// SendUsersAck 发名单回执。
func (s *Session) SendUsersAck(m *nodev1.UsersAck) bool {
	return s.send(&nodev1.SessionRequest{Body: &nodev1.SessionRequest_UsersAck{UsersAck: m}})
}

// send 把一条消息放进当前流的有界队列。队列满说明面板读不动或网络卡住：断掉这条流，
// 重连后经 Hello 重新对齐，而不是无界堆积或阻塞编排层的主循环。
func (s *Session) send(req *nodev1.SessionRequest) bool {
	s.mu.Lock()
	live := s.cur
	s.mu.Unlock()
	if live == nil {
		return false
	}
	select {
	case live.q <- req:
		return true
	default:
		live.cancel(errSendQueueFull)
		return false
	}
}

func (s *Session) setLive(live *liveStream) {
	s.mu.Lock()
	s.cur = live
	s.mu.Unlock()
}

// emit 把事件交给编排层；编排层忙时阻塞（经 gRPC 流控传回面板），ctx 结束即放弃。
func (s *Session) emit(ctx context.Context, ev Event) bool {
	select {
	case s.events <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// Run 建流并一直跑到 ctx 结束（返回 ctx.Err()）或收到 Tombstone（返回 ErrTombstoned）。
//
// 断流后按全抖动指数退避重连（1 秒起、封顶 30 秒），流在 Welcome 之后活过 stableStreamAfter
// 才把退避复位；面板发 Command.reconnect 时改按它给的 in_ms + [0, jitter_ms) 重连。
func (s *Session) Run(ctx context.Context) error {
	bo := Backoff{Base: backoffBase, Max: backoffMax, Rand: s.o.Rand}
	for {
		res := s.runStream(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if res.tombstone {
			s.log.Error(ErrTombstoned.Error())
			return ErrTombstoned
		}
		if res.welcomed {
			s.emit(ctx, Event{Kind: EventDisconnected, Err: res.err})
		}
		delay := s.reconnectDelay(&bo, res)
		// 连不上很常见（面板重启、网络抖动），记 Info：记成 Error 会把真正的问题埋掉。
		s.log.Info("会话断开，稍后重连", "err", res.err, "等待", delay.Round(time.Millisecond))
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// reconnectDelay 算一条流断开后到下次重连的等待：面板给了 reconnect 就按它；否则走退避，
// 流在 Welcome 之后活够 stableStreamAfter 才先把退避复位。
func (s *Session) reconnectDelay(bo *Backoff, res streamResult) time.Duration {
	if r := res.reconnect; r != nil {
		delay := time.Duration(r.GetInMs()) * time.Millisecond
		if j := int64(r.GetJitterMs()); j > 0 {
			delay += time.Duration(s.o.Rand(j)) * time.Millisecond
		}
		return delay
	}
	if res.welcomed && s.o.Now().Sub(res.welcomedAt) >= stableStreamAfter {
		bo.Reset()
	}
	return bo.Next()
}

// warnIfExpired 在建流前发现客户端证书已过期时记 Error（至多每 expiredLogEvery 一次）：
// 过期证书握手必然失败，只靠 Info 级的重连日志看不出要人去处理。重连照常。
func (s *Session) warnIfExpired() {
	now := s.o.Now()
	notAfter := s.o.Identity.NotAfter()
	if !now.After(notAfter) {
		return
	}
	if !s.expiredLoggedAt.IsZero() && now.Sub(s.expiredLoggedAt) < expiredLogEvery {
		return
	}
	s.expiredLoggedAt = now
	s.log.Error("客户端证书已过期，需要在后台生成新令牌重新接入", "到期", notAfter)
}

func (s *Session) dialOptions() []grpc.DialOption {
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(s.o.Identity.ClientTLSConfig(s.host))),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time: keepaliveTime, Timeout: keepaliveTimeout, PermitWithoutStream: true,
		}),
	}
	if s.o.Dialer != nil {
		opts = append(opts, grpc.WithContextDialer(s.o.Dialer))
	}
	return opts
}

func (s *Session) hello() *nodev1.Hello {
	configs, users := s.o.Applied()
	return &nodev1.Hello{
		AgentVersion:   s.o.AgentVersion,
		BinarySha256:   s.o.BinarySHA256,
		Features:       s.o.Features,
		Host:           s.o.Host(),
		AppliedConfigs: configs,
		AppliedUsers:   users,
	}
}

// runStream 跑一条流：每次新建连接（续期之后的握手因此一定用新证书），带新的 x-request-id。
func (s *Session) runStream(ctx context.Context) (res streamResult) {
	sctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	s.warnIfExpired()

	// 每条流新建连接，续期后的握手一定用新证书；重连本来就少，多一次握手无所谓。
	conn, err := grpc.NewClient("passthrough:///"+s.hostport, s.dialOptions()...)
	if err != nil {
		res.err = err
		return res
	}
	defer conn.Close()

	requestID := uuid.NewString()
	stream, err := nodev1.NewControlServiceClient(conn).Session(
		metadata.AppendToOutgoingContext(sctx, "x-request-id", requestID))
	if err != nil {
		res.err = err
		return res
	}
	var seq uint64
	send := func(req *nodev1.SessionRequest) error {
		seq++
		req.Seq = seq
		return stream.Send(req)
	}
	if err := send(&nodev1.SessionRequest{Body: &nodev1.SessionRequest_Hello{Hello: s.hello()}}); err != nil {
		res.err = fmt.Errorf("发送 Hello: %w", err)
		return res
	}

	recv := make(chan *nodev1.SessionResponse)
	recvErr := make(chan error, 1)
	go func() {
		for {
			m, err := stream.Recv()
			if err != nil {
				recvErr <- err
				return
			}
			select {
			case recv <- m:
			case <-sctx.Done():
				return
			}
		}
	}()

	welcome, early := s.awaitWelcome(sctx, recv, recvErr)
	if welcome == nil {
		return early
	}
	res.welcomed = true
	res.welcomedAt = s.o.Now()
	live := &liveStream{q: make(chan *nodev1.SessionRequest, s.o.QueueSize), cancel: cancel}
	s.setLive(live)
	defer s.setLive(nil)
	s.log.Info("会话已建立", "request_id", requestID, "session_id", welcome.GetSessionId(),
		"push_interval_sec", welcome.GetPushIntervalSec(), "heartbeat_interval_sec", welcome.GetHeartbeatIntervalSec())
	if !s.emit(sctx, Event{Kind: EventWelcome, Welcome: welcome}) {
		res.err = context.Cause(sctx)
		return res
	}

	users := newUsersAssembler()
	renewal := &pendingRenew{}
	if err := s.maybeRenew(send, renewal); err != nil {
		res.err = err
		return res
	}
	renew := time.NewTicker(s.o.RenewCheck)
	defer renew.Stop()
	for {
		select {
		case <-sctx.Done():
			res.err = context.Cause(sctx)
			return res
		case err := <-recvErr:
			res.err = err
			return res
		case req := <-live.q:
			if err := send(req); err != nil {
				res.err = err
				return res
			}
		case <-renew.C:
			if err := s.maybeRenew(send, renewal); err != nil {
				res.err = err
				return res
			}
		case m := <-recv:
			if stop := s.handle(sctx, m, users, send, renewal); stop != nil {
				stop.welcomed, stop.welcomedAt = true, res.welcomedAt
				return *stop
			}
		}
	}
}

// awaitWelcome 等流上的第一条消息。它必须是 Welcome；面板在 Welcome 之前就发 Tombstone 也认。
func (s *Session) awaitWelcome(ctx context.Context, recv <-chan *nodev1.SessionResponse, recvErr <-chan error) (*nodev1.Welcome, streamResult) {
	t := time.NewTimer(s.o.WelcomeTimeout)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return nil, streamResult{err: context.Cause(ctx)}
	case err := <-recvErr:
		return nil, streamResult{err: err}
	case <-t.C:
		return nil, streamResult{err: fmt.Errorf("等面板 Welcome 超时（%s）", s.o.WelcomeTimeout)}
	case m := <-recv:
		if w := m.GetWelcome(); w != nil {
			return w, streamResult{}
		}
		if tomb := m.GetCommand().GetTombstone(); tomb != nil {
			s.emit(ctx, Event{Kind: EventTombstone, Reason: tomb.GetReason()})
			return nil, streamResult{tombstone: true, err: ErrTombstoned}
		}
		return nil, streamResult{err: errFirstMessageNotWelcome}
	}
}

// handle 处理一条面板消息。返回非 nil 的 streamResult 表示这条流到此结束。
func (s *Session) handle(ctx context.Context, m *nodev1.SessionResponse, users *usersAssembler,
	send func(*nodev1.SessionRequest) error, renewal *pendingRenew) *streamResult {
	switch body := m.GetBody().(type) {
	case *nodev1.SessionResponse_Config:
		s.emit(ctx, Event{Kind: EventConfig, Config: body.Config})
	case *nodev1.SessionResponse_UsersFull:
		full, err := users.add(body.UsersFull)
		if err != nil {
			// 分块对不上（缺块、乱序、版本中途变了）：丢掉这一版，回 ok=false 让面板重发全量。
			s.log.Warn("名单分块不完整，已丢弃", "node", body.UsersFull.GetNodeId(), "version", body.UsersFull.GetVersion(), "err", err)
			ack := &nodev1.UsersAck{NodeId: body.UsersFull.GetNodeId(), Version: body.UsersFull.GetVersion(), ReasonCode: UsersReasonIncomplete}
			if err := send(&nodev1.SessionRequest{Body: &nodev1.SessionRequest_UsersAck{UsersAck: ack}}); err != nil {
				return &streamResult{err: err}
			}
		} else if full != nil {
			s.emit(ctx, Event{Kind: EventUsersFull, UsersFull: full})
		}
	case *nodev1.SessionResponse_UsersDelta:
		s.emit(ctx, Event{Kind: EventUsersDelta, UsersDelta: body.UsersDelta})
	case *nodev1.SessionResponse_TrafficAck:
		s.emit(ctx, Event{Kind: EventTrafficAck, ReportID: body.TrafficAck.GetReportId()})
	case *nodev1.SessionResponse_Certificate:
		s.finishRenew(body.Certificate, renewal)
	case *nodev1.SessionResponse_Command:
		if r := body.Command.GetReconnect(); r != nil {
			return &streamResult{err: errReconnectRequested, reconnect: r}
		}
		if tomb := body.Command.GetTombstone(); tomb != nil {
			s.emit(ctx, Event{Kind: EventTombstone, Reason: tomb.GetReason()})
			return &streamResult{err: ErrTombstoned, tombstone: true}
		}
	case *nodev1.SessionResponse_Welcome:
		s.log.Warn("同一条流上收到第二个 Welcome，忽略")
	default:
		s.log.Warn("收到不认识的面板消息，忽略", "seq", m.GetSeq())
	}
	return nil
}

// pendingRenew 是一条流上待配对的续期私钥：只在内存里，收到证书后才和证书一起落盘。
type pendingRenew struct {
	key    []byte
	sentAt time.Time
}

// maybeRenew 在证书剩余不到 RenewBefore 且没有在途申请时，生成新私钥并在会话里发 RenewRequest。
// 在途申请超过 renewTimeoutChecks 个检查周期还没回证书，就丢掉那把私钥重新申请（面板续期时
// 不立刻作废旧证书，重新申请是安全的）。
func (s *Session) maybeRenew(send func(*nodev1.SessionRequest) error, p *pendingRenew) error {
	now := s.o.Now()
	if p.key != nil {
		if now.Sub(p.sentAt) < renewTimeoutChecks*s.o.RenewCheck {
			return nil
		}
		s.log.Warn("续期申请迟迟没有回证书，丢掉待配对私钥重新申请", "申请于", p.sentAt)
		p.key = nil
	}
	notAfter := s.o.Identity.NotAfter()
	if notAfter.Sub(now) >= RenewBefore {
		return nil
	}
	keyPEM, csr, err := newKeyAndCSR(s.o.Identity.ServerID())
	if err != nil {
		s.log.Error("生成续期私钥失败，下次检查再试", "err", err)
		return nil
	}
	if err := send(&nodev1.SessionRequest{Body: &nodev1.SessionRequest_Renew{Renew: &nodev1.RenewRequest{CsrDer: csr}}}); err != nil {
		return err
	}
	p.key, p.sentAt = keyPEM, now
	s.log.Info("客户端证书即将到期，已在会话里申请续期", "到期", notAfter)
	return nil
}

// finishRenew 把面板签回的证书与待配对的私钥原子换上；不管装没装上都清掉这把私钥，下次检查再申请。
func (s *Session) finishRenew(c *nodev1.Certificate, p *pendingRenew) {
	key := p.key
	p.key = nil
	if key == nil {
		s.log.Warn("收到没有申请过的证书，忽略")
		return
	}
	if err := s.o.Identity.Replace(c.GetCertChainPem(), c.GetCaBundlePem(), key); err != nil {
		s.log.Error("续期证书装不上，下次检查再申请", "err", err)
		return
	}
	s.log.Info("客户端证书已续期，下次建流使用新证书", "到期", s.o.Identity.NotAfter())
}

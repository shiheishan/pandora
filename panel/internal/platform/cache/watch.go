package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 纪元监听：「变了没」不再进 PG（由 nodefabric 的 epoch_watch.go 抽出，w10quiet 定稿）。
//
// 进程独占一条连接 LISTEN 一条通道。迁移里的纪元触发器推进纪元的同时 pg_notify 这条通道，载荷是
// 「哪一类输入变了」（Kinds 里的一项）；本进程每 WatchProbeInterval 经一条池外专用连接发一条
// 探针 'p:<实例>:<序号>'，监听收到即证明还活着。
//
// NOTIFY 在提交之后才送达，所以「收到通知」一定晚于数据可见：缓存条目记下加载前的戳
// （会话号 + 每类通知的计数），戳没变就说明加载之后没有任何相关提交，条目原样可用、
// 不用读纪元。这比读序列更严：序列在提交前（延迟触发器里）就前进了，读到新纪元、却
// 读到旧数据的那道提交缝在这里不存在。
//
// 不健康（没连上、探针超时）时戳是零值，调用方一切照旧走 PG：读纪元比较、或者直接重算。
// 断线重连换新会话号，旧会话的条目全部作废，断线期间漏掉的通知不会留下旧条目。
//
// 新鲜度按本进程探针的「发送时刻」判：收到序号为 k 的回声，说明 k 发出之前提交的通知都已送到
// （同一条连接上的通知按提交顺序送达，与通道无关），所以「现在 − 最近一条已回声探针的发送时刻」
// 就是监听可能落后的上界，超过 WatchStaleAfter 即不健康（监听持续落后、断了不报错、探针写不进去，
// 都落在这条线上）。别的进程的探针不计。监听连着却长时间收不到回声（连接无声断掉，
// WaitForNotification 一直阻塞）时，看门狗在 watchdogAfter 后主动断开重连，不靠 TCP keepalive。
//
// 探针走单独一条专用连接，不和请求抢连接池：池被打满时照样能证明监听活着，优化不会
// 恰好在负载最高时撤掉。每个 Watch 占池里 1 条（LISTEN）加池外 1 条（探针），连接预算见 platform/config。

const (
	// WatchProbeInterval 是探针间隔。
	WatchProbeInterval = time.Second
	// WatchStaleAfter：最近一条已回声探针的发送时刻离现在超过这么久，就当监听不健康，回到
	// 逐请求查库。它也是监听落后时失效延迟的上界。
	WatchStaleAfter = 3 * time.Second
	// WatchRetryDelay 是监听断开后重连的等待。
	WatchRetryDelay = 2 * time.Second
	// watchdogAfter：监听连着、却这么久没有任何本进程探针回声，主动断开重连。
	watchdogAfter = 3 * WatchStaleAfter
	// watchdogEvery 是看门狗的检查间隔。
	watchdogEvery = time.Second
	// watchProbeTimeout 是一次探针写入的上限。
	watchProbeTimeout = 2 * time.Second

	// MaxKinds 是一个 Watch 最多区分的通知种类数。
	MaxKinds = 8
)

// Stamp 是监听在某一刻的状态。零值（会话号为 0）表示监听不健康，调用方走 PG。
type Stamp struct {
	session uint64
	kinds   uint8
	counts  [MaxKinds]uint64
}

// MakeStamp 按给定会话号与各类计数构造戳（给适配层与测试用；正常由 Watch.Stamp 给出）。
func MakeStamp(session uint64, counts ...uint64) Stamp {
	if len(counts) > MaxKinds {
		panic("cache: too many stamp counters")
	}
	s := Stamp{session: session, kinds: uint8(len(counts))}
	copy(s.counts[:], counts)
	return s
}

// OK 报告戳是否来自健康的监听。
func (s Stamp) OK() bool { return s.session != 0 }

// Session 是监听会话号；每次（重新）连上 +1。
func (s Stamp) Session() uint64 { return s.session }

// Count 是第 kind 类通知在本会话里的计数（kind 是 NewWatch 时 kinds 的下标）。
func (s Stamp) Count(kind int) uint64 { return s.counts[kind] }

// Covers 报告按 entry 加载的数据是否覆盖了 want 时刻之前提交的、给定几类的全部变化：
// 同一会话、且加载之前已收到的这几类通知都不少于 want 时刻的。want 不健康时一律否。
func (entry Stamp) Covers(want Stamp, kinds ...int) bool {
	if !want.OK() || entry.session != want.session {
		return false
	}
	for _, k := range kinds {
		if entry.counts[k] < want.counts[k] {
			return false
		}
	}
	return true
}

// Flight 是按戳单飞的标签：同一会话、同样多通知的请求合成一趟加载。
func (s Stamp) Flight(prefix string) string {
	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString(strconv.FormatUint(s.session, 10))
	for i := 0; i < int(s.kinds); i++ {
		b.WriteByte('.')
		b.WriteString(strconv.FormatUint(s.counts[i], 10))
	}
	return b.String()
}

// Watch 持有一条通道的监听状态。读（Stamp）在每个请求上，写只在监听与探针两个协程里。
type Watch struct {
	channel string
	label   string
	kinds   map[string]int
	nkinds  int
	now     func() time.Time
	// instance 区分本进程的探针与别的进程的（载荷 p:<instance>:<序号>）。
	instance string
	// watchdogAfter、watchdogEvery 是看门狗的参数（测试里调小）。
	watchdogAfter, watchdogEvery time.Duration

	mu          sync.Mutex
	session     uint64 // 当前会话号；每次连上 +1，0 表示还没连上过
	connected   bool
	connectedAt time.Time
	counts      [MaxKinds]uint64
	lastEcho    time.Time // 最近一条收到回声的本进程探针的「发送时刻」
	probeSeq    uint64
	probeSent   map[uint64]time.Time // 已发出、还没收到回声的探针的发送时刻
	sessions    uint64               // 已经用过的会话号（单调）
}

// NewWatch 建一个监听 channel 的 Watch。kinds 是认得的通知载荷，下标即 Stamp 里计数的下标；
// 认不出的载荷按「每一类都变了」处理（多算一次，不会少算）。label 只用于日志（如「节点」）。
// now 为 nil 用 time.Now。
func NewWatch(channel, label string, kinds []string, now func() time.Time) *Watch {
	if !validChannel(channel) {
		panic("cache: invalid watch channel " + strconv.Quote(channel))
	}
	if len(kinds) == 0 || len(kinds) > MaxKinds {
		panic("cache: a watch needs 1.." + strconv.Itoa(MaxKinds) + " kinds")
	}
	if now == nil {
		now = time.Now
	}
	idx := make(map[string]int, len(kinds))
	for i, k := range kinds {
		if k == "" || strings.HasPrefix(k, "p:") {
			panic("cache: invalid watch kind " + strconv.Quote(k))
		}
		idx[k] = i
	}
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return &Watch{channel: channel, label: label, kinds: idx, nkinds: len(kinds), now: now,
		instance: hex.EncodeToString(raw[:]), watchdogAfter: watchdogAfter, watchdogEvery: watchdogEvery,
		probeSent: make(map[uint64]time.Time)}
}

// validChannel 只收小写字母、数字与下划线：通道名直接拼进 LISTEN，不加引号。
func validChannel(ch string) bool {
	if ch == "" || len(ch) > 63 {
		return false
	}
	for _, r := range ch {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// Channel 是监听的通道名。
func (w *Watch) Channel() string { return w.channel }

// Stamp 返回当前戳；没连上、探针超时或 w 为 nil 时返回零值。
func (w *Watch) Stamp() Stamp {
	if w == nil {
		return Stamp{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.connected || w.lastEcho.IsZero() || w.now().Sub(w.lastEcho) >= WatchStaleAfter {
		return Stamp{}
	}
	return Stamp{session: w.session, kinds: uint8(w.nkinds), counts: w.counts}
}

// Healthy 报告监听此刻是否健康（连着、探针回声没超时）。
func (w *Watch) Healthy() bool { return w.Stamp().OK() }

// Connect 在 LISTEN 生效之后调用：换新会话、计数清零。要等第一个探针回声才算健康——
// 回声证明「LISTEN 之后的提交都会送到这里」。监听循环自己调；测试可以直接驱动。
func (w *Watch) Connect() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sessions++
	w.session = w.sessions
	w.connected = true
	w.connectedAt = w.now()
	w.counts = [MaxKinds]uint64{}
	w.lastEcho = time.Time{}
}

// Disconnect 在监听连接出错或退出时调用：立即不健康。
func (w *Watch) Disconnect() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.connected = false
	w.lastEcho = time.Time{}
}

// NextProbe 返回下一条探针的载荷，并记下它的发送时刻；顺手丢掉早已不可能再起作用的旧记录。
func (w *Watch) NextProbe() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	for seq, at := range w.probeSent {
		if now.Sub(at) >= watchdogAfter {
			delete(w.probeSent, seq)
		}
	}
	w.probeSeq++
	w.probeSent[w.probeSeq] = now
	return "p:" + w.instance + ":" + strconv.FormatUint(w.probeSeq, 10)
}

// echoAge 是「现在 − 最近一条已回声探针的发送时刻」；这次连上之后还没有回声时，从连上算起。
func (w *Watch) echoAge() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	since := w.lastEcho
	if since.IsZero() {
		since = w.connectedAt
	}
	return w.now().Sub(since)
}

// Observe 处理本通道上的一条通知。未知载荷按「每一类都变了」处理：多算一次，不会少算。
func (w *Watch) Observe(payload string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.connected {
		return
	}
	if k, ok := w.kinds[payload]; ok {
		w.counts[k]++
		return
	}
	if !strings.HasPrefix(payload, "p:") {
		for i := 0; i < w.nkinds; i++ {
			w.counts[i]++
		}
		return
	}
	// 只认本进程发的探针，按它的发送时刻计新鲜度；别的进程的探针不计
	inst, seqText, _ := strings.Cut(payload[2:], ":")
	seq, err := strconv.ParseUint(seqText, 10, 64)
	if err != nil || inst != w.instance {
		return
	}
	if sent, ok := w.probeSent[seq]; ok {
		if sent.After(w.lastEcho) {
			w.lastEcho = sent
		}
		for k := range w.probeSent {
			if k <= seq {
				delete(w.probeSent, k)
			}
		}
	}
}

// listenConn 是监听要用到的那一小块连接能力，测试用假的替换。
type listenConn interface {
	Exec(ctx context.Context, sql string) error
	WaitForNotification(ctx context.Context) (*pgconn.Notification, error)
	Close()
}

// Start 起监听与探针。返回的 wait 在 ctx 取消后阻塞到两个协程都退出；监听连接从 pool 借、
// 退出时销毁不还；探针用 pool 的连接配置另开一条池外连接。
func (w *Watch) Start(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) (wait func()) {
	if pool == nil {
		return func() {}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	acquire := func(ctx context.Context) (listenConn, error) {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		return &pooledListenConn{conn: conn}, nil
	}
	// 探针专用连接：不从池里借，池被打满时照样能发（连接预算见 platform/config）
	var probeConn *pgx.Conn
	closeProbe := func() {
		if probeConn != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = probeConn.Close(closeCtx)
			cancel()
			probeConn = nil
		}
	}
	probe := func(ctx context.Context, payload string) error {
		if probeConn == nil {
			conn, err := pgx.ConnectConfig(ctx, pool.Config().ConnConfig.Copy())
			if err != nil {
				return err
			}
			probeConn = conn
		}
		if _, err := probeConn.Exec(ctx, `SELECT pg_notify($1, $2)`, w.channel, payload); err != nil {
			closeProbe()
			return err
		}
		return nil
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		w.runListener(ctx, acquire, log)
	}()
	go func() {
		defer wg.Done()
		defer closeProbe()
		w.runProber(ctx, probe, log)
	}()
	return wg.Wait
}

// runListener 维持一条 LISTEN 连接，断了就换新会话重连，直到 ctx 结束。
func (w *Watch) runListener(ctx context.Context, acquire func(context.Context) (listenConn, error), log *slog.Logger) {
	failing := false
	for ctx.Err() == nil {
		err := w.listenOnce(ctx, acquire, func() {
			if failing {
				log.Info(w.label+"纪元监听已恢复", "channel", w.channel)
			}
			failing = false
		})
		w.Disconnect()
		if ctx.Err() != nil {
			return
		}
		if !failing {
			log.Warn(w.label+"纪元监听中断，请求暂回逐次查库", "channel", w.channel, "error", errString(err))
		}
		failing = true
		select {
		case <-ctx.Done():
			return
		case <-time.After(WatchRetryDelay):
		}
	}
}

func (w *Watch) listenOnce(ctx context.Context, acquire func(context.Context) (listenConn, error), ready func()) error {
	conn, err := acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.Exec(ctx, "LISTEN "+w.channel); err != nil {
		return err
	}
	w.Connect()
	ready()
	// 看门狗：连着却久久收不到本进程探针的回声（连接无声断掉、阻塞在等通知里），主动断开重连
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stalled := make(chan struct{})
	go func() {
		t := time.NewTicker(w.watchdogEvery)
		defer t.Stop()
		for {
			select {
			case <-lctx.Done():
				return
			case <-t.C:
				if w.echoAge() >= w.watchdogAfter {
					close(stalled)
					cancel()
					return
				}
			}
		}
	}()
	for {
		n, err := conn.WaitForNotification(lctx)
		if err != nil {
			select {
			case <-stalled:
				return errWatchStalled
			default:
			}
			return err
		}
		if n.Channel == w.channel {
			w.Observe(n.Payload)
		}
	}
}

// runProber 每 WatchProbeInterval 发一条探针。写失败不用管：回声收不到，
// WatchStaleAfter 之后自然判为不健康。
func (w *Watch) runProber(ctx context.Context, probe func(context.Context, string) error, log *slog.Logger) {
	t := time.NewTicker(WatchProbeInterval)
	defer t.Stop()
	failing := false
	for {
		pctx, cancel := context.WithTimeout(ctx, watchProbeTimeout)
		err := probe(pctx, w.NextProbe())
		cancel()
		switch {
		case err != nil && ctx.Err() == nil && !failing:
			log.Warn(w.label+"纪元监听探针写入失败", "channel", w.channel, "error", err.Error())
			failing = true
		case err == nil:
			failing = false
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// errWatchStalled 是看门狗断开的原因：监听连着，但 watchdogAfter 内没有任何本进程探针的回声。
var errWatchStalled = errors.New("epoch watch: no probe echo, reconnecting")

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// pooledListenConn 把连接池里借来的连接当监听连接用。LISTEN 是会话状态，所以退出时
// 从池里摘下销毁，不还回去（同 platform/realtime 的监听器）。
type pooledListenConn struct{ conn *pgxpool.Conn }

func (c *pooledListenConn) Exec(ctx context.Context, sql string) error {
	_, err := c.conn.Exec(ctx, sql)
	return err
}

func (c *pooledListenConn) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	return c.conn.Conn().WaitForNotification(ctx)
}

func (c *pooledListenConn) Close() {
	raw := c.conn.Hijack()
	closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = raw.Close(closeCtx)
}

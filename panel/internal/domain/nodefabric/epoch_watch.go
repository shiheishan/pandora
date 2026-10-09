package nodefabric

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 纪元监听（w10quiet）：静默时节点的「变了没」检查不再进 PG。
//
// 原先 aegis-node 每个节点请求都在自己那一次查询里顺手读一次下发纪元（00101 的序列），
// 用来判断进程内缓存是否过期：1000 个节点静默时每秒 130 多次只为问一句「变了没」的查询，
// 占了静默 CPU 的大头（10k-r1：PG 37、aegis-node 25.5，单核 = 100）。
//
// 现在 aegis-node 独占一条连接 LISTEN aegis_node_epoch（迁移 00153）：
//   - 'd'：00101 那组纪元触发器推进纪元的同时发出（订阅、配额用尽翻转、流量包、套餐、
//     池授权、用户组、账号状态、系统设置、节点身份、节点状态），即「下发输入变了」；
//   - 'c'：节点行的非遥测列、所属服务器的状态 / 删除 / 控制节点、生效发布物变了，
//     即「节点自己的配置与认证输入变了」；
//   - 'p'：本进程自己发的探针，证明这条监听还活着。
//
// NOTIFY 在提交之后才送达，所以「收到通知」一定晚于数据可见：缓存条目记下加载前的戳
// （会话号 + 两种通知的计数），戳没变就说明加载之后没有任何相关提交，条目原样可用、
// 不用读纪元。这比读序列更严：序列在提交前（延迟触发器里）就前进了，读到新纪元、却
// 读到旧数据的那道提交缝在这里不存在。
//
// 不健康（没连上、探针超时）时戳是零值，一切照旧走 PG：每个请求读纪元、配置视图不用
// 缓存。断线重连换新会话号，旧会话的条目全部作废，断线期间漏掉的通知不会留下旧条目。
//
// 吊销的即时性：提交 → 通知送达（本机 unix socket，毫秒级）→ 下一次请求回库。监听
// 不知不觉地断了（收不到任何东西又不报错）时，探针最晚 watchStaleAfter 之后发现，
// 期间最坏晚这么久——只在这种极端故障下出现。

const (
	// epochWatchChannel 是迁移 00153 的通知通道。
	epochWatchChannel = "aegis_node_epoch"
	// watchProbeInterval 是探针间隔：经连接池发一条 'p:<序号>'，监听收到即证明还活着。
	watchProbeInterval = time.Second
	// watchStaleAfter：这么久没收到探针回声就当监听不健康，回到逐请求查库。
	watchStaleAfter = 5 * time.Second
	// watchRetryDelay 是监听断开后重连的等待。
	watchRetryDelay = 2 * time.Second
	// watchProbeTimeout 是一次探针写入的上限。
	watchProbeTimeout = 2 * time.Second
)

// watchStamp 是监听在某一刻的状态。零值（session 为 0）表示监听不健康，调用方走 PG。
type watchStamp struct {
	session  uint64
	delivery uint64
	config   uint64
}

func (w watchStamp) ok() bool { return w.session != 0 }

// deliveryCovers 报告按 entry 加载的数据是否覆盖了 want 时刻之前提交的全部下发输入：
// 同一会话、且加载之前已收到的 'd' 通知不少于 want 时刻的。want 不健康时一律否。
func (entry watchStamp) deliveryCovers(want watchStamp) bool {
	return want.ok() && entry.session == want.session && entry.delivery >= want.delivery
}

// configCovers 同 deliveryCovers，看的是 'c' 通知。
func (entry watchStamp) configCovers(want watchStamp) bool {
	return want.ok() && entry.session == want.session && entry.config >= want.config
}

// flight 是按戳单飞的标签：同一会话、同样多通知的请求合成一趟加载。
func (w watchStamp) flight(kind string) string {
	return kind + strconv.FormatUint(w.session, 10) + "." +
		strconv.FormatUint(w.delivery, 10) + "." + strconv.FormatUint(w.config, 10)
}

// epochWatch 持有监听状态。读（stamp）在每个节点请求上，写只在监听与探针两个协程里。
type epochWatch struct {
	now func() time.Time

	mu        sync.Mutex
	session   uint64 // 当前会话号；每次连上 +1，0 表示还没连上过
	connected bool
	delivery  uint64
	config    uint64
	lastEcho  time.Time // 最近一次收到本进程探针回声的时刻
	probeSeq  uint64
	sessions  uint64 // 已经用过的会话号（单调）
}

func newEpochWatch(now func() time.Time) *epochWatch {
	if now == nil {
		now = time.Now
	}
	return &epochWatch{now: now}
}

// stamp 返回当前戳；没连上或探针超时返回零值。
func (w *epochWatch) stamp() watchStamp {
	if w == nil {
		return watchStamp{}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.connected || w.lastEcho.IsZero() || w.now().Sub(w.lastEcho) >= watchStaleAfter {
		return watchStamp{}
	}
	return watchStamp{session: w.session, delivery: w.delivery, config: w.config}
}

// connect 在 LISTEN 生效之后调用：换新会话、计数清零。要等第一个探针回声才算健康——
// 回声证明「LISTEN 之后的提交都会送到这里」。
func (w *epochWatch) connect() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sessions++
	w.session = w.sessions
	w.connected = true
	w.delivery, w.config = 0, 0
	w.lastEcho = time.Time{}
}

// disconnect 在监听连接出错或退出时调用：立即不健康。
func (w *epochWatch) disconnect() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.connected = false
	w.lastEcho = time.Time{}
}

// nextProbe 返回下一条探针的载荷。
func (w *epochWatch) nextProbe() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.probeSeq++
	return "p:" + strconv.FormatUint(w.probeSeq, 10)
}

// observe 处理一条通知。未知载荷按「两种都变了」处理：多算一次，不会少算。
func (w *epochWatch) observe(payload string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.connected {
		return
	}
	switch {
	case payload == "d":
		w.delivery++
	case payload == "c":
		w.config++
	case len(payload) > 2 && payload[:2] == "p:":
		// 只认本进程发的探针（别的副本、别的进程发的也是存活证明，一样收下）
		w.lastEcho = w.now()
	default:
		w.delivery++
		w.config++
	}
}

// listenConn 是监听要用到的那一小块连接能力，测试用假的替换。
type listenConn interface {
	Exec(ctx context.Context, sql string) error
	WaitForNotification(ctx context.Context) (*pgconn.Notification, error)
	Close()
}

// StartEpochWatch 起纪元监听与探针（aegis-node 装配时调用一次，要先 EnableNodeCaches）。
// 返回的 wait 在 ctx 取消后阻塞到两个协程都退出；连接在退出时销毁、不还回池。
func (s *Service) StartEpochWatch(ctx context.Context, log *slog.Logger) (wait func()) {
	if s.caches == nil || s.pool == nil {
		return func() {}
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	w := newEpochWatch(nil)
	s.caches.watch = w
	acquire := func(ctx context.Context) (listenConn, error) {
		conn, err := s.pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		return &pooledListenConn{conn: conn}, nil
	}
	probe := func(ctx context.Context, payload string) error {
		_, err := s.pool.Exec(ctx, `SELECT pg_notify($1, $2)`, epochWatchChannel, payload)
		return err
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		w.runListener(ctx, acquire, log)
	}()
	go func() {
		defer wg.Done()
		w.runProber(ctx, probe, log)
	}()
	return wg.Wait
}

// runListener 维持一条 LISTEN 连接，断了就换新会话重连，直到 ctx 结束。
func (w *epochWatch) runListener(ctx context.Context, acquire func(context.Context) (listenConn, error), log *slog.Logger) {
	failing := false
	for ctx.Err() == nil {
		err := w.listenOnce(ctx, acquire, func() {
			if failing {
				log.Info("节点纪元监听已恢复")
			}
			failing = false
		})
		w.disconnect()
		if ctx.Err() != nil {
			return
		}
		if !failing {
			log.Warn("节点纪元监听中断，节点请求暂回逐次查库", "error", errString(err))
		}
		failing = true
		select {
		case <-ctx.Done():
			return
		case <-time.After(watchRetryDelay):
		}
	}
}

func (w *epochWatch) listenOnce(ctx context.Context, acquire func(context.Context) (listenConn, error), ready func()) error {
	conn, err := acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.Exec(ctx, "LISTEN "+epochWatchChannel); err != nil {
		return err
	}
	w.connect()
	ready()
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		if n.Channel == epochWatchChannel {
			w.observe(n.Payload)
		}
	}
}

// runProber 每 watchProbeInterval 经连接池发一条探针。写失败不用管：回声收不到，
// watchStaleAfter 之后自然判为不健康。
func (w *epochWatch) runProber(ctx context.Context, probe func(context.Context, string) error, log *slog.Logger) {
	t := time.NewTicker(watchProbeInterval)
	defer t.Stop()
	failing := false
	for {
		pctx, cancel := context.WithTimeout(ctx, watchProbeTimeout)
		err := probe(pctx, w.nextProbe())
		cancel()
		switch {
		case err != nil && ctx.Err() == nil && !failing:
			log.Warn("节点纪元监听探针写入失败", "error", err.Error())
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

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// watchStamp 返回当前监听戳；没开缓存、没起监听或不健康时为零值。
func (s *Service) watchStamp() watchStamp {
	if s.caches == nil {
		return watchStamp{}
	}
	return s.caches.watch.stamp()
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

// EpochWatchHealthy 报告纪元监听此刻是否健康（连着、探针回声没超时）。给测试与诊断用。
func (s *Service) EpochWatchHealthy() bool { return s.watchStamp().ok() }

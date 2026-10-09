package nodefabric

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/aegispanel/aegis/internal/platform/cache"
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
// 吊销的即时性：提交 → 通知送达（本机 unix socket，毫秒级）→ 下一次请求回库。新鲜度按
// 本进程探针的「发送时刻」判：收到序号为 k 的回声，说明 k 发出之前提交的通知都已送到，
// 所以「现在 − 最近一条已回声探针的发送时刻」就是监听可能落后的上界，超过 cache.WatchStaleAfter
// 即不健康（监听持续落后、断了不报错、探针写不进去，都落在这条线上）。别的进程的探针不计。
// 监听连着却长时间收不到回声（连接无声断掉，WaitForNotification 一直阻塞）时，看门狗
// 主动断开重连，不靠 TCP keepalive。
//
// 探针走单独一条专用连接，不和请求抢连接池：池被打满时照样能证明监听活着，优化不会
// 恰好在负载最高时撤掉。
//
// 监听本身（会话、计数、探针、看门狗、重连）是 platform/cache 的 Watch，这里只定通道与两类载荷、
// 把它的戳翻成节点链路用的 watchStamp。阈值（3 秒不健康、9 秒看门狗）见 cache.WatchStaleAfter。
//
// 单副本设计：aegis-node 目前只支持单实例（总协调 2026-10-09 定）。纪元监听本身多副本也成立
// （每个副本各听各的），但心跳合并与在线上报备忘的「上次写了什么」只在本进程里，见那两处。

const (
	// epochWatchChannel 是迁移 00153 的通知通道。
	epochWatchChannel = cache.NodeEpochChannel
	// watchKindDelivery、watchKindConfig 是 'd'、'c' 两类载荷在监听戳里的下标。
	watchKindDelivery = 0
	watchKindConfig   = 1
)

// newEpochWatch 建节点纪元监听：通道 aegis_node_epoch，载荷 'd' 与 'c'。
func newEpochWatch(now func() time.Time) *cache.Watch {
	return cache.NewWatch(epochWatchChannel, "节点", []string{"d", "c"}, now)
}

// watchStamp 是监听在某一刻的状态（cache.Stamp 在节点通道上的视图）。零值（session 为 0）
// 表示监听不健康，调用方走 PG。
type watchStamp struct {
	session  uint64
	delivery uint64
	config   uint64
}

func stampOf(s cache.Stamp) watchStamp {
	if !s.OK() {
		return watchStamp{}
	}
	return watchStamp{session: s.Session(), delivery: s.Count(watchKindDelivery), config: s.Count(watchKindConfig)}
}

func (w watchStamp) cacheStamp() cache.Stamp { return cache.MakeStamp(w.session, w.delivery, w.config) }

func (w watchStamp) ok() bool { return w.session != 0 }

// deliveryCovers 报告按 entry 加载的数据是否覆盖了 want 时刻之前提交的全部下发输入：
// 同一会话、且加载之前已收到的 'd' 通知不少于 want 时刻的。want 不健康时一律否。
func (entry watchStamp) deliveryCovers(want watchStamp) bool {
	return entry.cacheStamp().Covers(want.cacheStamp(), watchKindDelivery)
}

// configCovers 同 deliveryCovers，看的是 'c' 通知。
func (entry watchStamp) configCovers(want watchStamp) bool {
	return entry.cacheStamp().Covers(want.cacheStamp(), watchKindConfig)
}

// flight 是按戳单飞的标签：同一会话、同样多通知的请求合成一趟加载。
func (w watchStamp) flight(kind string) string {
	return kind + strconv.FormatUint(w.session, 10) + "." +
		strconv.FormatUint(w.delivery, 10) + "." + strconv.FormatUint(w.config, 10)
}

// StartEpochWatch 起纪元监听与探针（aegis-node 装配时调用一次，要先 EnableNodeCaches）。
// 返回的 wait 在 ctx 取消后阻塞到两个协程都退出；连接在退出时销毁、不还回池。
func (s *Service) StartEpochWatch(ctx context.Context, log *slog.Logger) (wait func()) {
	if s.caches == nil || s.pool == nil {
		return func() {}
	}
	w := newEpochWatch(nil)
	s.caches.watch = w
	return w.Start(ctx, s.pool.Pool, log)
}

// watchStamp 返回当前监听戳；没开缓存、没起监听或不健康时为零值。
func (s *Service) watchStamp() watchStamp {
	if s.caches == nil {
		return watchStamp{}
	}
	return stampOf(s.caches.watch.Stamp())
}

// EpochWatchHealthy 报告纪元监听此刻是否健康（连着、探针回声没超时）。给测试与诊断用。
func (s *Service) EpochWatchHealthy() bool { return s.watchStamp().ok() }

// Package realtime 提供服务端推送，让页面不刷新也能跟上数据变化。
//
// 选 SSE 而不是 WebSocket：这里的数据流是单向的（服务端 → 浏览器），
// 客户端要提交什么走现成的 REST 就行，用不上双向通道。而 SSE 是纯 HTTP，
// nginx 只需关掉 buffering，不必配 Upgrade；浏览器原生 EventSource
// 自带断线重连；认证直接复用现有的 JWT 中间件，不用为握手另写一套。
//
// # 只推「什么变了」，不推数据本身
//
// 每条事件只说明「某个东西变了」，客户端收到后自己去拉最新数据。
// 看起来多一个来回，但换掉的是三类麻烦：
//
//   - 权限过滤：推数据就得为每个订阅者算一遍他能看到什么，
//     一处算错就是越权泄露；推通知则由既有的 REST 接口去做鉴权，
//     那条路径已经被反复验证过。
//   - 消息丢失：Pub/Sub 不保证送达。推数据时丢一条，界面就会
//     一直停在错误状态；推通知时丢一条，下一条通知照样会让客户端
//     拉到最新的完整状态，错误自动愈合。
//   - 乱序：两条数据事件乱序到达会让界面回退到旧值。
//     通知没有这个问题 —— 拉取拿到的永远是当前状态。
//
// 换句话说，这里的可靠性不来自消息通道，而来自「通知只是触发器，
// 真相始终在数据库」这个约束。
package realtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// Event 是一条推送。Payload 只放定位信息（哪个工单、哪个订阅），
// 不放业务数据本身。
type Event struct {
	Topic   string         `json:"topic"`
	Payload map[string]any `json:"payload,omitempty"`
}

// 频道命名。租户必须在最前面：跨租户串台是这套机制最严重的故障，
// 把租户放在键的第一段，任何订阅都不可能"不小心"跨过去。
func ChannelPublic(tenantID string) string { return "rt:" + tenantID + ":public" }
func ChannelUser(tenantID, userID string) string {
	return "rt:" + tenantID + ":user:" + userID
}
func ChannelTicket(tenantID, ticketID string) string {
	return "rt:" + tenantID + ":ticket:" + ticketID
}

// ChannelAdmin 是管理端频道：租户内的每一条变更都会额外抄送一份。
//
// 管理员要看的是全局，而不是某个人的东西 —— 工单列表、订单流水、
// 节点状态都得跟着变。让他去订阅每个用户的频道显然不现实。
//
// 抄送到这里是安全的：载荷只有表名与主键，没有业务数据；
// 管理员收到后仍要通过 admin API 拉数据，那条路径有自己的权限校验。
// 而这个频道只有 admin 域的令牌能订阅（见 admin 的 events 端点）。
func ChannelAdmin(tenantID string) string { return "rt:" + tenantID + ":admin" }

// ChannelNode 是某个节点的变更通知频道。
//
// 面板是多进程的：管理员改配置落在 aegis-admin，而节点的长连接挂在
// aegis-node，两个进程之间没有共享内存。走 Redis 这一层，改配置的进程
// 只管发一个信号，持有连接的进程收到后再去推给节点。
//
// 只发信号不发数据：配置内容要经过签名、分流拼装那一整套，让持有连接的
// 那个进程自己去查、自己去拼，比把结果塞进消息里更不容易走样——也避免
// 一份配置在 Redis 里留下明文副本。
func ChannelNode(tenantID, nodeID string) string {
	return "rt:" + tenantID + ":node:" + nodeID
}

// ChannelNodeAll 是租户下所有节点变更的汇总频道。
//
// 这套 Hub 没有暴露 Redis 的模式订阅，持有连接的进程没法一次订阅
// 「所有节点」。折中：都发到这一个频道，消息里带 node_id，收到的进程
// 自己筛掉不属于它的。租户级的量很小——节点配置不是高频操作。
func ChannelNodeAll(tenantID string) string { return "rt:" + tenantID + ":nodes" }

// 节点变更的 topic。
const (
	TopicNodeConfigChanged = "node.config.changed"
	TopicNodeUsersChanged  = "node.users.changed"
)

//------------------------------------------------------------------------------
// 订阅者
//------------------------------------------------------------------------------

// subscriber 是一条已建立的 SSE 连接。
type subscriber struct {
	ch       chan Event
	channels map[string]bool
}

// Hub 管理本机的所有 SSE 连接，并通过 Valkey 与其它实例互通。
//
// 为什么要 Valkey：面板会有多个实例（至少 public 与 admin 两个进程）。
// 用户连在 A 实例上，而改数据的请求可能落在 B 实例 ——
// 只有本地广播的话，那次变更这个用户永远收不到。
type Hub struct {
	rdb *redis.Client
	log *slog.Logger

	mu   sync.RWMutex
	subs map[*subscriber]struct{}
	// byChannel 是订阅索引。没有它的话每次广播都要遍历全部连接，
	// 在几千条长连接下这是每秒都要付一次的成本。
	byChannel map[string]map[*subscriber]struct{}

	ctx    context.Context
	cancel context.CancelFunc
}

func NewHub(rdb *redis.Client, log *slog.Logger) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Hub{
		rdb: rdb, log: log,
		subs:      make(map[*subscriber]struct{}),
		byChannel: make(map[string]map[*subscriber]struct{}),
		ctx:       ctx, cancel: cancel,
	}
	if rdb != nil {
		go h.consume()
	}
	return h
}

func (h *Hub) Close() { h.cancel() }

// consume 收 Valkey 上的广播并分发给本机连接。
func (h *Hub) consume() {
	// 用模式订阅一次性覆盖所有租户与主题。
	// 逐个频道订阅的话，每来一个新工单都要动一次订阅关系，
	// 而 Pub/Sub 的订阅变更是有成本的。
	ps := h.rdb.PSubscribe(h.ctx, "rt:*")
	defer ps.Close()

	for {
		select {
		case <-h.ctx.Done():
			return
		default:
		}
		msg, err := ps.ReceiveMessage(h.ctx)
		if err != nil {
			if h.ctx.Err() != nil {
				return
			}
			// 连接抖动：等一下再来。这里不能直接退出 ——
			// 一旦退出，这个实例上的所有连接就永远收不到跨实例的事件了，
			// 而它们的 SSE 连接看起来还好好的，故障完全静默。
			h.log.Warn("实时广播订阅中断，正在重连", "err", err)
			_ = ps.Close()
			time.Sleep(2 * time.Second)
			if h.ctx.Err() != nil {
				return
			}
			ps = h.rdb.PSubscribe(h.ctx, "rt:*")
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
			continue
		}
		h.dispatch(msg.Channel, ev)
	}
}

// dispatch 把事件送给订阅了该频道的本机连接。
func (h *Hub) dispatch(channel string, ev Event) {
	h.mu.RLock()
	targets := make([]*subscriber, 0, 8)
	for s := range h.byChannel[channel] {
		targets = append(targets, s)
	}
	h.mu.RUnlock()

	for _, s := range targets {
		select {
		case s.ch <- ev:
		default:
			// 这条连接的缓冲满了 —— 客户端读得太慢或已经僵死。
			// 直接丢弃这条事件而不是阻塞：一个卡住的浏览器标签页
			// 不该拖垮整个分发循环。丢事件是安全的，因为客户端
			// 下次收到任何通知时都会重新拉全量数据。
		}
	}
}

// Publish 广播一条事件。
//
// 失败只记日志不返回错误：推送是锦上添花，数据已经写进数据库了。
// 让一次推送失败回滚业务操作，是本末倒置。
func (h *Hub) Publish(ctx context.Context, channel, topic string, payload map[string]any) {
	ev := Event{Topic: topic, Payload: payload}
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if h.rdb == nil {
		h.dispatch(channel, ev)
		return
	}
	if err := h.rdb.Publish(ctx, channel, body).Err(); err != nil {
		h.log.Warn("实时事件发布失败", "channel", channel, "topic", topic, "err", err)
	}
}

//------------------------------------------------------------------------------
// SSE 连接
//------------------------------------------------------------------------------

// Subscribe 登记一条连接，返回事件通道与注销函数。
func (h *Hub) Subscribe(channels []string) (<-chan Event, func()) {
	s := &subscriber{
		// 带缓冲：一次批量变更可能连发几条事件，
		// 无缓冲会让发布方在写入时被慢客户端拖住
		ch:       make(chan Event, 32),
		channels: make(map[string]bool, len(channels)),
	}

	h.mu.Lock()
	h.subs[s] = struct{}{}
	for _, c := range channels {
		s.channels[c] = true
		if h.byChannel[c] == nil {
			h.byChannel[c] = make(map[*subscriber]struct{})
		}
		h.byChannel[c][s] = struct{}{}
	}
	h.mu.Unlock()

	return s.ch, func() {
		h.mu.Lock()
		delete(h.subs, s)
		for c := range s.channels {
			if set := h.byChannel[c]; set != nil {
				delete(set, s)
				if len(set) == 0 {
					// 空集合要删掉，否则频道多了以后这张表只增不减
					delete(h.byChannel, c)
				}
			}
		}
		h.mu.Unlock()
		close(s.ch)
	}
}

// Count 返回本机当前的连接数，用于观测。
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

// FormatSSE 把一条事件序列化成 SSE 帧。
func FormatSSE(id uint64, ev Event) string {
	body, err := json.Marshal(ev.Payload)
	if err != nil {
		body = []byte("{}")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "id: %d\n", id)
	fmt.Fprintf(&b, "event: %s\n", ev.Topic)
	// data 必须单行：SSE 用换行分隔字段，JSON 里若含裸换行会把帧截断。
	// json.Marshal 不会产生裸换行，这里只是把这个前提写明。
	fmt.Fprintf(&b, "data: %s\n\n", body)
	return b.String()
}

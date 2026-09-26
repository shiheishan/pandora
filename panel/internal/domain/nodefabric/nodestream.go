// [INPUT]: 依赖 platform/realtime 的跨进程广播、同包 uniproxy_config.go 的配置组装与 uniproxy.go 的用户下发
// [OUTPUT]: 对外提供 StreamHub、StreamConn 与节点长连接注册；AttachStream / AttachRealtime、NotifyNodeChanged、NotifyUsersChanged（租户级 node.users.changed）、RegisterStream / WatchNodeChanges
// [POS]: domain/nodefabric 的推送层：配置或用户变更后经 Valkey 通知持有连接的进程，再推给节点端；尽力而为，失败由轮询兜底
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/realtime"
)

// StreamHub 记着当前连着的节点，负责把事件推给它们。
//
// 只在内存里。面板多副本时，节点 A 连在副本 1 上，副本 2 改了配置推不到
// 它——这是有意接受的：轮询那条路还在，最多晚一个周期（15 秒）生效，
// 和没有 WS 时一样。要做跨副本推送就得引入 Redis pub/sub 之类的东西，
// 那是另一个量级的复杂度，等真有多副本部署再说。
//
// 换句话说，WS 是一条「快车道」，不是唯一通路。它断了、丢了、推不到，
// 系统都还能按原来的节奏工作——这是设计前提，不是妥协。
type StreamHub struct {
	mu           sync.RWMutex
	conns        map[string]map[*StreamConn]struct{} // tenantID + nodeID -> 连接集合
	tenantCounts map[string]int
}

func NewStreamHub() *StreamHub {
	return &StreamHub{
		conns:        make(map[string]map[*StreamConn]struct{}),
		tenantCounts: make(map[string]int),
	}
}

func streamKey(tenantID, nodeID string) string { return tenantID + "\x00" + nodeID }

// StreamConn 是一条节点连接。
//
// 真正的写在 api 层，这里只留一个发送通道——domain 层不该知道 SSE 的
// 帧格式，换传输时这一层不用动。
type StreamConn struct {
	TenantID string
	NodeID   string
	// Send 是待发送队列。带缓冲，满了就丢弃并断开（见 push）。
	Send chan []byte
	// Version 是这条连接上次收到的用户列表版本，用来算增量。
	// 只由 hub 在持锁时读写。
	Version string

	closeOnce sync.Once
	closed    chan struct{}
}

func NewStreamConn(tenantID, nodeID string, buffer int) *StreamConn {
	if buffer <= 0 {
		buffer = 16
	}
	return &StreamConn{
		TenantID: tenantID,
		NodeID:   nodeID,
		Send:     make(chan []byte, buffer),
		closed:   make(chan struct{}),
	}
}

// Close 关闭这条连接的发送侧。可重复调用。
func (c *StreamConn) Close() {
	c.closeOnce.Do(func() {
		close(c.closed)
		close(c.Send)
	})
}

// Closed 在连接关闭后返回一个已关闭的通道。
func (c *StreamConn) Closed() <-chan struct{} { return c.closed }

func (h *StreamHub) Add(c *StreamConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	key := streamKey(c.TenantID, c.NodeID)
	set := h.conns[key]
	if set == nil {
		set = make(map[*StreamConn]struct{})
		h.conns[key] = set
	}
	if _, exists := set[c]; exists {
		return false
	}
	first := h.tenantCounts[c.TenantID] == 0
	set[c] = struct{}{}
	h.tenantCounts[c.TenantID]++
	return first
}

func (h *StreamHub) Remove(c *StreamConn) bool {
	h.mu.Lock()
	last := false
	key := streamKey(c.TenantID, c.NodeID)
	if set := h.conns[key]; set != nil {
		if _, exists := set[c]; exists {
			delete(set, c)
			h.tenantCounts[c.TenantID]--
			if h.tenantCounts[c.TenantID] == 0 {
				delete(h.tenantCounts, c.TenantID)
				last = true
			}
		}
		if len(set) == 0 {
			delete(h.conns, key)
		}
	}
	h.mu.Unlock()
	c.Close()
	return last
}

// Count 是某个节点当前的连接数。给监控和测试用。
func (h *StreamHub) Count(tenantID, nodeID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns[streamKey(tenantID, nodeID)])
}

// NodesOf 列出本进程上连着的、属于该租户的节点。
//
// 租户级事件（比如「可服务用户集合变了」）没有单个 node_id 可指，
// 需要落到这个租户当前连在本进程上的每个节点。
func (h *StreamHub) NodesOf(tenantID string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	prefix := streamKey(tenantID, "")
	out := make([]string, 0, len(h.conns))
	for key, conns := range h.conns {
		if len(conns) == 0 || !strings.HasPrefix(key, prefix) {
			continue
		}
		out = append(out, key[len(prefix):])
	}
	return out
}

// Total 是所有节点的连接总数。
func (h *StreamHub) Total() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	n := 0
	for _, set := range h.conns {
		n += len(set)
	}
	return n
}

// PushConfig 把配置变更推给这个节点的所有连接。
func (h *StreamHub) PushConfig(tenantID, nodeID string, config json.RawMessage, etag string) {
	payload, err := json.Marshal(SyncConfigPayload{Config: config, ETag: etag})
	if err != nil {
		return
	}
	h.push(tenantID, nodeID, EventSyncConfig, payload, nil)
}

// PushUsers 把用户列表推给这个节点。
//
// 每条连接各自算：手上版本对得上就发增量，对不上（新连接、断过、版本
// 太旧）就发全量。这是长连接相比轮询的关键优势——服务端知道每条连接
// 处在哪一版。
func (h *StreamHub) PushUsers(tenantID, nodeID string, users []ProxyUser, previous map[string][]ProxyUser) {
	version := UserSetVersion(users)

	h.mu.Lock()
	set := h.conns[streamKey(tenantID, nodeID)]
	targets := make([]*StreamConn, 0, len(set))
	for c := range set {
		targets = append(targets, c)
	}
	h.mu.Unlock()

	for _, c := range targets {
		h.mu.RLock()
		from := c.Version
		h.mu.RUnlock()

		if from == version {
			continue // 这条连接已经是最新的
		}

		var data []byte
		var err error
		event := EventSyncUsers

		if old, ok := previous[from]; ok && from != "" {
			delta := DiffUsers(old, users)
			// 增量比全量还大就没必要发增量——批量改动时常有这种情况。
			// 判据用条数而不是字节数：字节数要先序列化两遍才知道。
			if len(delta.Added)+len(delta.Removed) < len(users) {
				event = EventSyncUserDelta
				data, err = json.Marshal(SyncUserDeltaPayload{
					Delta: delta, FromVersion: from, ToVersion: version,
				})
			}
		}
		if event == EventSyncUsers {
			data, err = json.Marshal(SyncUsersPayload{Users: users, Version: version})
		}
		if err != nil {
			continue
		}
		h.pushOne(c, event, data, version)
	}
}

// push 给某个节点的所有连接发同一条消息。
func (h *StreamHub) push(tenantID, nodeID, event string, data json.RawMessage, versionAfter *string) {
	h.mu.RLock()
	targets := make([]*StreamConn, 0, len(h.conns[streamKey(tenantID, nodeID)]))
	for c := range h.conns[streamKey(tenantID, nodeID)] {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	for _, c := range targets {
		v := ""
		if versionAfter != nil {
			v = *versionAfter
		}
		h.pushOne(c, event, data, v)
	}
}

// pushOne 往一条连接的队列里塞一条消息。
//
// 队列满了就断开这条连接，而不是阻塞等它。慢消费者拖住推送方是长连接
// 服务最经典的死法：一个卡住的节点能让所有推送排队，最后拖垮整个面板。
// 断开的代价很小——节点端会重连，重连时走全量同步，不丢数据。
func (h *StreamHub) pushOne(c *StreamConn, event string, data json.RawMessage, versionAfter string) {
	msg, err := json.Marshal(StreamMessage{
		Event: event, Data: data, Timestamp: time.Now().UnixMilli(),
	})
	if err != nil {
		return
	}
	select {
	case <-c.Closed():
		return
	case c.Send <- msg:
		if versionAfter != "" {
			h.mu.Lock()
			c.Version = versionAfter
			h.mu.Unlock()
		}
	default:
		// 塞不进去 = 这条连接消费不过来。断了它，让它重连后重新同步。
		h.Remove(c)
	}
}

// SetVersion 记下某条连接当前的用户列表版本。
func (h *StreamHub) SetVersion(c *StreamConn, version string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c.Version = version
}

// AttachStream 把事件流注册表挂到服务上。
//
// 装配时调用一次。挂上之后，改配置、改用户这些写操作会顺手把变更推给
// 连着的节点——推送失败不影响写操作本身，节点端有轮询兜底。
func (s *Service) AttachStream(h *StreamHub) { s.stream = h }

// notifyConfigChanged 在节点配置变更后推一条。
//
// 静默失败：推送是加速手段，它出问题不该让「改配置」这个操作报错回滚。
func (s *Service) notifyConfigChanged(tenantID, nodeID string, config []byte, etag string) {
	if s.stream == nil {
		return
	}
	s.stream.PushConfig(tenantID, nodeID, config, etag)
}

// notifyNodeChanged 在节点配置改动后，把新配置和用户列表推给连着的节点端。
//
// 整段是尽力而为：任何一步失败都只是没推成，节点端下一轮轮询照样会拿到
// 新配置。所以这里不返回错误，也不该让调用方（写操作）因为推送失败而回滚。
//
// 之所以要重新查一遍而不是复用调用方手上的数据：调用方拿的是「管理视图」
// 的节点对象，字段和下发给节点端的那份不一样（后者要带签名、分流、
// 派生出来的连接参数）。复制一份转换逻辑过来，迟早和 BuildNodeConfig 走样。
func (s *Service) notifyNodeChanged(ctx context.Context, tenantID, nodeID string) {
	// 先把信号发到 Redis：改配置的是 aegis-admin 进程，而节点的长连接挂在
	// aegis-node 进程上，两边没有共享内存。不走这一层的话，管理员改完
	// 配置，持有连接的那个进程根本不知道。
	//
	// 这也是这个功能第一版没生效的原因——当时只在 aegis-node 里挂了 hub，
	// 以为面板是单进程的。
	if s.realtime != nil {
		s.realtime.Publish(ctx, realtime.ChannelNodeAll(tenantID),
			realtime.TopicNodeConfigChanged, map[string]any{"node_id": nodeID})
	}

	// 本进程也可能正好持有这个节点的连接，直接推一次。
	if s.stream == nil || s.stream.Count(tenantID, nodeID) == 0 {
		return
	}

	n, err := s.loadServingNodeForPush(ctx, tenantID, nodeID)
	if err != nil || n == nil {
		return
	}

	// 分流读失败就不推配置——推一份缺了分流的配置比不推更糟，节点会把
	// 既有的封禁或指定出口换成默认直连。REST 那边同样是 fail-closed。
	if outs, routes, rerr := s.LoadRouting(ctx, tenantID, n.ID); rerr == nil {
		n.Outbounds, n.Routes = outs, routes
		if body, etag, berr := s.BuildNodeConfig(n); berr == nil {
			s.stream.PushConfig(tenantID, nodeID, body, etag)
		}
	}

	// 用户列表也可能因为这次改动变了（换了节点池就换了授权范围）。
	// previous 传 nil：这里拿不到「每条连接手上是哪一版」对应的旧列表，
	// 推全量。hub 自己会跳过已经是最新版的连接。
	if users, uerr := s.ListNodeUsers(ctx, tenantID, n); uerr == nil {
		s.stream.PushUsers(tenantID, nodeID, users, nil)
	}
}

// NotifyNodeChanged 给不走本包写路径、却改了节点下发内容的调用方用
// （后台单节点路由保存直接写 node_routes / node_outbounds），语义同 notifyNodeChanged：
// 事务提交之后调，尽力而为，不返回错误。
func (s *Service) NotifyNodeChanged(ctx context.Context, tenantID, nodeID string) {
	s.notifyNodeChanged(ctx, tenantID, nodeID)
}

// NotifyUsersChanged 在「谁能连哪些节点」变了之后发一次租户级 node.users.changed：
// 套餐版本换绑节点池、池的用户组名单变化、用户换组（R104）。事件不带 node_id，
// 持有连接的进程收到后给本进程上该租户的每个节点重算用户列表（WatchNodeChanges）。
// 事务提交之后调，尽力而为，不返回错误；节点端的轮询兜底。
func (s *Service) NotifyUsersChanged(ctx context.Context, tenantID string) {
	if s.realtime != nil {
		s.realtime.Publish(ctx, realtime.ChannelNodeAll(tenantID),
			realtime.TopicNodeUsersChanged, map[string]any{})
	}
}

// loadServingNodeForPush 按 ID 取出下发用的节点视图。
//
// 和 AuthenticateNode 走同一套可服务性条件——不满足的节点本来就拉不到
// 配置，推给它也没意义。
func (s *Service) loadServingNodeForPush(ctx context.Context, tenantID, nodeID string) (*ServingNode, error) {
	var n ServingNode
	var proto []byte
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT n.id, n.name, coalesce(n.node_type,''), coalesce(n.server_host,''),
			       coalesce(n.server_port,0), n.traffic_rate, n.protocol_config, n.pool_id,
			       n.status, coalesce(n.kernel,'auto'), s.status, n.serving_status
			  FROM nodes n
			  JOIN servers s ON s.tenant_id=n.tenant_id AND s.id=n.server_id
			 WHERE n.tenant_id = $1 AND n.id = $2::uuid
			   AND s.deleted_at IS NULL
			   AND s.status IN ('ready','draining')
			   AND n.serving_status IN ('active','draining')
			   AND n.node_type IS NOT NULL
			   AND n.server_port BETWEEN 1 AND 65535
			   AND `+StableProtocolReadySQL("n"),
			tenantID, nodeID,
		).Scan(&n.ID, &n.Name, &n.NodeType, &n.ServerHost, &n.ServerPort,
			&n.TrafficRate, &proto, &n.PoolID, &n.Status, &n.Kernel,
			&n.ServerStatus, &n.ServingStatus)
	})
	if err != nil {
		return nil, err
	}
	n.NodeType = CanonicalNodeType(n.NodeType)
	n.Protocol = proto
	return &n, nil
}

// AttachRealtime 挂上跨进程通知通道。
//
// 改配置的进程用它发信号，持有节点连接的进程用它收信号。没挂也能工作——
// 只是推送退化成「只有同一个进程内的连接能收到」，而节点端还有轮询兜底。
func (s *Service) AttachRealtime(h *realtime.Hub) { s.realtime = h }

// RegisterStream starts one tenant-level change watcher when that tenant's
// first local SSE connection arrives. The node process cannot know its future
// tenants during startup, so this lifecycle follows active connections.
func (s *Service) RegisterStream(c *StreamConn, log *slog.Logger) {
	if s.stream == nil {
		return
	}

	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if !s.stream.Add(c) || s.realtime == nil {
		return
	}
	if s.nodeWatchers == nil {
		s.nodeWatchers = make(map[string]context.CancelFunc)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.nodeWatchers[c.TenantID] = cancel
	go s.WatchNodeChanges(ctx, c.TenantID, log)
}

// UnregisterStream stops the tenant watcher once its last local connection
// leaves, so idle tenants do not retain Redis subscriptions.
func (s *Service) UnregisterStream(c *StreamConn) {
	if s.stream == nil {
		c.Close()
		return
	}

	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	if !s.stream.Remove(c) {
		return
	}
	if cancel := s.nodeWatchers[c.TenantID]; cancel != nil {
		cancel()
		delete(s.nodeWatchers, c.TenantID)
	}
}

func (s *Service) nodeChangeWatcherCount() int {
	s.watchMu.Lock()
	defer s.watchMu.Unlock()
	return len(s.nodeWatchers)
}

// WatchNodeChanges 订阅节点变更信号，收到就把最新配置推给本进程持有的连接。
//
// 在 aegis-node 进程里起一个 goroutine 跑它。信号里只有 node_id，配置内容
// 由这里自己查——那套签名和分流拼装逻辑只应该有一份。
func (s *Service) WatchNodeChanges(ctx context.Context, tenantID string, log *slog.Logger) {
	if s.realtime == nil || s.stream == nil {
		return
	}
	// 订阅这个租户下所有节点的频道。Redis 的模式订阅在这套 Hub 里没有
	// 暴露，所以退一步：用一个租户级频道，消息里带 node_id。
	events, unsubscribe := s.realtime.Subscribe([]string{realtime.ChannelNodeAll(tenantID)})
	defer unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			nodeID, _ := ev.Payload["node_id"].(string)
			if nodeID == "" {
				// 不带 node_id 的是租户级事件——目前是「可服务用户集合变了」，
				// 由付款履约触发。该租户下每个节点的用户列表都要重算，
				// 所以推给本进程上连着的全部节点。
				ids := s.stream.NodesOf(tenantID)
				// 这条日志是排障的锚点：事件到没到、落到几个节点上，
				// 一眼可见。付款后节点迟迟不认新用户时先看它。
				log.Info("收到租户级节点事件", "topic", ev.Topic,
					"tenant_id", tenantID, "本进程节点数", len(ids))
				for _, id := range ids {
					s.pushNodeSnapshot(ctx, tenantID, id, log)
				}
				continue
			}
			if s.stream.Count(tenantID, nodeID) == 0 {
				continue // 这个节点没连在本进程上
			}
			s.pushNodeSnapshot(ctx, tenantID, nodeID, log)
		}
	}
}

// pushNodeSnapshot 查出当前配置和用户列表，推给连着的节点。
func (s *Service) pushNodeSnapshot(ctx context.Context, tenantID, nodeID string, log *slog.Logger) {
	n, err := s.loadServingNodeForPush(ctx, tenantID, nodeID)
	if err != nil || n == nil {
		return
	}
	if outs, routes, rerr := s.LoadRouting(ctx, tenantID, n.ID); rerr == nil {
		n.Outbounds, n.Routes = outs, routes
		if body, etag, berr := s.BuildNodeConfig(n); berr == nil {
			s.stream.PushConfig(tenantID, nodeID, body, etag)
		}
	}
	if users, uerr := s.ListNodeUsers(ctx, tenantID, n); uerr == nil {
		s.stream.PushUsers(tenantID, nodeID, users, nil)
	}
	if log != nil {
		log.Info("已按变更信号推送节点配置", "node", nodeID)
	}
}

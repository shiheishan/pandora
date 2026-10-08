package nodefabric

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

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
//
// 锁序：先 h.mu 后 StreamConn.mu 的路径不存在——持 h.mu 时只复制连接列表，
// 放开之后才逐条拿连接自己的锁；持连接锁时可以调 Remove（拿 h.mu）。
type StreamHub struct {
	mu           sync.RWMutex
	conns        map[string]map[*StreamConn]struct{} // tenantID + nodeID -> 连接集合
	tenantCounts map[string]int
	// pulls 是各节点正在进行中的 REST 拉用户请求数（见 BeginUsersPull）。
	pulls map[string]int

	// users 是用户名单的版本历史与编码缓存（nodestream_users.go）。
	users *userPayloads
}

func NewStreamHub() *StreamHub {
	return &StreamHub{
		conns:        make(map[string]map[*StreamConn]struct{}),
		tenantCounts: make(map[string]int),
		pulls:        make(map[string]int),
		users:        newUserPayloads(),
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
	// Send 是待发送队列。带缓冲，满了就断开这条连接（见 pushOne）。
	//
	// 它从不关闭：关闭只关 closed。推送方与断开方并发时，「往已关闭的通道发送」
	// 会直接 panic 打崩整个 aegis-node（审计实测 2000 次里 988 次）；只关 closed
	// 的话，晚到的那条消息落进一个没人再读的缓冲里，随连接一起被回收。
	// 写协程以 Closed() 为退出信号，不靠 Send 关闭。
	Send chan []byte

	closeOnce sync.Once
	closed    chan struct{}

	// mu 串行化这条连接上的用户推送：读版本 → 选全量或增量 → 入队 → 记版本，
	// 四步必须是一体的，否则两个推送方并发时版本会乱序。
	mu sync.Mutex
	// version 是这条连接上最后入队的用户名单版本，增量从它算起。
	version string
	// stale 表示这个节点经 REST 拿过与 version 不同的名单（见 BeginUsersPull）：
	// 它手上的名单不再一定是 version，下一次只能推全量。
	stale bool
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

// Close 标记这条连接已关闭。可重复调用、可与推送并发调用。
func (c *StreamConn) Close() {
	c.closeOnce.Do(func() { close(c.closed) })
}

// Closed 在连接关闭后返回一个已关闭的通道。写协程用它退出。
func (c *StreamConn) Closed() <-chan struct{} { return c.closed }

// UsersVersion 是这条连接上最后入队的用户名单版本。给日志与测试用。
func (c *StreamConn) UsersVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version
}

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

// targets 复制出某个节点当前的连接列表，复制完即放锁。
func (h *StreamHub) targets(tenantID, nodeID string) []*StreamConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	set := h.conns[streamKey(tenantID, nodeID)]
	out := make([]*StreamConn, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	return out
}

// PushConfig 把配置变更推给这个节点的所有连接。
func (h *StreamHub) PushConfig(tenantID, nodeID string, config json.RawMessage, etag string) {
	payload, err := json.Marshal(SyncConfigPayload{Config: config, ETag: etag})
	if err != nil {
		return
	}
	msg := encodeStreamMessage(EventSyncConfig, payload, time.Now().UnixMilli())
	for _, c := range h.targets(tenantID, nodeID) {
		h.pushOne(c, msg)
	}
}

// pushOne 往一条连接的队列里塞一条已编码的消息，报告是否入队。
//
// 队列满了就断开这条连接，而不是阻塞等它。慢消费者拖住推送方是长连接
// 服务最经典的死法：一个卡住的节点能让所有推送排队，最后拖垮整个面板。
// 断开的代价很小——节点端会重连，重连时走全量同步，不丢数据。
//
// 连接已关闭时什么也不做。Send 从不关闭，所以这里与 Close 任意并发都不会 panic。
// msg 可能被多条连接共享（同一版本的全量名单只编码一次），只读。
func (h *StreamHub) pushOne(c *StreamConn, msg []byte) bool {
	select {
	case <-c.closed:
		return false
	default:
	}
	select {
	case c.Send <- msg:
		return true
	default:
		// 塞不进去 = 这条连接消费不过来。断了它，让它重连后重新同步。
		h.Remove(c)
		return false
	}
}

// encodeStreamMessage 按 StreamMessage 的线格式拼出消息字节，与
// json.Marshal(StreamMessage{...}) 逐字节相同（守卫 TestEncodeStreamMessageMatchesMarshal）。
//
// 直接拼而不 Marshal 信封：信封里的 data 是几百 KB 的已编码名单，json.Marshal 会把
// RawMessage 再校验、压缩、拷贝一遍。event 只会是本包的常量，data 来自 json.Marshal
// （合法且紧凑），拼接安全。
func encodeStreamMessage(event string, data []byte, timestamp int64) []byte {
	eventJSON, _ := json.Marshal(event)
	out := make([]byte, 0, len(data)+len(eventJSON)+48)
	out = append(out, `{"event":`...)
	out = append(out, eventJSON...)
	if len(data) > 0 {
		out = append(out, `,"data":`...)
		out = append(out, data...)
	}
	if timestamp != 0 {
		out = append(out, `,"timestamp":`...)
		out = strconv.AppendInt(out, timestamp, 10)
	}
	return append(out, '}')
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
	// hub 按每条连接手上的版本决定全量还是增量，已是最新版的连接跳过。
	if users, version, uerr := s.NodeUserSet(ctx, tenantID, n); uerr == nil {
		s.stream.PushUsers(tenantID, nodeID, users, version)
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
	err := s.pool.QueryRowScoped(ctx, db.Scope{TenantID: tenantID}, `
			SELECT n.id, n.name, coalesce(n.node_type,''), coalesce(n.server_host,''),
			       coalesce(n.server_port,0), n.traffic_rate, n.protocol_config, n.pool_id,
			       n.status, coalesce(n.kernel,'auto'), s.status, n.serving_status, `+deliveryEpochSQL+`
			  FROM nodes n
			  JOIN servers s ON s.tenant_id=n.tenant_id AND s.id=n.server_id
			 WHERE n.tenant_id = $1 AND n.id = $2::uuid
			   AND s.deleted_at IS NULL
			   AND s.status IN ('ready','draining')
			   AND n.serving_status IN ('active','draining')
			   AND n.node_type IS NOT NULL
			   AND n.server_port BETWEEN 1 AND 65535
			   AND `+StableProtocolReadySQL("n"),
		[]any{tenantID, nodeID},
		&n.ID, &n.Name, &n.NodeType, &n.ServerHost, &n.ServerPort,
		&n.TrafficRate, &proto, &n.PoolID, &n.Status, &n.Kernel,
		&n.ServerStatus, &n.ServingStatus, &n.deliveryEpoch)
	if err != nil {
		return nil, err
	}
	n.NodeType = CanonicalNodeType(n.NodeType)
	n.Protocol = proto
	n.epochKnown = true
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
	if !s.stream.Add(c) {
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

// WatchNodeChanges 盯着这个租户的下发变化，把最新配置与用户名单推给本进程持有的连接。
//
// 在 aegis-node 进程里每个租户起一个 goroutine 跑它（RegisterStream）。信号有三路：
//   - Valkey 的节点变更信号（后台改节点配置带 node_id；付款、R104 等写路径发租户级
//     node.users.changed）——跨进程，没挂 realtime 时没有这一路；
//   - 下发纪元（nodestream_epoch.go）：每 nodeEpochPollInterval 读一次序列，凡影响下发
//     结果的已提交写都让它前进（订阅、配额与流量包翻转、套餐、池授权、用户组、账号
//     状态……），不管写来自哪个进程、哪条路径，甚至直接执行的 SQL；
//   - 名单里最早的订阅到期时刻：到期没有写，到点按时推一轮。
//
// 读信号的循环只登记「谁要推」，真正查库、推送交给 streamPushQueue 的 worker 有上限地
// 并发去做（nodestream_fanout.go），且两轮之间至少隔 nodeFanoutMinInterval：批量到期、
// 批量开单时的成百上千条变化按节点合并成一轮。原先在这个循环里逐节点串行重算：一次
// 付款事件要给 200 个节点各查三遍库，几秒钟里事件循环不读，realtime 的 32 条缓冲
// 一满，后面的配置变更就被丢了。
func (s *Service) WatchNodeChanges(ctx context.Context, tenantID string, log *slog.Logger) {
	if s.stream == nil {
		return
	}
	var events <-chan realtime.Event
	if s.realtime != nil {
		// 订阅这个租户下所有节点的频道。Redis 的模式订阅在这套 Hub 里没有
		// 暴露，所以退一步：用一个租户级频道，消息里带 node_id。
		ch, unsubscribe := s.realtime.Subscribe([]string{realtime.ChannelNodeAll(tenantID)})
		defer unsubscribe()
		events = ch
	}
	poller := s.newEpochPoller(tenantID)
	if events == nil && poller == nil {
		return
	}

	queue := newStreamPushQueue()
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runStreamPushQueue(ctx, tenantID, queue, log)
	}()
	defer func() { <-done }()

	var tick <-chan time.Time
	if poller != nil {
		ticker := time.NewTicker(nodeEpochPollInterval)
		defer ticker.Stop()
		tick = ticker.C
	}
	// settle 是纪元前进之后、开始重算之前的等待（见 nodeEpochSettle）；为 nil 表示没有在等。
	var settle <-chan time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick:
			changed, expired := poller.poll(ctx, queue, now, log)
			if expired {
				queue.addAllUsers()
			}
			if changed && settle == nil {
				settle = time.After(nodeEpochSettle)
			}
		case <-settle:
			settle = nil
			queue.addAllUsers()
		case ev, ok := <-events:
			if !ok {
				if poller == nil {
					return
				}
				events = nil // Valkey 那一路关了，纪元这一路照常
				continue
			}
			nodeID, _ := ev.Payload["node_id"].(string)
			if nodeID == "" {
				// 不带 node_id 的是租户级事件——目前是「可服务用户集合变了」，
				// 由付款履约与 R104 三处写路径触发。交给 worker 按池重算、推给本进程
				// 上连着的全部节点；那些写已经推进了下发纪元，worker 读节点时拿到新
				// 纪元，用户集缓存自然重算。只推用户：配置没变，推 sync.config 会让
				// 每个节点白白重拉一遍配置。
				queue.addAllUsers()
				// 这条日志是排障的锚点：事件到没到、落到几个节点上，
				// 一眼可见。付款后节点迟迟不认新用户时先看它。
				log.Info("收到租户级节点事件", "topic", ev.Topic,
					"tenant_id", tenantID, "本进程节点数", len(s.stream.NodesOf(tenantID)))
				continue
			}
			if s.stream.Count(tenantID, nodeID) == 0 {
				continue // 这个节点没连在本进程上
			}
			queue.addNode(nodeID)
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
	if users, version, uerr := s.NodeUserSet(ctx, tenantID, n); uerr == nil {
		s.stream.PushUsers(tenantID, nodeID, users, version)
	}
	if log != nil {
		log.Info("已按变更信号推送节点配置", "node", nodeID)
	}
}

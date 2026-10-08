package nodefabric

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// 推送扇出。
//
// 租户级事件（付款、R104 的池与用户组变化）要落到本进程上连着的每个节点。原先
// 逐节点串行「查节点 → 查分流 → 查用户」：200 个节点就是 600 个事务，且每个节点
// 各跑一遍同一个池的用户查询。现在一条查询取出全部在线节点所在的池（连同当前下发
// 纪元），每个池只算一次用户集（有缓存时至多一次缓存重算），再推给池里的每个节点：连接手上的
// 版本还在历史里就推增量，否则推全量，全量与增量都按版本只编码一次；查库的那部分并发有上限，
// 不把连接池抽干。

// nodeFanoutConcurrency 是扇出里同时查库的上限，给节点的正常请求留出大半个连接池。
const nodeFanoutConcurrency = 4

// nodeFanoutTimeout 是一轮扇出的总时限。推送只是快车道，超时就放弃，节点有轮询兜底。
const nodeFanoutTimeout = 30 * time.Second

// nodeFanoutMinInterval 是同一租户两轮推送之间的最短间隔（节流）。间隔里到的信号攒到
// 下一轮一起处理：批量到期、批量开单时成百上千条变化合成每秒至多一轮。
const nodeFanoutMinInterval = time.Second

// streamPushQueue 合并待推送的工作：同一节点的多条变更只推一次；租户级用户变更
// 在 worker 忙的时候再来几条，也只补一轮。
type streamPushQueue struct {
	mu       sync.Mutex
	nodes    map[string]struct{}
	allUsers bool
	wake     chan struct{}
	// nextExpiry 是上一轮租户级推送里最早的订阅到期时刻（零值表示没有），到点再推一轮。
	nextExpiry time.Time
}

func newStreamPushQueue() *streamPushQueue {
	return &streamPushQueue{nodes: make(map[string]struct{}), wake: make(chan struct{}, 1)}
}

func (q *streamPushQueue) addNode(nodeID string) {
	q.mu.Lock()
	q.nodes[nodeID] = struct{}{}
	q.mu.Unlock()
	q.signal()
}

func (q *streamPushQueue) addAllUsers() {
	q.mu.Lock()
	q.allUsers = true
	q.mu.Unlock()
	q.signal()
}

func (q *streamPushQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default: // 已经有一次唤醒在排队，worker 醒来会一并取走
	}
}

// setNextExpiry 记下一轮租户级推送带回的最早到期时刻。
func (q *streamPushQueue) setNextExpiry(at time.Time) {
	q.mu.Lock()
	q.nextExpiry = at
	q.mu.Unlock()
}

// takeExpiryDue 报告最早到期时刻是否已到；到了就清掉（下一轮推送会带回新的）。
func (q *streamPushQueue) takeExpiryDue(now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.nextExpiry.IsZero() || now.Before(q.nextExpiry) {
		return false
	}
	q.nextExpiry = time.Time{}
	return true
}

// take 取走当前积压的全部工作。
func (q *streamPushQueue) take() (nodes []string, allUsers bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	nodes = make([]string, 0, len(q.nodes))
	for id := range q.nodes {
		nodes = append(nodes, id)
	}
	q.nodes = make(map[string]struct{})
	allUsers, q.allUsers = q.allUsers, false
	return nodes, allUsers
}

// runStreamPushQueue 是扇出 worker，跑到 ctx 结束。两轮之间至少隔 nodeFanoutMinInterval。
func (s *Service) runStreamPushQueue(ctx context.Context, tenantID string, q *streamPushQueue, log *slog.Logger) {
	runPushRounds(ctx, q, nodeFanoutMinInterval, func(nodes []string, allUsers bool) {
		runCtx, cancel := context.WithTimeout(ctx, nodeFanoutTimeout)
		defer cancel()
		defer recoverStreamPush(log, tenantID)
		if next, ok := s.processStreamPushes(runCtx, tenantID, nodes, allUsers, log); ok {
			q.setNextExpiry(next)
		}
	})
}

// runPushRounds 是 worker 的节拍：被唤醒就取走积压、跑一轮；两轮的开始至少隔 minGap，
// 间隔里到的工作攒到下一轮。跑到 ctx 结束。
func runPushRounds(ctx context.Context, q *streamPushQueue, minGap time.Duration,
	round func(nodes []string, allUsers bool)) {
	var last time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		}
		if wait := minGap - time.Since(last); !last.IsZero() && wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		last = time.Now()
		nodes, allUsers := q.take()
		round(nodes, allUsers)
	}
}

// recoverStreamPush 兜住推送路径上的 panic：推送只是快车道，它的任何 bug 都不能让
// 整个 aegis-node 崩溃、断掉全部连接（节点会走轮询兜底）。只记日志，不吞掉线索。
func recoverStreamPush(log *slog.Logger, tenantID string) {
	if v := recover(); v != nil && log != nil {
		log.Error("节点推送 panic，已兜住；本轮推送放弃，节点走轮询兜底",
			"tenant_id", tenantID, "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
	}
}

// processStreamPushes 先推节点级快照（配置 + 用户），再给其余连着的节点推用户。
// 收到过节点级快照的节点不再重复推用户——快照里已经带了最新的用户集。
// 租户级那一轮（allUsers）返回名单里最早的订阅到期时刻，ok 为真。
//
// 一轮的库查询：每个节点级快照 3 次（节点、分流、用户集，用户集多半命中缓存）；租户级
// 是 1 次取齐本进程全部在线节点所在的池，加上每个池至多 1 次名单重算（纪元没变就命中
// 缓存，一次都不查）。推送本身不查库。
func (s *Service) processStreamPushes(ctx context.Context, tenantID string, nodes []string, allUsers bool, log *slog.Logger) (time.Time, bool) {
	sem := make(chan struct{}, nodeFanoutConcurrency)
	var wg sync.WaitGroup
	for _, nodeID := range nodes {
		wg.Add(1)
		sem <- struct{}{}
		go func(nodeID string) {
			defer wg.Done()
			defer func() { <-sem }()
			s.pushNodeSnapshot(ctx, tenantID, nodeID, log)
		}(nodeID)
	}
	wg.Wait()
	if !allUsers {
		return time.Time{}, false
	}
	skip := make(map[string]bool, len(nodes))
	for _, id := range nodes {
		skip[id] = true
	}
	return s.pushTenantUsers(ctx, tenantID, skip, log), true
}

// pushTenantUsers 给本进程上该租户连着的节点（skip 之外）按池推最新用户集，返回这些
// 池的名单里最早的订阅到期时刻（零值表示没有）。
func (s *Service) pushTenantUsers(ctx context.Context, tenantID string, skip map[string]bool, log *slog.Logger) time.Time {
	ids := make([]string, 0)
	for _, id := range s.stream.NodesOf(tenantID) {
		if !skip[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return time.Time{}
	}
	nodes, err := s.loadServingNodesForPush(ctx, tenantID, ids)
	if err != nil {
		if log != nil {
			log.Warn("租户级推送：读取在线节点失败，留给节点轮询", "tenant_id", tenantID, "err", err)
		}
		return time.Time{}
	}

	// 同池节点的用户集一样：每个池挑一个节点代表去算。
	byPool := make(map[string][]ServingNode)
	for _, n := range nodes {
		key := ""
		if n.PoolID != nil {
			key = *n.PoolID
		}
		byPool[key] = append(byPool[key], n)
	}
	type poolUsers struct {
		set nodeUserSet
		err error
	}
	results := make(map[string]poolUsers, len(byPool))
	var mu sync.Mutex
	sem := make(chan struct{}, nodeFanoutConcurrency)
	var wg sync.WaitGroup
	for key, members := range byPool {
		wg.Add(1)
		sem <- struct{}{}
		go func(key string, rep ServingNode) {
			defer wg.Done()
			defer func() { <-sem }()
			set, err := s.nodeUsers(ctx, tenantID, &rep)
			if err == nil && set.version == "" {
				set.version = UserSetVersion(set.users)
			}
			mu.Lock()
			results[key] = poolUsers{set: set, err: err}
			mu.Unlock()
		}(key, members[0])
	}
	wg.Wait()

	// 推送本身不查库：每条连接按自己手上的版本拿增量或全量，同一版本的全量、
	// 同一对版本的增量都只编码一次（nodestream_users.go）。
	pushed := 0
	var next time.Time
	for key, members := range byPool {
		r := results[key]
		if r.err != nil {
			continue // 这个池算失败就不推，节点轮询兜底
		}
		next = earlierExpiry(next, nonZeroTime(r.set.nextExpiry))
		for _, n := range members {
			s.stream.PushUsers(tenantID, n.ID, r.set.users, r.set.version)
			pushed++
		}
	}
	if log != nil {
		log.Debug("租户级推送完成", "tenant_id", tenantID, "节点数", pushed, "池数", len(byPool))
	}
	return next
}

// nonZeroTime 把零值时刻当成「没有」。
func nonZeroTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// loadServingNodesForPush 一次取出一批节点里此刻可下发的那些及其所在池。条件与
// loadServingNodeForPush（也就是 AuthenticateNode）相同，不满足的节点不推。
func (s *Service) loadServingNodesForPush(ctx context.Context, tenantID string, nodeIDs []string) ([]ServingNode, error) {
	var out []ServingNode
	err := s.pool.QueryScoped(ctx, db.Scope{TenantID: tenantID}, `
			SELECT n.id::text, n.pool_id::text, `+deliveryEpochSQL+`
			  FROM nodes n
			  JOIN servers s ON s.tenant_id=n.tenant_id AND s.id=n.server_id
			 WHERE n.tenant_id = $1 AND n.id = ANY($2::uuid[])
			   AND s.deleted_at IS NULL
			   AND s.status IN ('ready','draining')
			   AND n.serving_status IN ('active','draining')
			   AND n.node_type IS NOT NULL
			   AND n.server_port BETWEEN 1 AND 65535
			   AND `+StableProtocolReadySQL("n")+`
			 ORDER BY n.id`, []any{tenantID, nodeIDs}, func(rows pgx.Rows) error {
		var n ServingNode
		if err := rows.Scan(&n.ID, &n.PoolID, &n.deliveryEpoch); err != nil {
			return err
		}
		n.epochKnown = true
		out = append(out, n)
		return nil
	})
	return out, err
}

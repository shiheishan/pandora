package nodefabric

import (
	"context"
	"log/slog"
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
// 纪元），每个池只算一次用户集（有缓存时至多一次缓存重算），全量载荷按版本只编码一次，再推给池里
// 的每个节点；查库的那部分并发有上限，不把连接池抽干。

// nodeFanoutConcurrency 是扇出里同时查库的上限。aegis-node 的连接池只有 8 条，
// 留一半给节点的正常请求。
const nodeFanoutConcurrency = 4

// nodeFanoutTimeout 是一轮扇出的总时限。推送只是快车道，超时就放弃，节点有轮询兜底。
const nodeFanoutTimeout = 30 * time.Second

// streamPushQueue 合并待推送的工作：同一节点的多条变更只推一次；租户级用户变更
// 在 worker 忙的时候再来几条，也只补一轮。
type streamPushQueue struct {
	mu       sync.Mutex
	nodes    map[string]struct{}
	allUsers bool
	wake     chan struct{}
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

// runStreamPushQueue 是扇出 worker，跑到 ctx 结束。
func (s *Service) runStreamPushQueue(ctx context.Context, tenantID string, q *streamPushQueue, log *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.wake:
		}
		nodes, allUsers := q.take()
		runCtx, cancel := context.WithTimeout(ctx, nodeFanoutTimeout)
		s.processStreamPushes(runCtx, tenantID, nodes, allUsers, log)
		cancel()
	}
}

// processStreamPushes 先推节点级快照（配置 + 用户），再给其余连着的节点推用户。
// 收到过节点级快照的节点不再重复推用户——快照里已经带了最新的用户集。
func (s *Service) processStreamPushes(ctx context.Context, tenantID string, nodes []string, allUsers bool, log *slog.Logger) {
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
		return
	}
	skip := make(map[string]bool, len(nodes))
	for _, id := range nodes {
		skip[id] = true
	}
	s.pushTenantUsers(ctx, tenantID, skip, log)
}

// pushTenantUsers 给本进程上该租户连着的节点（skip 之外）按池推最新用户集。
func (s *Service) pushTenantUsers(ctx context.Context, tenantID string, skip map[string]bool, log *slog.Logger) {
	ids := make([]string, 0)
	for _, id := range s.stream.NodesOf(tenantID) {
		if !skip[id] {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	nodes, err := s.loadServingNodesForPush(ctx, tenantID, ids)
	if err != nil {
		if log != nil {
			log.Warn("租户级推送：读取在线节点失败，留给节点轮询", "tenant_id", tenantID, "err", err)
		}
		return
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
		users   []ProxyUser
		version string
		err     error
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
			users, version, err := s.NodeUserSet(ctx, tenantID, &rep)
			mu.Lock()
			results[key] = poolUsers{users: users, version: version, err: err}
			mu.Unlock()
		}(key, members[0])
	}
	wg.Wait()

	shared := fullUsersPayloads{}
	pushed := 0
	for key, members := range byPool {
		r := results[key]
		if r.err != nil {
			continue // 这个池算失败就不推，节点轮询兜底
		}
		for _, n := range members {
			s.stream.pushUsers(tenantID, n.ID, r.users, r.version, nil, shared)
			pushed++
		}
	}
	if log != nil {
		log.Info("租户级推送完成", "tenant_id", tenantID, "节点数", pushed, "池数", len(byPool))
	}
}

// loadServingNodesForPush 一次取出一批节点里此刻可下发的那些及其所在池。条件与
// loadServingNodeForPush（也就是 AuthenticateNode）相同，不满足的节点不推。
func (s *Service) loadServingNodesForPush(ctx context.Context, tenantID string, nodeIDs []string) ([]ServingNode, error) {
	var out []ServingNode
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
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
			 ORDER BY n.id`, tenantID, nodeIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n ServingNode
			if err := rows.Scan(&n.ID, &n.PoolID, &n.deliveryEpoch); err != nil {
				return err
			}
			n.epochKnown = true
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}

package subscription

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// nodeHostSQL 是节点对用户的可连地址：订阅里写出去的就是它，空串表示没有可连地址。
// 资格片段与 listEligibleNodesTx 的 SELECT 用同一个表达式，判定与输出不会各说各话。
//
// 没填 server_host 的旧节点回落到接入时记下的公网地址。inet 要用 host() 取纯地址：
// inet::text 会带上掩码（203.0.113.7/32），写进订阅就是一个连不上的地址。host() 对
// IPv4 与 IPv6 都只给地址本身，且对非空 inet 永不为空串，不改变「有没有可连地址」的判定。
const nodeHostSQL = `COALESCE(NULLIF(n.server_host, ''), host(n.public_ipv4), n.hostname, '')`

// DeliverableNodeSQL 是下发资格里与用户、套餐都无关的那一半：节点自己能不能被发出去。
//
// 三处只经它判定，改条件只改这里：
//   - 订阅下载与门户节点预览（listEligibleNodesTx，在此之上再加套餐版本绑池与池的用户组限定）；
//   - 套餐页每个节点池的「可下发节点数」（adminops.PlanPools）；
//   - 后台节点列表的「是否下发」说明（NodeDeliverability → NodeDeliveryFacts.Refine）。
//
// 调用方必须把 nodes 记作 n、servers 记作 s，并按 s.id = n.server_id AND
// s.tenant_id = n.tenant_id 连上。内连时没挂服务器的节点直接被连接排除；左连时整个
// 片段对它求值为 NULL，放进 SELECT 列要套 COALESCE(…, false)。片段自带外层括号。
func DeliverableNodeSQL() string {
	return `(
			   -- Logical service Nodes are governed by serving_status. A Server's
			   -- control/Agent Node additionally remains gated by the legacy state.
			   (s.control_node_id IS DISTINCT FROM n.id OR n.status = 'active')
			   AND n.node_type IS NOT NULL
			   AND n.server_port BETWEEN 1 AND 65535
			   AND s.status = 'ready' AND s.deleted_at IS NULL
			   AND n.serving_status = 'active'
			   -- 从未心跳过的节点不下发。它从来没接进来过 —— 多半是
			   -- 建了没装 agent，或是测试留下的记录。发给客户端就是
			   -- 一条必然连不上的线路，和下面「没有可连地址」是同一类。
			   --
			   -- 心跳「超时」不在这里排除：心跳走 agent → 面板的 HTTPS，
			   -- 代理走 用户 → 节点，两条独立链路。agent 挂了而 xray 还在
			   -- 跑是常见情况，在 SQL 里一刀切会把还能用的节点也踢掉。
			   -- 超时的降级处理在 Go 侧（preferFreshNodes）。
			   AND n.last_heartbeat_at IS NOT NULL
			   AND ` + nodefabric.StableProtocolReadySQL("n") + `
			   -- 没有可连地址的节点发给客户端只会变成一条连不上的线路
			   AND ` + nodeHostSQL + ` <> ''
			)`
}

// NodeDeliveryFacts 是后台节点列表在 DeliveryState 之外还要知道的两条事实，
// 都由数据库按 DeliverableNodeSQL 的口径算出（NodeDeliverability）。
type NodeDeliveryFacts struct {
	// Deliverable 是节点满足 DeliverableNodeSQL
	Deliverable bool
	// ServerReady 是挂着服务器且服务器 ready、未删除；只用来挑说明文案
	ServerReady bool
	// PoolBound 是所在节点池至少绑在一个套餐版本上（plan_node_pools）
	PoolBound bool
}

// 节点列表的下发说明。PoolUnboundNote 与上线时的提示（nodefabric.activationWarnings）同一句话。
const (
	ServerNotReadyNote = "所属服务器未就绪或已删除，不下发"
	NodeNotReadyNote   = "协议、端口或可连地址不完整，不下发"
	PoolUnboundNote    = "所在节点池没有绑定任何套餐，暂时不服务任何用户"
)

// Refine 在 DeliveryState 的结论之上补齐与用户无关的其余资格条件。
//
// DeliveryState 只看服务状态、有没有池、心跳，按它判「下发」的节点仍可能因为
// 服务器没就绪、协议没配好、池没绑任何套餐而一个用户都拿不到。DeliveryState 已经
// 说不下发的，原样返回（它的说明更具体）。
func (f NodeDeliveryFacts) Refine(delivered bool, note string) (bool, string) {
	if !delivered {
		return false, note
	}
	if !f.Deliverable {
		if !f.ServerReady {
			return false, ServerNotReadyNote
		}
		return false, NodeNotReadyNote
	}
	if !f.PoolBound {
		return false, PoolUnboundNote
	}
	return delivered, note
}

// NodeDeliverability 按 DeliverableNodeSQL 的口径给一批节点算 NodeDeliveryFacts，
// 键是节点 id；不存在或不属于本租户的 id 不出现在结果里。
func NodeDeliverability(ctx context.Context, pool *db.Pool, tenantID string, nodeIDs []string) (map[string]NodeDeliveryFacts, error) {
	out := make(map[string]NodeDeliveryFacts, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}
	err := pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT n.id::text,
			       COALESCE(`+DeliverableNodeSQL()+`, false),
			       COALESCE(s.status = 'ready' AND s.deleted_at IS NULL, false),
			       n.pool_id IS NOT NULL AND EXISTS (
			         SELECT 1 FROM plan_node_pools p
			          WHERE p.tenant_id = n.tenant_id AND p.pool_id = n.pool_id)
			  FROM nodes n
			  LEFT JOIN servers s
			    ON s.id = n.server_id AND s.tenant_id = n.tenant_id
			 WHERE n.tenant_id = $1 AND n.id = ANY($2::uuid[])`, tenantID, nodeIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var f NodeDeliveryFacts
			if err := rows.Scan(&id, &f.Deliverable, &f.ServerReady, &f.PoolBound); err != nil {
				return err
			}
			out[id] = f
		}
		return rows.Err()
	})
	return out, err
}

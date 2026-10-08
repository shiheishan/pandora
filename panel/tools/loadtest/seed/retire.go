package seed

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// 后台批量改服务状态一次最多 100 个节点（BatchAdminNodeLifecycle）
const retireBatchSize = 100

type retireResult struct {
	Nodes         int
	Subscriptions int
	// Revoked 是吊销了有效接入身份的上一批节点数：有有效身份的节点后台不让退役（仍有依赖）
	Revoked int
}

type nodeVersion struct {
	ID         string `json:"id"`
	RowVersion int64  `json:"row_version"`
	serving    string
}

// expireSubscriptionsSQL 一条语句做完三件事：订阅转 expired（触发器校验每条跳转合法）、补追加写的过期事件、凭据置 expired。
// 只认 @loadtest.invalid 用户名下仍可服务或可恢复的订阅。
const expireSubscriptionsSQL = `
	WITH target AS (
		SELECT s.id, s.status
		  FROM subscriptions s
		  JOIN users u ON u.tenant_id = s.tenant_id AND u.id = s.user_id
		 WHERE s.tenant_id = $1::uuid AND u.email LIKE $2
		   AND s.status IN ('pending','trialing','active','past_due','grace','paused')
		   FOR UPDATE OF s
	), expired AS (
		UPDATE subscriptions s SET status = 'expired', ended_at = now()
		  FROM target t WHERE s.id = t.id
		RETURNING s.id, t.status AS from_status
	), events AS (
		INSERT INTO subscription_events
			(tenant_id, subscription_id, event_type, from_status, to_status, actor_kind, payload)
		SELECT $1::uuid, e.id, 'expired', e.from_status, 'expired', 'system', '{"source":"loadtest-seed-retire"}'::jsonb
		  FROM expired e
		RETURNING 1
	), creds AS (
		UPDATE subscription_credentials c SET status = 'expired'
		  FROM expired e
		 WHERE c.tenant_id = $1::uuid AND c.subscription_id = e.id AND c.status IN ('active','grace')
		RETURNING 1
	)
	SELECT (SELECT count(*) FROM expired), (SELECT count(*) FROM events), (SELECT count(*) FROM creds)`

// activeIdentityNodesSQL 圈出上一批里仍带有效接入身份的节点：上一轮压测接入过的节点都在此列。
// 后台退役要求节点没有有效身份，所以重复造数必须先把这些身份吊销。
const activeIdentityNodesSQL = `
	SELECT n.id::text
	  FROM nodes n
	 WHERE n.tenant_id = $1::uuid AND n.name LIKE $2 AND n.serving_status <> 'retired'
	   AND EXISTS (SELECT 1 FROM node_identities i
	                WHERE i.tenant_id = n.tenant_id AND i.node_id = n.id AND i.status = 'active')
	 ORDER BY n.id`

// retirePrevious 清掉上一批造数，可重复执行（每一步都只处理还没处理的）：
// 订阅转 expired → 吊销上一批节点的有效身份 → 在役节点转 draining → 全部转 retired。
// 中途失败后原样重跑即可继续，不会因为已处理的部分报错。
func retirePrevious(ctx context.Context, admin *adminPool, pool *db.Pool, tenantID string) (*retireResult, error) {
	res := &retireResult{}
	err := pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var subs, events, creds int
		if err := tx.QueryRow(ctx, expireSubscriptionsSQL, tenantID, "%@"+loadtestEmailDomain).
			Scan(&subs, &events, &creds); err != nil {
			return err
		}
		if subs != events {
			return fmt.Errorf("expired %d subscriptions but wrote %d events", subs, events)
		}
		res.Subscriptions = subs
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("expire previous load-test subscriptions: %w", err)
	}

	var identityNodes []string
	err = pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, activeIdentityNodesSQL, tenantID, loadtestNodePrefix+"%")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			identityNodes = append(identityNodes, id)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list previous load-test nodes with active identities: %w", err)
	}
	if res.Revoked, err = revokeIdentities(ctx, admin, identityNodes); err != nil {
		return nil, err
	}

	// 先把在役的推到 draining，再把全部未退役的推到 retired；每一步都重读行版本
	for _, step := range []struct {
		from []string
		to   string
	}{
		{[]string{"active"}, "draining"},
		{[]string{"draft", "draining", "disabled"}, "retired"},
	} {
		nodes, err := loadTestNodes(ctx, pool, tenantID, step.from)
		if err != nil {
			return nil, err
		}
		for _, batch := range retireBatches(nodes, retireBatchSize) {
			if _, err := admin.primary().call(ctx, http.MethodPost, "/v1/nodes/status:batch", jsonObject{
				"items": batch, "serving_status": step.to, "reason": "loadtest seed: retire previous batch",
			}); err != nil {
				return nil, err
			}
			if step.to == "retired" {
				res.Nodes += len(batch)
			}
		}
	}
	return res, nil
}

func loadTestNodes(ctx context.Context, pool *db.Pool, tenantID string, serving []string) ([]nodeVersion, error) {
	var out []nodeVersion
	err := pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, row_version, serving_status FROM nodes
			 WHERE tenant_id = $1::uuid AND name LIKE $2 AND serving_status = ANY($3::text[])
			 ORDER BY id`, tenantID, loadtestNodePrefix+"%", serving)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var n nodeVersion
			if err := rows.Scan(&n.ID, &n.RowVersion, &n.serving); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list previous load-test nodes: %w", err)
	}
	return out, nil
}

// retireBatches 把节点切成后台批量接口能收的大小。
func retireBatches(nodes []nodeVersion, size int) [][]nodeVersion {
	var out [][]nodeVersion
	for len(nodes) > 0 {
		n := min(size, len(nodes))
		out = append(out, nodes[:n])
		nodes = nodes[n:]
	}
	return out
}

// revokeIdentities 经后台吊销接口逐个吊销节点的有效身份，多会话并行。回 404 说明这一刻已没有
// 有效身份（别处刚吊销过），不算错。返回实际吊销的个数。
func revokeIdentities(ctx context.Context, admin *adminPool, nodeIDs []string) (int, error) {
	var mu sync.Mutex
	revoked := 0
	err := admin.forEach(ctx, len(nodeIDs), func(ctx context.Context, c *adminClient, i int) error {
		_, err := c.call(ctx, http.MethodPost, "/v1/nodes/"+nodeIDs[i]+"/revoke-identity", jsonObject{})
		var ae *apiError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			return nil
		}
		if err != nil {
			return fmt.Errorf("revoke identity of node %s: %w", nodeIDs[i], err)
		}
		mu.Lock()
		revoked++
		mu.Unlock()
		return nil
	})
	return revoked, err
}

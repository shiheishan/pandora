// [INPUT]: 依赖 admin.go 的 adminClient（后台批量改服务状态接口 /v1/nodes/status:batch）、platform/db 的 InTx，依赖 naming.go 的两个识别标记
// [OUTPUT]: 包内提供 retireResult、retirePrevious、expireSubscriptionsSQL、retireBatches
// [POS]: tools/loadtest/seed 的「先停用旧批次再造新批次」：压测库里删不掉旧数据（订阅事件、拉取日志、有效发布物、审计都是追加写，
//        订阅连着它们），所以旧批次只做状态迁移——节点经后台接口退役（active 先 draining 再 retired，过服务状态机），
//        订阅经状态机触发器转 expired、凭据随之失效；只圈 loadtest- 名字与 @loadtest.invalid 邮箱，绝不碰别的数据

package seed

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// 后台批量改服务状态一次最多 100 个节点（BatchAdminNodeLifecycle）
const retireBatchSize = 100

type retireResult struct {
	Nodes         int
	Subscriptions int
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

func retirePrevious(ctx context.Context, admin *adminClient, pool *db.Pool, tenantID string) (*retireResult, error) {
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
			if _, err := admin.call(ctx, http.MethodPost, "/v1/nodes/status:batch", jsonObject{
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

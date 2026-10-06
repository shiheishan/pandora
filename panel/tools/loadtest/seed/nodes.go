// [INPUT]: 依赖 admin.go 的 adminClient（后台真实写接口）、enroll.go 的 nodeClient 与 nodeIdentity、naming.go 的 namespace，依赖 platform/db 读节点行版本
// [OUTPUT]: 包内提供 seededNode、createCatalog、createServers、createNodes、enrollNodes、activateNodes、publishPlan
// [POS]: tools/loadtest/seed 的节点与目录造数，照前端冒烟 frontend/tests/smoke/seed.ts 的真实流程：池与套餐草稿先行（草稿绑池）→ 服务器 →
//        节点划进池 → 按节点签发接入令牌 → 节点两段式接入 → 一步上线 → 发布套餐版本；全程走网关，生命周期、审计与配置发布锁都由面板自己推进
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package seed

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// 节点协议固定 shadowsocks / aes-256-gcm：协议配置最简单、用户口令就是订阅的 proxy_uuid，
// 节点端模拟器不必处理 REALITY 之类的密钥材料
const (
	seedNodeType   = "shadowsocks"
	seedNodeMethod = "aes-256-gcm"
	seedBasePort   = 20000
	// 套餐按月计费、每月 9.90（虚构价格，只用来满足价格必填）
	seedPriceInterval = "month"
	seedPriceAmount   = 990
	seedPriceCurrency = "CNY"
	// 接入令牌签发后要在这段时间内用掉，接入阶段远短于它
	bootstrapTTLMinutes = 30
)

type seededNode struct {
	Index    int
	Name     string
	ID       string
	ServerID string
	Port     int
	token    string
	Identity *nodeIdentity
	Enroll   *enrollResult
}

// createCatalog 建节点池与套餐草稿（向导一次建好套餐、额度、价格并绑池，暂不发布）。
// 草稿先绑池：上线接口发现所在池没绑任何套餐会带 warnings，而发布又要求绑定池里已有可服务节点。
func createCatalog(ctx context.Context, admin *adminClient, ns namespace, o *options) (poolID, planID, versionID string, err error) {
	pool, err := admin.call(ctx, http.MethodPost, "/v1/node-pools",
		jsonObject{"name": "Loadtest " + ns.base(), "code": ns.PoolCode()})
	if err != nil {
		return "", "", "", err
	}
	if poolID, err = str(pool, "id"); err != nil {
		return "", "", "", err
	}
	plan, err := admin.call(ctx, http.MethodPost, "/v1/plans/complete", jsonObject{
		"code": ns.PlanCode(), "name": "Loadtest " + ns.base(), "visibility": "public",
		"traffic_gb": o.TrafficGB, "max_devices": o.MaxDevices, "pool_ids": []string{poolID},
		"prices": []jsonObject{{
			"billing_interval": seedPriceInterval, "interval_count": 1, "unit_amount": seedPriceAmount,
			"currency": seedPriceCurrency, "trial_days": 0,
		}},
		"publish": false,
	}, http.StatusCreated)
	if err != nil {
		return "", "", "", err
	}
	if planID, err = str(plan, "plan.id"); err != nil {
		return "", "", "", err
	}
	if versionID, err = str(plan, "version_id"); err != nil {
		return "", "", "", err
	}
	return poolID, planID, versionID, nil
}

// serverCount 是放下 nodes 个节点、每台 perServer 个所需的服务器数。
func serverCount(nodes, perServer int) int { return (nodes + perServer - 1) / perServer }

func createServers(ctx context.Context, admin *adminClient, ns namespace, count, perServer int) ([]string, error) {
	ids := make([]string, 0, count)
	for j := 0; j < count; j++ {
		out, err := admin.call(ctx, http.MethodPost, "/v1/servers", jsonObject{
			"name": ns.ServerName(j), "hostname": fmt.Sprintf("s%04d.%s.%s", j+1, ns.base(), loadtestEmailDomain),
			"public_ipv4": serverIP(j), "capacity_nodes": perServer,
		}, http.StatusCreated)
		if err != nil {
			return nil, err
		}
		id, err := str(out, "id")
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// createNodes 建草稿节点并逐个签发接入令牌（按节点签发：令牌哈希绑节点名，按服务器签发会拿服务器名当节点名）。
func createNodes(ctx context.Context, admin *adminClient, ns namespace, poolID string, serverIDs []string, count, perServer int) ([]*seededNode, error) {
	nodes := make([]*seededNode, 0, count)
	for i := 0; i < count; i++ {
		n := &seededNode{Index: i, Name: ns.NodeName(i), ServerID: serverIDs[i/perServer], Port: seedBasePort + i}
		out, err := admin.call(ctx, http.MethodPost, "/v1/nodes", jsonObject{
			"name": n.Name, "server_id": n.ServerID, "pool_id": poolID, "node_type": seedNodeType,
			"server_host": ns.NodeHost(i), "server_port": n.Port, "kernel": "auto", "traffic_rate": 1,
			"display_name":    fmt.Sprintf("LT %s %04d", ns.Label, i+1),
			"protocol_config": jsonObject{"method": seedNodeMethod},
		}, http.StatusCreated)
		if err != nil {
			return nil, err
		}
		if n.ID, err = str(out, "id"); err != nil {
			return nil, err
		}
		tok, err := admin.call(ctx, http.MethodPost, "/v1/nodes/bootstrap-token", jsonObject{
			"node_name": n.Name, "ttl_minutes": bootstrapTTLMinutes, "server_id": n.ServerID,
		}, http.StatusCreated)
		if err != nil {
			return nil, err
		}
		if n.token, err = str(tok, "token"); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// enrollNodes 并发跑节点侧接入；节点网关不限流，面板侧接入持租户级发布锁，并发只是把排队放进库里。
func enrollNodes(ctx context.Context, nc *nodeClient, nodes []*seededNode, workers int) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan *seededNode)
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range jobs {
				id, err := newNodeIdentity()
				var res *enrollResult
				if err == nil {
					res, err = nc.enroll(ctx, id, n.Name, n.token, n.ID, fmt.Sprintf("lt-host-%04d", n.Index+1))
				}
				if err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
						cancel()
					}
					mu.Unlock()
					continue
				}
				n.Identity, n.Enroll, n.token = id, res, ""
			}
		}()
	}
	for _, n := range nodes {
		select {
		case jobs <- n:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wg.Wait()
	return firstErr
}

// activateNodes 一步上线：行版本直接从库里读（接入推过两次生命周期），省掉逐个列节点的后台请求。
func activateNodes(ctx context.Context, admin *adminClient, pool *db.Pool, tenantID string, nodes []*seededNode) error {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	versions := make(map[string]int64, len(nodes))
	err := pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text,row_version FROM nodes WHERE tenant_id=$1 AND id=ANY($2::uuid[])`, tenantID, ids)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var v int64
			if err := rows.Scan(&id, &v); err != nil {
				return err
			}
			versions[id] = v
		}
		return rows.Err()
	})
	if err != nil {
		return fmt.Errorf("read node row versions: %w", err)
	}
	for _, n := range nodes {
		v, ok := versions[n.ID]
		if !ok {
			return fmt.Errorf("node %s (%s) vanished before activation", n.Name, n.ID)
		}
		out, err := admin.call(ctx, http.MethodPost, "/v1/nodes/"+n.ID+"/activate", jsonObject{"row_version": v})
		if err != nil {
			return err
		}
		status, _ := str(out, "status")
		serving, _ := str(out, "serving_status")
		if status != "active" || serving != "active" {
			return fmt.Errorf("node %s after activate: status=%q serving_status=%q", n.Name, status, serving)
		}
		// warnings 出现说明池或套餐绑定没接上，节点不会服务任何人（R113）
		if w := lookup(out, "warnings"); w != nil {
			return fmt.Errorf("node %s activated with warnings: %s", n.Name, mustJSON(w))
		}
	}
	return nil
}

// publishPlan 发布套餐草稿版本：两个乐观锁取自详情，与后台「版本」页的发布对话框同一个请求。
func publishPlan(ctx context.Context, admin *adminClient, planID, versionID string) error {
	detail, err := admin.call(ctx, http.MethodGet, "/v1/plans/"+planID, nil)
	if err != nil {
		return err
	}
	planRV, err := num(detail, "plan.row_version")
	if err != nil {
		return err
	}
	versions, _ := lookup(detail, "plan.versions").([]any)
	var versionRV int64 = -1
	for _, v := range versions {
		if m, ok := v.(map[string]any); ok && m["id"] == versionID {
			if versionRV, err = num(m, "row_version"); err != nil {
				return err
			}
		}
	}
	if versionRV < 0 {
		return fmt.Errorf("plan %s detail lacks version %s", planID, versionID)
	}
	_, err = admin.call(ctx, http.MethodPost, "/v1/plans/"+planID+"/versions/"+versionID+"/publish", jsonObject{
		"expected_plan_row_version": planRV, "expected_version_row_version": versionRV,
	})
	return err
}

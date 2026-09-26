// [INPUT]: 依赖 uniproxy.go 的 ServingNode，依赖 usage_daily.go 的 chargeReportEntry 逐用户记账，依赖 platform/db；读写 quota_balances 与 traffic_pack_grants（00070）
// [OUTPUT]: 对外提供 PushResult、Service.ReportTraffic；包内提供 sortedReportEntries、splitTrafficCharge、chargeTraffic
// [POS]: domain/nodefabric 的 UniProxy 流量上报（POST /api/v1/server/UniProxy/push）：从 uniproxy.go 拆出。按用户 ID 排序逐个记账（确定的加锁顺序）；扣量先吃套餐本周期额度、再按先到先扣吃流量包（D-E-1），先锁配额行再锁流量包
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

//------------------------------------------------------------------------------
// POST /api/v1/server/UniProxy/push
//------------------------------------------------------------------------------

type PushResult struct {
	Accepted   int   `json:"accepted"`
	Duplicate  bool  `json:"duplicate"`
	TotalBytes int64 `json:"total_bytes"`
}

// ReportTraffic 接收节点上报的流量并扣减配额。
//
// 协议的缺陷与应对：UniProxy 的 push 提交的是**增量**且**没有幂等键**，
// 节点端重试会导致同一段流量被计两次。协议层无法修复，这里做两件事：
//
//  1. 近似去重 —— 同一节点在 10 秒内提交完全相同的报文视为重试，直接丢弃。
//     窗口取 10 秒是因为节点端的 push_interval 是 60 秒，正常情况下
//     两次上报不可能在 10 秒内且内容完全一致。
//  2. 原样留档 —— 每一次上报都写进 node_traffic_reports，
//     出现流量争议时可以逐笔回溯，这是唯一的证据来源。
func (s *Service) ReportTraffic(ctx context.Context, tenantID string, n *ServingNode, raw []byte) (*PushResult, error) {
	var report map[string][2]int64
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, httpx.New(httpx.CodeBadRequest, "上报格式非法")
	}

	sum := sha256.Sum256(raw)
	out := &PushResult{}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// --- 近似去重 ---
		var dupID string
		err := tx.QueryRow(ctx, `
			SELECT id FROM node_traffic_reports
			 WHERE node_id = $1 AND content_hash = $2
			   AND received_at > now() - interval '10 seconds'
			 ORDER BY received_at DESC LIMIT 1`,
			n.ID, sum[:]).Scan(&dupID)
		isDup := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		var totalUp, totalDown int64
		for _, v := range report {
			totalUp += v[0]
			totalDown += v[1]
		}

		var reportID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO node_traffic_reports
				(tenant_id, node_id, user_count, total_upload, total_download,
				 traffic_rate, raw_payload, content_hash, duplicate_of)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			RETURNING id`,
			tenantID, n.ID, len(report), totalUp, totalDown,
			n.TrafficRate, raw, sum[:], nullStr(dupID),
		).Scan(&reportID); err != nil {
			return err
		}

		out.TotalBytes = totalUp + totalDown
		out.Duplicate = isDup
		if isDup {
			// 留了档但不扣量
			return nil
		}

		// --- 扣减配额 ---
		// 按 uid 排序逐个处理：并发的两份上报以同一顺序锁配额行、流量包与
		// 当日用量行，不会交叉死锁（map 迭代顺序是随机的）。
		now := time.Now()
		for _, entry := range sortedReportEntries(report) {
			if entry.used <= 0 {
				continue
			}
			// 按节点倍率折算后计费
			billed := int64(float64(entry.used) * n.TrafficRate)
			accepted, err := chargeReportEntry(ctx, tx, tenantID, entry.uid, billed, now)
			if err != nil {
				return err
			}
			if accepted { // 订阅已删除的 uid 忽略
				out.Accepted++
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

type reportEntry struct {
	uid  int64
	used int64
}

// sortedReportEntries 把上报整理成按 uid 升序的 (uid, 上下行合计)；非法 key 跳过，
// 不因一个坏值毁掉整批。同一 uid 出现多种写法（"7" 与 "007"）时合并。
func sortedReportEntries(report map[string][2]int64) []reportEntry {
	byUID := map[int64]int64{}
	for key, v := range report {
		uid, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			continue
		}
		byUID[uid] += v[0] + v[1]
	}
	out := make([]reportEntry, 0, len(byUID))
	for uid, used := range byUID {
		out = append(out, reportEntry{uid: uid, used: used})
	}
	slices.SortFunc(out, func(a, b reportEntry) int { return cmp.Compare(a.uid, b.uid) })
	return out
}

// splitTrafficCharge 决定一笔用量怎么分：先吃套餐本周期剩余额度（planRoom，
// nil 表示不限量），超出的部分再从流量包余额里扣（packRemaining），
// 两者都不够的那部分仍记在套餐上（让配额变负、下一轮停止下发）。
// 返回 记到套餐配额上的量 与 从流量包扣的量。
func splitTrafficCharge(billed int64, planRoom *int64, packRemaining int64) (int64, int64) {
	if billed <= 0 {
		return 0, 0
	}
	if planRoom == nil {
		return billed, 0
	}
	overflow := billed - max(*planRoom, 0)
	if overflow <= 0 {
		return billed, 0
	}
	fromPacks := min(overflow, max(packRemaining, 0))
	return billed - fromPacks, fromPacks
}

// chargeTraffic 把一笔已计费的用量记到订阅配额与用户的流量包上（D-E-1）。
//
// 锁顺序：先锁本周期的流量配额行，再按先到先扣的顺序锁流量包。所有上报走同一
// 顺序。套餐剩余额度取本周期所有限量流量行里最紧的一条；没有限量行就是不限量，
// 不动流量包。
func chargeTraffic(ctx context.Context, tx pgx.Tx, tenantID, subID, userID string, billed int64) error {
	var planRoom *int64
	if err := tx.QueryRow(ctx, `
		SELECT min(limit_value + adjusted - consumed)
		  FROM (SELECT limit_value, adjusted, consumed FROM quota_balances
		         WHERE tenant_id = $1 AND subscription_id = $2::uuid
		           AND metric = 'traffic.bytes'
		           AND period_start <= now()
		           AND (period_end IS NULL OR period_end > now())
		         ORDER BY id FOR UPDATE) q
		 WHERE limit_value IS NOT NULL`, tenantID, subID).Scan(&planRoom); err != nil {
		return err
	}

	fromPacks := int64(0)
	if planRoom != nil && billed > max(*planRoom, 0) {
		type openGrant struct {
			id   string
			left int64
		}
		var grants []openGrant
		var packRemaining int64
		rows, err := tx.Query(ctx, `
			SELECT id::text, granted_bytes - consumed_bytes
			  FROM traffic_pack_grants
			 WHERE tenant_id = $1 AND user_id = $2::uuid
			   AND consumed_bytes < granted_bytes
			 ORDER BY created_at, id FOR UPDATE`, tenantID, userID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var g openGrant
			if err := rows.Scan(&g.id, &g.left); err != nil {
				rows.Close()
				return err
			}
			grants = append(grants, g)
			packRemaining += g.left
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		_, fromPacks = splitTrafficCharge(billed, planRoom, packRemaining)
		left := fromPacks
		for _, g := range grants {
			if left == 0 {
				break
			}
			take := min(left, g.left)
			if _, err := tx.Exec(ctx, `
				UPDATE traffic_pack_grants SET consumed_bytes = consumed_bytes + $3
				 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, g.id, take); err != nil {
				return err
			}
			left -= take
		}
	}

	if planCharge := billed - fromPacks; planCharge > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE quota_balances
			   SET consumed = consumed + $3
			 WHERE tenant_id = $1 AND subscription_id = $2::uuid AND metric = 'traffic.bytes'
			   AND period_start <= now()
			   AND (period_end IS NULL OR period_end > now())`,
			tenantID, subID, planCharge); err != nil {
			return err
		}
	}
	return nil
}

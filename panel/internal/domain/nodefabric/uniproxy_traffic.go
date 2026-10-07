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

		// --- 小时汇总（00099）：与留档同一事务，重复上报只计重复数 ---
		if err := rollupTrafficReport(ctx, tx, tenantID, reportID); err != nil {
			return err
		}
		if isDup {
			// 留了档但不扣量
			return nil
		}

		// --- 扣减配额 ---
		// 整份上报批量记账（chargeReportEntries）：按 uid 升序整理，一次查订阅、一次锁全部
		// 配额行（按 id 排序加锁）、必要时一次锁流量包，再各用一条语句写回。并发的两份
		// 上报以同一顺序加锁，不会交叉死锁（map 迭代顺序是随机的，所以先排序）。
		now := time.Now()
		entries := make([]billedEntry, 0, len(report))
		for _, entry := range sortedReportEntries(report) {
			if entry.used <= 0 {
				continue
			}
			// 按节点倍率折算后计费
			entries = append(entries, billedEntry{uid: entry.uid, billed: int64(float64(entry.used) * n.TrafficRate)})
		}
		accepted, err := chargeReportEntries(ctx, tx, tenantID, entries, now)
		if err != nil {
			return err
		}
		out.Accepted = accepted // 订阅已删除的 uid 不计
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

// trafficCharge 是一笔已经找到订阅的计费用量。
type trafficCharge struct {
	subID  string
	userID string
	billed int64
}

// chargeTraffic 把一笔已计费的用量记到订阅配额与用户的流量包上（D-E-1），
// 是 applyTrafficCharges 的单笔形式。
func chargeTraffic(ctx context.Context, tx pgx.Tx, tenantID, subID, userID string, billed int64) error {
	return applyTrafficCharges(ctx, tx, tenantID, []trafficCharge{{subID: subID, userID: userID, billed: billed}})
}

// applyTrafficCharges 在调用方事务里把一批计费用量记到配额与流量包上（D-E-1）。
//
// 口径与逐笔记账完全相同，按 charges 的顺序（上报按 uid 升序）依次决定每笔怎么分：
// 先吃该订阅本周期剩余额度（取本周期所有限量流量行里最紧的一条；没有限量行就是
// 不限量，不动流量包），超出的部分按先到先扣从该用户的流量包里扣，两者都不够的
// 那部分仍记在套餐上（配额变负、下一轮停止下发）。同一用户的多条订阅共用流量包，
// 前一笔扣掉的后一笔看得见。
//
// 语句数与条数无关：一次锁全部配额行（ORDER BY id FOR UPDATE），有超额时再一次锁
// 涉及用户的流量包（按用户、先到先扣的顺序 FOR UPDATE），然后配额与流量包各一条
// UPDATE。所有上报走同一锁序：先配额行（按 id），再流量包，并发两份上报不会交叉
// 死锁；锁从第一条语句起只持有到本批写完，不再随条数线性拉长。
func applyTrafficCharges(ctx context.Context, tx pgx.Tx, tenantID string, charges []trafficCharge) error {
	subIDs := make([]string, 0, len(charges))
	seen := map[string]bool{}
	for _, c := range charges {
		if c.billed > 0 && !seen[c.subID] {
			seen[c.subID] = true
			subIDs = append(subIDs, c.subID)
		}
	}
	if len(subIDs) == 0 {
		return nil
	}

	// 本周期流量配额：全部锁上（含不限量行，与下面的 UPDATE 同一集合），剩余额度取限量行里最紧的
	planRoom := map[string]*int64{}
	rows, err := tx.Query(ctx, `
		SELECT q.subscription_id::text,
		       min(q.limit_value + q.adjusted - q.consumed) FILTER (WHERE q.limit_value IS NOT NULL)
		  FROM (SELECT subscription_id, limit_value, adjusted, consumed FROM quota_balances
		         WHERE tenant_id = $1 AND subscription_id = ANY($2::uuid[])
		           AND metric = 'traffic.bytes'
		           AND period_start <= now()
		           AND (period_end IS NULL OR period_end > now())
		         ORDER BY id FOR UPDATE) q
		 GROUP BY q.subscription_id`, tenantID, subIDs)
	if err != nil {
		return err
	}
	for rows.Next() {
		var sub string
		var room *int64
		if err := rows.Scan(&sub, &room); err != nil {
			rows.Close()
			return err
		}
		planRoom[sub] = room
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// 只有超出套餐剩余额度的用户才需要流量包
	var packUsers []string
	needPacks := map[string]bool{}
	for _, c := range charges {
		if room := planRoom[c.subID]; c.billed > 0 && room != nil && c.billed > max(*room, 0) && !needPacks[c.userID] {
			needPacks[c.userID] = true
			packUsers = append(packUsers, c.userID)
		}
	}
	type openGrant struct {
		id   string
		left int64
	}
	grants := map[string][]*openGrant{} // 用户 → 按先到先扣排好的流量包
	if len(packUsers) > 0 {
		rows, err := tx.Query(ctx, `
			SELECT id::text, user_id::text, granted_bytes - consumed_bytes
			  FROM traffic_pack_grants
			 WHERE tenant_id = $1 AND user_id = ANY($2::uuid[])
			   AND consumed_bytes < granted_bytes
			 ORDER BY user_id, created_at, id FOR UPDATE`, tenantID, packUsers)
		if err != nil {
			return err
		}
		for rows.Next() {
			var g openGrant
			var user string
			if err := rows.Scan(&g.id, &user, &g.left); err != nil {
				rows.Close()
				return err
			}
			grants[user] = append(grants[user], &g)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}

	// 逐笔决定怎么分，只在内存里算；写回各一条语句
	planCharge := map[string]int64{}
	var planSubs []string
	packTake := map[string]int64{}
	var packIDs []string
	for _, c := range charges {
		if c.billed <= 0 {
			continue
		}
		room := planRoom[c.subID]
		fromPacks := int64(0)
		if room != nil && c.billed > max(*room, 0) {
			var packRemaining int64
			for _, g := range grants[c.userID] {
				packRemaining += g.left
			}
			_, fromPacks = splitTrafficCharge(c.billed, room, packRemaining)
			left := fromPacks
			for _, g := range grants[c.userID] {
				if left == 0 {
					break
				}
				take := min(left, g.left)
				if take == 0 {
					continue
				}
				if _, ok := packTake[g.id]; !ok {
					packIDs = append(packIDs, g.id)
				}
				packTake[g.id] += take
				g.left -= take
				left -= take
			}
		}
		if charge := c.billed - fromPacks; charge > 0 {
			if _, ok := planCharge[c.subID]; !ok {
				planSubs = append(planSubs, c.subID)
			}
			planCharge[c.subID] += charge
			// 同一订阅再来一笔时，剩余额度已经扣掉这一笔（与逐笔记账重读配额一致）
			if room != nil {
				left := *room - charge
				planRoom[c.subID] = &left
			}
		}
	}

	if len(packIDs) > 0 {
		takes := make([]int64, len(packIDs))
		for i, id := range packIDs {
			takes[i] = packTake[id]
		}
		if _, err := tx.Exec(ctx, `
			UPDATE traffic_pack_grants g SET consumed_bytes = g.consumed_bytes + c.take
			  FROM unnest($2::uuid[], $3::bigint[]) AS c(id, take)
			 WHERE g.tenant_id = $1 AND g.id = c.id`, tenantID, packIDs, takes); err != nil {
			return err
		}
	}
	if len(planSubs) > 0 {
		amounts := make([]int64, len(planSubs))
		for i, sub := range planSubs {
			amounts[i] = planCharge[sub]
		}
		if _, err := tx.Exec(ctx, `
			UPDATE quota_balances q SET consumed = q.consumed + c.amount
			  FROM unnest($2::uuid[], $3::bigint[]) AS c(subscription_id, amount)
			 WHERE q.tenant_id = $1 AND q.subscription_id = c.subscription_id
			   AND q.metric = 'traffic.bytes'
			   AND q.period_start <= now()
			   AND (q.period_end IS NULL OR q.period_end > now())`,
			tenantID, planSubs, amounts); err != nil {
			return err
		}
	}
	return nil
}

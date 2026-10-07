package nodefabric

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
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
	// Invalid 是没有记账的条目数：格式或数值不合规（见 parseTrafficReport），或者 uid
	// 不在这个节点当前的放行名单里。它们照样留档，只是不扣任何人的额度。
	Invalid int `json:"invalid"`
}

// maxTrafficEntryBytes 是一条上报里单个用户单个方向的上限：链路 2 Gbps 跑满两个上报
// 周期（节点每 60 秒报一次，按 120 秒算）约 30 GB。超过它的值不可能来自一个正常的节点。
const maxTrafficEntryBytes int64 = 30_000_000_000

// rolloverGraceSQL 是「结束了但还没滚动」的周期行仍照扣的时长。滚动每 10 分钟一次
// （跳过被锁的行时顺延到下一轮），一天足够覆盖滚动空窗。
const rolloverGraceSQL = "1 day"

// pushRetryAttempts 是记账事务遇到死锁或序列化失败时的总尝试次数。整个事务已回滚，
// 重放是安全的；节点不重试上报，失败一次这一分钟的流量就丢了。
const pushRetryAttempts = 3

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
//
// 节点只能扣自己当前放行名单里的用户（ListNodeUsers，缓存命中不查库）：uid 是自增的、
// 可以挨个枚举，一个泄露的节点令牌原先能把全站用户的额度扣成负数、全部停发。名单外与
// 数值不合规的条目照样留档，不扣费，计入 Invalid；不因为一条坏值拒收整份报文。
// 代价：用户刚被摘出名单（到期、用尽、换池）之后才报上来的最后一点流量不再计费。
//
// 不带上报编号，等同 ReportTrafficWithID(…, "")：老节点与测试夹具走这条。
func (s *Service) ReportTraffic(ctx context.Context, tenantID string, n *ServingNode, raw []byte) (*PushResult, error) {
	return s.reportTraffic(ctx, tenantID, n, raw, "")
}

// ReportTrafficWithID 是带幂等键的 ReportTraffic（w4deliver，审计 ledger N5）：pdnd 给每份
// 上报一个编号（请求头 X-Report-Id，经 NormalizeTrafficReportID 校验），结果不确定时原样
// 重发、带同一个编号。带编号的上报按 (节点, 编号) 去重：第一份入账；同一编号再来只留档一行
// 重复件、不扣费，不论隔了多久、是不是并发到达（迁移 00123 的唯一部分索引）。reportID 为空
// （老节点、编号不合规）照旧走 10 秒内容哈希去重。
func (s *Service) ReportTrafficWithID(ctx context.Context, tenantID string, n *ServingNode, raw []byte, reportID string) (*PushResult, error) {
	return s.reportTraffic(ctx, tenantID, n, raw, reportID)
}

func (s *Service) reportTraffic(ctx context.Context, tenantID string, n *ServingNode, raw []byte, reportID string) (*PushResult, error) {
	report, err := parseTrafficReport(raw)
	if err != nil {
		return nil, err
	}
	// 名单只对经认证得到的节点视图核对（epochKnown 只由 AuthenticateNode 等本包查询设置，
	// 包外拼不出来）。生产里 ReportTraffic 唯一的调用方是 api/node 的 uniPush，它一定先过
	// authNode（守卫 TestReportTrafficOnlyReachedThroughAuthentication）；包外测试手拼的
	// 节点视图没有池信息，不做这层核对。
	var allowed map[int64]struct{}
	if n.epochKnown {
		served, err := s.ListNodeUsers(ctx, tenantID, n)
		if err != nil {
			return nil, err
		}
		allowed = make(map[int64]struct{}, len(served))
		for _, u := range served {
			allowed[u.ID] = struct{}{}
		}
	}

	sum := sha256.Sum256(raw)
	now := time.Now()
	entries := make([]billedEntry, 0, len(report.entries))
	invalid := report.invalid
	for _, entry := range report.entries {
		if _, ok := allowed[entry.uid]; allowed != nil && !ok {
			invalid++
			continue
		}
		if entry.used <= 0 {
			continue
		}
		// 按节点倍率折算后计费
		entries = append(entries, billedEntry{uid: entry.uid, billed: int64(float64(entry.used) * n.TrafficRate)})
	}

	var out *PushResult
	for attempt := 1; ; attempt++ {
		out = &PushResult{TotalBytes: report.upload + report.download, Invalid: invalid}
		err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			rowID, duplicate, err := recordTrafficReport(ctx, tx, tenantID, n, report, raw, sum[:], reportID)
			if err != nil {
				return err
			}
			out.Duplicate = duplicate

			// --- 小时汇总（00099）：与留档同一事务，重复上报只计重复数 ---
			if err := rollupTrafficReport(ctx, tx, tenantID, rowID); err != nil {
				return err
			}
			if out.Duplicate {
				return nil // 留了档但不扣量
			}

			// --- 扣减配额 ---
			// 整份上报批量记账（chargeReportEntries）：按 uid 升序整理，一次查订阅、一次锁全部
			// 配额行（按 id 排序加锁）、必要时一次锁流量包，再各用一条语句写回。并发的两份
			// 上报以同一顺序加锁，不会交叉死锁（map 迭代顺序是随机的，所以先排序）。
			accepted, err := chargeReportEntries(ctx, tx, tenantID, entries, now)
			if err != nil {
				return err
			}
			out.Accepted = accepted // 订阅已删除的 uid 不计
			return nil
		})
		if err == nil || attempt >= pushRetryAttempts || !db.IsSerializationFailure(err) || ctx.Err() != nil {
			break
		}
		// 死锁的另一方（多半是周期滚动）刚回滚或提交，稍等一下再整笔重来
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(attempt*attempt) * 5 * time.Millisecond):
		}
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

type reportEntry struct {
	uid  int64
	used int64
}

// trafficReport 是解析、校验过的一份上报。
type trafficReport struct {
	entries          []reportEntry // 合规条目，按 uid 升序，同一 uid 的不同写法已合并
	keys             int           // 报文里的条目总数（留档的 user_count）
	invalid          int           // 不合规的条目数
	upload, download int64         // 合规条目的上下行合计（留档用，不会回绕）
}

// parseTrafficReport 解析 UniProxy 上报 {"uid":[上行, 下行], …}，逐项校验。
//
// 整份不是 JSON 对象才拒收（400）；单项不合规只跳过这一项并计数：uid 不是整数、值不是
// 恰好两个元素的数组、任一方向不是 0 到 maxTrafficEntryBytes 之间的整数。与 SQL 侧的
// 严格口径（app.node_traffic_payload_entries）同样拒收负数、错长度与超出 int64 的值，
// 计费与看板不再各说各话；单项有上限，合计也就不会溢出回绕。
func parseTrafficReport(raw []byte) (trafficReport, error) {
	var items map[string]json.RawMessage
	// 整份为 null 与 {} 一样当作空报文（与原先解析成 map 的行为一致）
	if err := json.Unmarshal(raw, &items); err != nil {
		return trafficReport{}, httpx.New(httpx.CodeBadRequest, "上报格式非法")
	}
	out := trafficReport{keys: len(items)}
	byUID := map[int64]int64{}
	for key, value := range items {
		uid, err := strconv.ParseInt(key, 10, 64)
		if err != nil {
			out.invalid++
			continue
		}
		up, down, ok := parseTrafficPair(value)
		if !ok {
			out.invalid++
			continue
		}
		out.upload += up
		out.download += down
		byUID[uid] += up + down
	}
	out.entries = make([]reportEntry, 0, len(byUID))
	for uid, used := range byUID {
		out.entries = append(out.entries, reportEntry{uid: uid, used: used})
	}
	slices.SortFunc(out.entries, func(a, b reportEntry) int { return cmp.Compare(a.uid, b.uid) })
	return out, nil
}

// parseTrafficPair 解析一项 [上行, 下行]：恰好两个元素，都是 0 到 maxTrafficEntryBytes 的整数。
func parseTrafficPair(value json.RawMessage) (up, down int64, ok bool) {
	var pair []json.Number
	dec := json.NewDecoder(bytes.NewReader(value))
	dec.UseNumber()
	if err := dec.Decode(&pair); err != nil || len(pair) != 2 {
		return 0, 0, false
	}
	var v [2]int64
	for i, num := range pair {
		n, err := strconv.ParseInt(num.String(), 10, 64)
		if err != nil || n < 0 || n > maxTrafficEntryBytes {
			return 0, 0, false
		}
		v[i] = n
	}
	return v[0], v[1], true
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

	// 本周期流量配额：全部锁上（含不限量行，与下面的 UPDATE 同一集合），剩余额度取限量行里最紧的。
	//
	// 「本周期」= 已开始（period_start <= now()）且没结束的行，外加每种周期（period）里
	// 开始得最晚、且结束不到 rolloverGraceSQL 的那一行。后者是滚动空窗：period_end 到了而
	// RollQuotaPeriods（aegis-admin 每 10 分钟）还没把它推进到下一期，原先这段时间选不到
	// 任何行，按「不限量」处理，流量既不进旧周期也不进新周期。现在照扣在这一行上，
	// 滚动时随 consumed 一起结转（滚动先锁行，与这里同一锁序：按 id）。结束更久的行不是
	// 滚动空窗，而是没跟着订阅对齐的旧数据（00102 修的那一类），照旧不扣。
	// 已开始的旧周期行一并锁上再在 Go 里挑（FOR UPDATE 不能和窗口函数同用）；按原地
	// 推进的写法，每种周期通常只有一行。
	type quotaRow struct {
		id, sub string
		room    *int64
	}
	var rows []quotaRow
	latest := map[string]time.Time{} // 订阅 + 周期种类 → 最晚的 period_start
	type candidate struct {
		quotaRow
		kind    string
		start   time.Time
		current bool
		lapsed  bool // 已结束但不到 rolloverGraceSQL
	}
	var candidates []candidate
	lockRows, err := tx.Query(ctx, `
		SELECT id::text, subscription_id::text, period, period_start,
		       period_end IS NULL OR period_end > now(),
		       period_end > now() - interval '`+rolloverGraceSQL+`',
		       CASE WHEN limit_value IS NOT NULL THEN limit_value + adjusted - consumed END
		  FROM quota_balances
		 WHERE tenant_id = $1 AND subscription_id = ANY($2::uuid[])
		   AND metric = 'traffic.bytes'
		   AND period_start <= now()
		 ORDER BY id FOR UPDATE`, tenantID, subIDs)
	if err != nil {
		return err
	}
	for lockRows.Next() {
		var c candidate
		var recent *bool
		if err := lockRows.Scan(&c.id, &c.sub, &c.kind, &c.start, &c.current, &recent, &c.room); err != nil {
			lockRows.Close()
			return err
		}
		c.lapsed = recent != nil && *recent
		if key := c.sub + "\x00" + c.kind; c.start.After(latest[key]) {
			latest[key] = c.start
		}
		candidates = append(candidates, c)
	}
	lockRows.Close()
	if err := lockRows.Err(); err != nil {
		return err
	}
	planRoom := map[string]*int64{}
	for _, c := range candidates {
		if !c.current && !(c.lapsed && c.start.Equal(latest[c.sub+"\x00"+c.kind])) {
			continue // 已经滚动过去的旧周期，或结束太久、不是滚动空窗
		}
		rows = append(rows, c.quotaRow)
		if _, seen := planRoom[c.sub]; !seen {
			planRoom[c.sub] = nil
		}
		if c.room != nil && (planRoom[c.sub] == nil || *c.room < *planRoom[c.sub]) {
			room := *c.room
			planRoom[c.sub] = &room
		}
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
	if len(planSubs) > 0 && len(rows) > 0 {
		// 按行写回：每一行加上它所属订阅这一批的计费量（与上面锁住、挑出的同一集合）。
		// 饱和加法：consumed 逼近 bigint 上限时封顶而不是溢出报错——一行溢出会让整份上报
		// 的事务回滚，同一报文里所有用户都不计。
		ids := make([]string, 0, len(rows))
		amounts := make([]int64, 0, len(rows))
		for _, r := range rows {
			if amount := planCharge[r.sub]; amount > 0 {
				ids = append(ids, r.id)
				amounts = append(amounts, amount)
			}
		}
		if len(ids) > 0 {
			if _, err := tx.Exec(ctx, `
				UPDATE quota_balances q
				   SET consumed = CASE WHEN q.consumed > 9223372036854775807 - c.amount
				                       THEN 9223372036854775807 ELSE q.consumed + c.amount END
				  FROM unnest($2::uuid[], $3::bigint[]) AS c(id, amount)
				 WHERE q.tenant_id = $1 AND q.id = c.id`,
				tenantID, ids, amounts); err != nil {
				return err
			}
		}
	}
	return nil
}

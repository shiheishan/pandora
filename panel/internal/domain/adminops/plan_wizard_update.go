package adminops

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 改套餐也一次改完，不用在四个弹窗之间跳。
//
// 对着 xboard 看的：它的套餐是一张扁平表 —— 价格是 JSON 字段、节点分组
// 是单个 group_id、没有版本概念，所以一个 save 就是一次 UPDATE。
//
// 我们的模型是六张表（products / plans / plan_versions / prices /
// plan_node_pools / quota_definitions），换来的是版本化：已经买了的用户
// 按购买那一刻的版本执行，之后改流量不会把他们的额度改掉。这个能力不能
// 为了少几个接口丢掉。
//
// 但界面没必要跟着表结构走。这里把「改基本资料、改流量与设备数、改价格、
// 改节点分组」编排成一次调用，版本怎么开、旧价格怎么归档，都在后面处理。
//
// 各字段的生效方式不一样，这不是实现细节，是业务语义：
//
//   - 基本资料（名字、可见性、开关）—— 直接改，立即生效
//   - 流量 / 设备数 —— 开新版本。已购用户留在旧版本上，额度不受影响
//   - 价格 —— 归档旧的、建新的。已经付过钱的订单不受影响
//   - 节点分组 —— 同样要开新版本。已发布的版本是冻结的，它的配额和
//     分组绑定都不可改（"only draft versions can modify node pools"），
//     这是快照语义的一部分：已购用户看到的东西不能被事后改动。
//
// 最后一条和 xboard 不一样，值得说清楚：xboard 的套餐是扁平表，改了
// group_id 所有人立刻换线路。我们做不到「立刻对所有人生效」——那正是
// 版本化换来的保护的另一面。加了新线路，已购用户要到续费换版本时才拿
// 得到。这个差异不能靠界面掩盖，得如实写在返回的说明里。

type UpdatePlanCompleteInput struct {
	ActorID string `json:"-"`

	// ExpectedRowVersion 用于乐观锁，来自详情接口。
	ExpectedRowVersion int64 `json:"expected_row_version"`

	// --- 基本资料 ---
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Visibility  string  `json:"visibility"`
	SortOrder   int     `json:"sort_order"`

	AllowNewPurchase     *bool    `json:"allow_new_purchase"`
	AllowRenewal         *bool    `json:"allow_renewal"`
	AllowUpgrade         *bool    `json:"allow_upgrade"`
	VisibleGroupIDs      []string `json:"visible_group_ids"`
	PurchaseLimitPerUser *int     `json:"purchase_limit_per_user"`
	StockTotal           *int     `json:"stock_total"`

	// --- 卖的是什么。为 nil 表示这次不动它 ---
	TrafficGB    *int64 `json:"traffic_gb"`
	MaxDevices   *int   `json:"max_devices"`
	ThrottleKbps *int   `json:"throttle_kbps"`

	// --- 价格与分组。为 nil 表示不动；给了空数组表示清空 ---
	Prices  *[]PlanPriceInput `json:"prices"`
	PoolIDs *[]string         `json:"pool_ids"`
}

type UpdatePlanCompleteOutput struct {
	Plan *CatalogPlanDetail `json:"plan"`
	// Changed 逐条说明这次实际改了什么、什么时候生效。
	// 界面直接把它显示给用户 —— 「保存成功」说明不了额度是立刻变了
	// 还是只对新用户生效。
	Changed []string `json:"changed"`
}

// UpdatePlanComplete 一次改完套餐的资料、额度、价格与节点分组。
func (s *Service) UpdatePlanComplete(ctx context.Context, tenantID, planID string,
	in UpdatePlanCompleteInput) (*UpdatePlanCompleteOutput, error) {

	before, err := s.GetPlan(ctx, tenantID, planID)
	if err != nil {
		return nil, err
	}

	changed := []string{}

	// 1) 基本资料
	rowVersion, err := s.UpdatePlan(ctx, tenantID, planID, UpdatePlanInput{
		ActorID: in.ActorID, ExpectedRowVersion: in.ExpectedRowVersion,
		Code: in.Code, Name: in.Name, Description: in.Description,
		Visibility: in.Visibility, VisibleGroupIDs: in.VisibleGroupIDs,
		// UpdatePlanInput 这三个是 bool 不是 *bool。传 nil 表示「这次不动它」，
		// 所以要从当前值兜底 —— 直接取零值会把「允许续费」悄悄关掉。
		AllowNewPurchase:     boolOr(in.AllowNewPurchase, before.AllowNewPurchase),
		AllowRenewal:         boolOr(in.AllowRenewal, before.AllowRenewal),
		AllowUpgrade:         boolOr(in.AllowUpgrade, before.AllowUpgrade),
		PurchaseLimitPerUser: in.PurchaseLimitPerUser,
		StockTotal:           in.StockTotal, SortOrder: in.SortOrder,
	})
	if err != nil {
		return nil, err
	}
	changed = append(changed, "套餐资料已更新")
	_ = rowVersion

	// 2) 额度与线路：合并进同一个新版本。
	//
	// 两者都存在已发布版本上，而已发布的版本是冻结的、子对象不可改。
	// 第一版是先滚版本改额度、再去改分组，结果分组那步撞上冻结保护：
	//   "plan version ... was frozen; its snapshot children are immutable"
	// 而且就算能改，分两次滚版本也会平白多出一个中间版本。
	quotaChanged := quotaDiffers(before, in)
	poolsChanged := in.PoolIDs != nil && poolsDiffer(before, *in.PoolIDs)
	if quotaChanged || poolsChanged {
		if err := s.rollPlanVersion(ctx, tenantID, planID, in); err != nil {
			return nil, fmt.Errorf("更新额度与线路: %w", err)
		}
		if quotaChanged {
			changed = append(changed,
				"流量与设备数已更新；新购买的用户按新额度，已经买了的用户仍按原额度")
		}
		if poolsChanged {
			changed = append(changed,
				"可用线路已更新；新购买的用户立即拿到，已经买了的用户要到续费时才切过来")
		}
	}

	// 4) 价格：归档不再需要的，补上新增的
	if in.Prices != nil {
		n, err := s.syncPlanPrices(ctx, tenantID, planID, *in.Prices, in.ActorID)
		if err != nil {
			return nil, fmt.Errorf("更新价格: %w", err)
		}
		if n > 0 {
			changed = append(changed,
				"价格已更新，只影响之后的新购与续费；已成交的订单不变")
		}
	}

	out, err := s.GetPlan(ctx, tenantID, planID)
	if err != nil {
		return nil, err
	}
	return &UpdatePlanCompleteOutput{Plan: out, Changed: changed}, nil
}

// quotaDiffers 判断这次提交有没有真的改动额度。
func quotaDiffers(before *CatalogPlanDetail, in UpdatePlanCompleteInput) bool {
	if in.TrafficGB == nil && in.MaxDevices == nil && in.ThrottleKbps == nil {
		return false
	}
	cur := currentVersion(before)
	if cur == nil {
		return true
	}
	if in.MaxDevices != nil && !sameIntPtr(cur.MaxDevices, in.MaxDevices) {
		return true
	}
	if in.ThrottleKbps != nil && !sameIntPtr(cur.ThrottleKbps, in.ThrottleKbps) {
		return true
	}
	if in.TrafficGB != nil {
		var curGB int64 = -1
		for _, q := range cur.Quotas {
			if q.Metric == "traffic.bytes" && q.Limit != nil {
				curGB = *q.Limit / bytesPerGB
			}
		}
		if curGB != *in.TrafficGB {
			return true
		}
	}
	return false
}

func currentVersion(p *CatalogPlanDetail) *VersionRow {
	if p == nil || p.CurrentVersionID == nil {
		return nil
	}
	for i := range p.Versions {
		if p.Versions[i].ID == *p.CurrentVersionID {
			return &p.Versions[i]
		}
	}
	return nil
}

// poolsDiffer 判断线路清单有没有真的变化。
// 没变就别滚版本 —— 每保存一次多一个版本，列表很快没法看。
func poolsDiffer(before *CatalogPlanDetail, want []string) bool {
	cur := currentVersion(before)
	have := []string{}
	if cur != nil {
		have = cur.PoolIDs
	}
	if len(have) != len(want) {
		return true
	}
	seen := map[string]bool{}
	for _, x := range have {
		seen[x] = true
	}
	for _, x := range want {
		if !seen[x] {
			return true
		}
	}
	return false
}

func boolOr(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

func sameIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// pricesKey 把一档价格压成可比较的键：周期 + 币种唯一确定一档。
func pricesKey(interval string, count int16, currency string) string {
	return fmt.Sprintf("%s/%d/%s", interval, count, currency)
}

// syncPlanPrices 让在售价格和提交的清单一致，返回改动条数。
//
// 不做「原地改金额」—— prices 表的一行会被历史订单引用，改掉它等于篡改
// 已成交订单的单价。所以是归档旧的、建新的：老订单仍指向老那一行。
func (s *Service) syncPlanPrices(ctx context.Context, tenantID, planID string,
	want []PlanPriceInput, actorID string) (int, error) {
	wanted := map[string]PlanPriceInput{}
	for _, p := range want {
		candidate := CreatePriceInput{
			ActorID: actorID, Currency: p.Currency, UnitAmount: p.UnitAmount,
			BillingInterval: p.BillingInterval, IntervalCount: p.IntervalCount,
			TrialDays: p.TrialDays,
		}
		if err := validatePrice(candidate); err != nil {
			return 0, err
		}
		wanted[pricesKey(p.BillingInterval, p.IntervalCount, p.Currency)] = p
	}

	if err := s.requireP0BSales(); err != nil {
		return 0, err
	}
	changes := 0
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		// 锁套餐把同一套餐的价格同步串行化；归档和补建必须同生共死。
		var productID, status string
		if err := tx.QueryRow(ctx, `SELECT product_id::text,status FROM plans
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, planID).
			Scan(&productID, &status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if status == "archived" {
			return httpx.New(httpx.CodeConflict, "已归档套餐不能更新价格")
		}

		have := map[string]PriceRow{}
		rows, err := tx.Query(ctx, `SELECT id,currency,unit_amount,billing_interval,
			interval_count,trial_days,status,user_group_id,valid_from,valid_until,row_version
			FROM prices WHERE tenant_id=$1 AND product_id=$2::uuid AND status='active'
			ORDER BY id FOR UPDATE`, tenantID, productID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var p PriceRow
			if err := rows.Scan(&p.ID, &p.Currency, &p.UnitAmount, &p.Interval, &p.Count,
				&p.TrialDays, &p.Status, &p.UserGroupID, &p.ValidFrom, &p.ValidUntil,
				&p.RowVersion); err != nil {
				rows.Close()
				return err
			}
			have[pricesKey(p.Interval, p.Count, p.Currency)] = p
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		keys := make([]string, 0, len(have))
		for k := range have {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			old := have[k]
			w, keep := wanted[k]
			if keep && w.UnitAmount == old.UnitAmount && w.TrialDays == old.TrialDays {
				continue
			}
			if _, err := tx.Exec(ctx, `UPDATE prices SET status='archived',
				row_version=row_version+1 WHERE tenant_id=$1 AND id=$2::uuid`,
				tenantID, old.ID); err != nil {
				return err
			}
			changes++
		}

		wkeys := make([]string, 0, len(wanted))
		for k := range wanted {
			wkeys = append(wkeys, k)
		}
		sort.Strings(wkeys)
		for _, k := range wkeys {
			w := wanted[k]
			old, exists := have[k]
			if exists && w.UnitAmount == old.UnitAmount && w.TrialDays == old.TrialDays {
				continue
			}
			var priceID string
			if err := tx.QueryRow(ctx, `INSERT INTO prices
				(tenant_id,product_id,currency,unit_amount,billing_interval,
				 interval_count,trial_days,status)
				VALUES($1,$2::uuid,$3,$4,$5,$6,$7,'active') RETURNING id::text`,
				tenantID, productID, w.Currency, w.UnitAmount, w.BillingInterval,
				w.IntervalCount, w.TrialDays).Scan(&priceID); err != nil {
				return err
			}
			changes++
		}
		if changes == 0 {
			return nil
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID, Action: "price.sync",
			ResourceType: "plan", ResourceID: &planID, APIDomain: "admin",
			Outcome: "success", RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"changes": changes},
		})
	})
	return changes, catalogResult(err)
}

// rollPlanVersion 开一个新版本承载新的额度，然后发布。
//
// 已经买了的用户留在旧版本上 —— 这正是版本存在的意义：改流量不该把
// 别人已经付过钱的额度改掉。
func (s *Service) rollPlanVersion(ctx context.Context, tenantID, planID string,
	in UpdatePlanCompleteInput) (retErr error) {

	before, err := s.GetPlan(ctx, tenantID, planID)
	if err != nil {
		return err
	}
	cur := currentVersion(before)

	var ver *VersionRow
	created := false
	for i := range before.Versions {
		if before.Versions[i].Status == "draft" && before.Versions[i].FrozenAt == nil {
			ver = &before.Versions[i]
			break
		}
	}
	if ver == nil {
		ver, err = s.CreatePlanVersion(ctx, tenantID, planID, in.ActorID)
		if err != nil {
			// 并发调用可能都在最初快照里看不到 draft；创建由套餐行锁和
			// one_draft_per_plan 唯一索引裁决后，失败方复查并复用赢家。
			latest, lookupErr := s.GetPlan(ctx, tenantID, planID)
			if lookupErr != nil {
				return err
			}
			for i := range latest.Versions {
				if latest.Versions[i].Status == "draft" && latest.Versions[i].FrozenAt == nil {
					ver = &latest.Versions[i]
					break
				}
			}
			if ver == nil {
				return err
			}
		} else {
			created = true
		}
	}
	// 新建 draft 在后续任一步失败时必须删除，否则 one_draft_per_plan 会让重试
	// 永久冲突。既有 draft 则复用并保留，下一次仍可继续重试。
	defer func() {
		if retErr != nil && created {
			if cleanupErr := s.deleteFreshDraft(ctx, tenantID, planID, ver.ID); cleanupErr != nil {
				retErr = fmt.Errorf("%w；清理失败的 draft %s: %v", retErr, ver.ID, cleanupErr)
			}
		}
	}()

	// 没填的沿用当前版本，不要因为「这次只想改流量」把设备数清掉。
	quotas := []QuotaInput{}
	trafficGB := int64(-1)
	if in.TrafficGB != nil {
		trafficGB = *in.TrafficGB
	} else if cur != nil {
		for _, q := range cur.Quotas {
			if q.Metric == "traffic.bytes" && q.Limit != nil {
				trafficGB = *q.Limit / bytesPerGB
			}
		}
	}
	if trafficGB > 0 {
		quotas = append(quotas, QuotaInput{
			Metric: "traffic.bytes", Limit: ptrInt64(trafficGB * bytesPerGB),
			Unit: "bytes", Period: "cycle"})
	}

	devices := in.MaxDevices
	if devices == nil && cur != nil {
		devices = cur.MaxDevices
	}
	if devices != nil && *devices > 0 {
		quotas = append(quotas, QuotaInput{
			Metric: "devices.active", Limit: ptrInt64(int64(*devices)),
			Unit: "count", Period: "cycle"})
	}

	throttle := in.ThrottleKbps
	if throttle == nil && cur != nil {
		throttle = cur.ThrottleKbps
	}

	strategy := "billing_cycle"
	overage := "suspend"
	if cur != nil {
		if cur.QuotaResetStrategy != "" {
			strategy = cur.QuotaResetStrategy
		}
		if cur.OveragePolicy != "" {
			overage = cur.OveragePolicy
		}
	}

	verRow, err := s.UpdatePlanVersion(ctx, tenantID, planID, ver.ID,
		VersionSemanticsInput{
			ActorID: in.ActorID, ExpectedRowVersion: ver.RowVersion,
			QuotaResetStrategy: strategy,
			GraceKeepsService:  true, RenewalExtendsPeriod: true,
			RenewalResetsQuota: true, RenewalKeepsAddons: true,
			MaxDevices: devices, ThrottleKbps: throttle,
			OveragePolicy: overage, Quotas: quotas,
		})
	if err != nil {
		return err
	}

	// 线路：这次给了就用新的，没给就沿用旧版本的。
	//
	// 沿用那一条不能省：只想改个流量却把线路丢了，发布之后所有人的订阅
	// 会瞬间变空。
	poolIDs := []string{}
	if in.PoolIDs != nil {
		poolIDs = *in.PoolIDs
	} else if cur != nil {
		poolIDs = cur.PoolIDs
	}
	if !created {
		// 复用的 draft 可能是上次失败留下的，先清空旧绑定；空数组也必须清空。
		if err := s.clearDraftPools(ctx, tenantID, ver.ID); err != nil {
			return err
		}
		verRow++
	}
	if len(poolIDs) > 0 {
		if err := s.bindPoolsToFreshVersion(ctx, tenantID, ver.ID, poolIDs); err != nil {
			return err
		}
		verRow++
	}

	after, err := s.GetPlan(ctx, tenantID, planID)
	if err != nil {
		return err
	}
	_, _, err = s.PublishPlanVersion(ctx, tenantID, planID, ver.ID,
		in.ActorID, after.RowVersion, verRow)
	return err
}

func (s *Service) clearDraftPools(ctx context.Context, tenantID, versionID string) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM plan_node_pools
			WHERE tenant_id=$1 AND plan_version_id=$2::uuid`, tenantID, versionID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE plan_versions SET row_version=row_version+1
			WHERE tenant_id=$1 AND id=$2::uuid AND status='draft'`, tenantID, versionID)
		return err
	})
}

func (s *Service) deleteFreshDraft(ctx context.Context, tenantID, planID, versionID string) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM plan_versions
			WHERE tenant_id=$1 AND plan_id=$2::uuid AND id=$3::uuid
			  AND status='draft' AND frozen_at IS NULL`, tenantID, planID, versionID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("draft 已不存在或已改变状态")
		}
		return nil
	})
}

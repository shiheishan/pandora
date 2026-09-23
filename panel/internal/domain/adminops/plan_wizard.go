package adminops

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 一次填完，一次建成一个能卖的套餐。
//
// 在这之前，建一个可售套餐要在界面上走五步、开五个弹窗：
//
//	建套餐 → 建版本 → 配流量与设备数 → 逐个加价格 → 发布
//
// 每一步都是独立接口、独立弹窗，中间任何一步走神就留下一个卖不出去的
// 半成品：套餐建好了但没有价格，或者配好了却忘了发布。而界面上看不出
// 它缺什么 —— 列表里它和正常套餐长得一模一样。
//
// 更要命的是新建弹窗里根本没有价格和流量字段，第一次用的人填完保存，
// 会理所当然地以为套餐建好了。
//
// 这里把五步合成一次调用。失败就把已经建出来的部分删掉，对调用方
// 要么得到一个完整可售的套餐，要么什么都没发生 —— 不留半成品。

// PlanPriceInput 是套餐向导里的一档价格。
type PlanPriceInput struct {
	// BillingInterval: month / year 等；IntervalCount 表示几个周期。
	// 「季付」就是 month × 3，不是一个独立的周期类型。
	BillingInterval string `json:"billing_interval"`
	IntervalCount   int16  `json:"interval_count"`
	// UnitAmount 是最小货币单位（分）。9.90 元填 990。
	UnitAmount int64  `json:"unit_amount"`
	Currency   string `json:"currency"`
	TrialDays  int16  `json:"trial_days"`
}

type CreatePlanCompleteInput struct {
	ActorID string `json:"-"`

	// --- 基本信息 ---
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

	// --- 卖的是什么 ---
	// TrafficGB 为 nil 表示不限流量；0 也是不限（前端留空即可）。
	TrafficGB *int64 `json:"traffic_gb"`
	// MaxDevices 为 nil 表示不限设备数。
	MaxDevices *int `json:"max_devices"`
	// ThrottleKbps 限速，nil 表示不限速。
	ThrottleKbps *int `json:"throttle_kbps"`
	// QuotaResetStrategy 决定流量什么时候清零，默认跟着账单周期。
	QuotaResetStrategy string `json:"quota_reset_strategy"`
	QuotaResetDay      *int16 `json:"quota_reset_day"`

	// --- 卖给谁、多少钱 ---
	PoolIDs []string         `json:"pool_ids"`
	Prices  []PlanPriceInput `json:"prices"`

	// Publish 为 true 时直接发布上架；false 留作草稿。
	Publish bool `json:"publish"`
}

type CreatePlanCompleteOutput struct {
	Plan      *CatalogPlanDetail `json:"plan"`
	VersionID string             `json:"version_id"`
	PriceIDs  []string           `json:"price_ids"`
	Published bool               `json:"published"`
}

const bytesPerGB = 1024 * 1024 * 1024

// CreatePlanComplete 建套餐、配额度、加价格、绑节点组、发布，一步到位。
func (s *Service) CreatePlanComplete(ctx context.Context, tenantID string,
	in CreatePlanCompleteInput) (*CreatePlanCompleteOutput, error) {

	if err := validateWizardInput(&in); err != nil {
		return nil, err
	}

	plan, err := s.CreatePlan(ctx, tenantID, CreatePlanInput{
		ActorID: in.ActorID, Code: in.Code, Name: in.Name,
		Description: in.Description, Visibility: in.Visibility,
		VisibleGroupIDs:  in.VisibleGroupIDs,
		AllowNewPurchase: in.AllowNewPurchase, AllowRenewal: in.AllowRenewal,
		AllowUpgrade:         in.AllowUpgrade,
		PurchaseLimitPerUser: in.PurchaseLimitPerUser,
		StockTotal:           in.StockTotal, SortOrder: in.SortOrder,
	})
	if err != nil {
		return nil, err
	}

	// 从这里开始任何一步失败，都要把已经建出来的套餐收掉。
	// 半成品比没有更糟：它在列表里和正常套餐没有区别。
	rollback := func(cause error, step string) error {
		if _, e := s.ArchivePlan(ctx, tenantID, plan.ID, in.ActorID, plan.RowVersion); e != nil {
			// 回滚也失败：如实报出来，让人知道有个残留要手工清理。
			return httpx.New(httpx.CodeInternal, fmt.Sprintf(
				"%s失败：%v；自动清理也失败了，请手工归档套餐 %s（%s）",
				step, cause, plan.Code, plan.ID))
		}
		if he, ok := cause.(*httpx.Error); ok {
			return he
		}
		return httpx.New(httpx.CodeBadRequest, step+"失败："+cause.Error())
	}

	version, err := s.CreatePlanVersion(ctx, tenantID, plan.ID, in.ActorID)
	if err != nil {
		return nil, rollback(err, "创建套餐版本")
	}

	quotas := make([]QuotaInput, 0, 2)
	if in.TrafficGB != nil && *in.TrafficGB > 0 {
		quotas = append(quotas, QuotaInput{
			Metric: "traffic.bytes", Limit: ptrInt64(*in.TrafficGB * bytesPerGB),
			Unit: "bytes", Period: "cycle",
		})
	}
	if in.MaxDevices != nil && *in.MaxDevices > 0 {
		quotas = append(quotas, QuotaInput{
			Metric: "devices.active", Limit: ptrInt64(int64(*in.MaxDevices)),
			Unit: "count", Period: "cycle",
		})
	}

	strategy := in.QuotaResetStrategy
	if strategy == "" {
		strategy = "billing_cycle"
	}
	versionRow, err := s.UpdatePlanVersion(ctx, tenantID, plan.ID, version.ID,
		VersionSemanticsInput{
			ActorID: in.ActorID, ExpectedRowVersion: version.RowVersion,
			QuotaResetStrategy: strategy, QuotaResetDay: in.QuotaResetDay,
			GraceKeepsService:    true,
			RenewalExtendsPeriod: true,
			RenewalResetsQuota:   true,
			RenewalKeepsAddons:   true,
			MaxDevices:           in.MaxDevices,
			ThrottleKbps:         in.ThrottleKbps,
			OveragePolicy:        "suspend", // 流量用完即停服；合法值只有 suspend/throttle/metered_billing
			Quotas:               quotas,
			// 不在这里传 PoolIDs：UpdatePlanVersion 明确把节点池绑定划给了
			// 专用端点（validateVersionUpdatePoolContract），传了会被拒。
			// 下面用 bindPoolsToFreshVersion 单独绑。
		})
	if err != nil {
		return nil, rollback(err, "配置额度与节点分组")
	}

	if len(in.PoolIDs) > 0 {
		if err := s.bindPoolsToFreshVersion(ctx, tenantID, version.ID, in.PoolIDs); err != nil {
			return nil, rollback(err, "绑定节点分组")
		}
		versionRow++ // 绑定把版本行推进了一格，发布时的乐观锁要跟上
	}

	priceIDs := make([]string, 0, len(in.Prices))
	for i, p := range in.Prices {
		row, err := s.CreatePlanPrice(ctx, tenantID, plan.ID, CreatePriceInput{
			ActorID: in.ActorID, Currency: p.Currency, UnitAmount: p.UnitAmount,
			BillingInterval: p.BillingInterval, IntervalCount: p.IntervalCount,
			TrialDays: p.TrialDays,
		})
		if err != nil {
			return nil, rollback(err, fmt.Sprintf("创建第 %d 档价格", i+1))
		}
		priceIDs = append(priceIDs, row.ID)
	}

	out := &CreatePlanCompleteOutput{
		Plan: plan, VersionID: version.ID, PriceIDs: priceIDs,
	}
	if !in.Publish {
		return out, nil
	}

	if _, _, err := s.PublishPlanVersion(ctx, tenantID, plan.ID, version.ID,
		in.ActorID, plan.RowVersion, versionRow); err != nil {
		return nil, rollback(err, "发布套餐")
	}
	out.Published = true
	return out, nil
}

// validateWizardInput 在动手建任何东西之前先把话说清楚。
//
// 这些校验底层函数其实也会做，但那时套餐已经建了一半，错误信息还是
// 底层的措辞（比如「unit_amount 必须为正」），看不出是第几档价格的问题。
func validateWizardInput(in *CreatePlanCompleteInput) error {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)

	fields := map[string]string{}
	if in.Code == "" {
		fields["code"] = "请填写套餐代码"
	}
	if in.Name == "" {
		fields["name"] = "请填写套餐名称"
	}
	if in.Publish && len(in.Prices) == 0 {
		fields["prices"] = "要上架就至少得有一档价格，否则用户看得到却买不了"
	}
	if in.Publish && len(in.PoolIDs) == 0 {
		fields["pool_ids"] = "要上架就得选节点分组，否则买了也没有线路可用"
	}
	if in.TrafficGB != nil && *in.TrafficGB < 0 {
		fields["traffic_gb"] = "流量不能是负数；不限流量请留空"
	}
	if in.MaxDevices != nil && *in.MaxDevices < 0 {
		fields["max_devices"] = "设备数不能是负数；不限请留空"
	}

	seen := map[string]int{}
	for i, p := range in.Prices {
		n := i + 1
		if p.UnitAmount <= 0 {
			fields[fmt.Sprintf("prices.%d.unit_amount", i)] =
				fmt.Sprintf("第 %d 档价格要大于 0", n)
		}
		if p.IntervalCount <= 0 {
			fields[fmt.Sprintf("prices.%d.interval_count", i)] =
				fmt.Sprintf("第 %d 档的周期数要大于 0", n)
		}
		if p.Currency == "" {
			fields[fmt.Sprintf("prices.%d.currency", i)] =
				fmt.Sprintf("第 %d 档没有选币种", n)
		}
		// 同一个周期配两档价格，下单时不知道该用哪个。
		key := fmt.Sprintf("%s/%d/%s", p.BillingInterval, p.IntervalCount, p.Currency)
		if prev, dup := seen[key]; dup {
			fields[fmt.Sprintf("prices.%d.billing_interval", i)] =
				fmt.Sprintf("第 %d 档和第 %d 档的周期与币种完全相同", n, prev)
		}
		seen[key] = n
	}

	if len(fields) > 0 {
		return httpx.Invalid(fields)
	}
	return nil
}

func ptrInt64(v int64) *int64 { return &v }

// bindPoolsToFreshVersion 给一个刚创建、尚无任何绑定的 draft 版本绑节点分组。
//
// 常规路径是 POST /v1/plans/{id}/pools（internal/api/admin/pools.go），
// 那里要应付已上架版本的改绑：既有绑定的前后差异审计、并发改绑的确定性
// 加锁顺序、乐观锁冲突。这里刻意不重复那一套 —— 版本是本次调用刚建出来
// 的，还没发布，也不可能有别人正在改它，那些保护没有对象。
//
// 但校验一条都不能省：分组必须存在且未被禁用，否则套餐上架后用户买到手
// 会发现没有线路可用 —— 那正是这个向导要杜绝的半成品。
func (s *Service) bindPoolsToFreshVersion(ctx context.Context,
	tenantID, versionID string, poolIDs []string) error {

	seen := map[string]bool{}
	unique := make([]string, 0, len(poolIDs))
	for _, id := range poolIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return nil
	}

	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		for _, pid := range unique {
			var ok string
			err := tx.QueryRow(ctx, `
				SELECT id::text FROM node_pools
				 WHERE tenant_id=$1 AND id=$2::uuid AND status <> 'disabled'`,
				tenantID, pid).Scan(&ok)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.Invalid(map[string]string{
					"pool_ids": "包含不存在或已禁用的节点分组",
				})
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO plan_node_pools (tenant_id, plan_version_id, pool_id)
				VALUES ($1, $2::uuid, $3::uuid)`, tenantID, versionID, pid); err != nil {
				return err
			}
		}
		_, err := tx.Exec(ctx, `
			UPDATE plan_versions SET row_version = row_version + 1
			 WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, versionID)
		return err
	})
}

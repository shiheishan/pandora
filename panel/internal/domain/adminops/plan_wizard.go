// [INPUT]: 依赖 catalog.go 的 prepareCreatePlanInput/validatePrice/validateVersionSemantics 校验与 createPlanTx/createPlanVersionTx/updatePlanVersionTx/createPlanPriceTx/publishPlanVersionTx/loadPlanTx 事务体，依赖 platform/db、platform/httpx
// [OUTPUT]: 对外提供 CreatePlanComplete、CreatePlanCompleteInput/Output、PlanPriceInput；包内提供 bindPoolsTx、wizardVersionSemantics 与 bytesPerGB
// [POS]: adminops 套餐向导的「一次建成」：事务外校验后把建壳、版本、额度、线路、价格、发布编排进同一个事务；plan_wizard_update.go 是它的「一次改完」兄弟，并复用 bindPoolsTx
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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
// 这里把五步合成一次调用、一个事务。对调用方要么得到一个完整可售的
// 套餐，要么什么都没发生 —— 不留半成品。

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
	// Plan 是建成（含版本、价格与发布结果）之后的详情。
	Plan      *CatalogPlanDetail `json:"plan"`
	VersionID string             `json:"version_id"`
	PriceIDs  []string           `json:"price_ids"`
	Published bool               `json:"published"`
}

const bytesPerGB = 1024 * 1024 * 1024

// CreatePlanComplete 建套餐、配额度、加价格、绑节点组、发布，一步到位。
//
// 整个编排在一个事务里（与 UpdatePlanComplete 同理，缺陷 12 的同类）：此前
// 每一步各自提交，失败时靠「归档刚建的套餐」补救 —— 归档不是删除，留下一个
// 已归档的空壳占着 code；补救本身也失败时，只能在报错里请管理员手工清理。
// 现在任何一步失败，库里什么都不会多出来。
func (s *Service) CreatePlanComplete(ctx context.Context, tenantID string,
	in CreatePlanCompleteInput) (*CreatePlanCompleteOutput, error) {

	// 能在事务外判的都先判：失败时一行都不碰。
	if err := validateWizardInput(&in); err != nil {
		return nil, err
	}
	planInput := CreatePlanInput{
		ActorID: in.ActorID, Code: in.Code, Name: in.Name,
		Description: in.Description, Visibility: in.Visibility,
		VisibleGroupIDs:  in.VisibleGroupIDs,
		AllowNewPurchase: in.AllowNewPurchase, AllowRenewal: in.AllowRenewal,
		AllowUpgrade:         in.AllowUpgrade,
		PurchaseLimitPerUser: in.PurchaseLimitPerUser,
		StockTotal:           in.StockTotal, SortOrder: in.SortOrder,
	}
	if err := prepareCreatePlanInput(&planInput); err != nil {
		return nil, err
	}
	prices := make([]CreatePriceInput, 0, len(in.Prices))
	for _, p := range in.Prices {
		price := CreatePriceInput{
			ActorID: in.ActorID, Currency: p.Currency, UnitAmount: p.UnitAmount,
			BillingInterval: p.BillingInterval, IntervalCount: p.IntervalCount,
			TrialDays: p.TrialDays,
		}
		if err := validatePrice(price); err != nil {
			return nil, err
		}
		prices = append(prices, price)
	}
	// 加价格与发布都受销售开关控制
	if len(prices) > 0 || in.Publish {
		if err := s.requireP0BSales(); err != nil {
			return nil, err
		}
	}
	semantics := wizardVersionSemantics(in)
	if err := validateVersionSemantics(semantics); err != nil {
		return nil, err
	}

	out := &CreatePlanCompleteOutput{PriceIDs: make([]string, 0, len(prices))}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		planID, err := createPlanTx(ctx, tx, tenantID, planInput)
		if err != nil {
			return err
		}
		version, err := s.createPlanVersionTx(ctx, tx, tenantID, planID, in.ActorID)
		if err != nil {
			return err
		}
		out.VersionID = version.ID
		semantics.ExpectedRowVersion = version.RowVersion
		// 不在语义里传 PoolIDs：UpdatePlanVersion 把节点池绑定划给了专用端点
		// （validateVersionUpdatePoolContract），这里用 bindPoolsTx 单独绑。
		if _, err := s.updatePlanVersionTx(ctx, tx, tenantID, planID, version.ID, semantics); err != nil {
			return err
		}
		if _, err := bindPoolsTx(ctx, tx, tenantID, version.ID, in.PoolIDs); err != nil {
			return err
		}
		for _, price := range prices {
			row, err := createPlanPriceTx(ctx, tx, tenantID, planID, price)
			if err != nil {
				return err
			}
			out.PriceIDs = append(out.PriceIDs, row.ID)
		}

		if in.Publish {
			// 发布用的两个乐观锁令牌就地读：本事务刚把它们各推进了若干格。
			var planRow, versionRow int64
			if err := tx.QueryRow(ctx, `SELECT p.row_version, v.row_version
				FROM plans p JOIN plan_versions v ON v.tenant_id=p.tenant_id AND v.plan_id=p.id
				WHERE p.tenant_id=$1 AND p.id=$2::uuid AND v.id=$3::uuid`,
				tenantID, planID, version.ID).Scan(&planRow, &versionRow); err != nil {
				return err
			}
			if _, _, err := s.publishPlanVersionTx(ctx, tx, tenantID, planID, version.ID,
				in.ActorID, planRow, versionRow); err != nil {
				return err
			}
			out.Published = true
		}

		out.Plan = &CatalogPlanDetail{Versions: []VersionRow{}, Prices: []PriceRow{}}
		return loadPlanTx(ctx, tx, tenantID, planID, out.Plan)
	})
	if err != nil {
		return nil, catalogResult(err)
	}
	return out, nil
}

// wizardVersionSemantics 把向导的「流量 / 设备 / 限速 / 重置策略」翻成版本语义。
// 流量与设备数不填或填 0 都是不限，不写配额行。
func wizardVersionSemantics(in CreatePlanCompleteInput) VersionSemanticsInput {
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
	return VersionSemanticsInput{
		ActorID:            in.ActorID,
		QuotaResetStrategy: strategy, QuotaResetDay: in.QuotaResetDay,
		GraceKeepsService:    true,
		RenewalExtendsPeriod: true,
		RenewalResetsQuota:   true,
		RenewalKeepsAddons:   true,
		MaxDevices:           in.MaxDevices,
		ThrottleKbps:         in.ThrottleKbps,
		OveragePolicy:        "suspend", // 流量用完即停服；合法值只有 suspend/throttle/metered_billing
		Quotas:               quotas,
	}
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

// bindPoolsTx 在调用方的事务里绑定，返回实际绑定的分组数（去重、去空之后）。
// 绑定了至少一个分组时版本行推进一格，发布时的乐观锁要跟上。
func bindPoolsTx(ctx context.Context, tx pgx.Tx,
	tenantID, versionID string, poolIDs []string) (int, error) {

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
		return 0, nil
	}

	for _, pid := range unique {
		var ok string
		err := tx.QueryRow(ctx, `
			SELECT id::text FROM node_pools
			 WHERE tenant_id=$1 AND id=$2::uuid AND status <> 'disabled'`,
			tenantID, pid).Scan(&ok)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, httpx.Invalid(map[string]string{
				"pool_ids": "包含不存在或已禁用的节点分组",
			})
		}
		if err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO plan_node_pools (tenant_id, plan_version_id, pool_id)
			VALUES ($1, $2::uuid, $3::uuid)`, tenantID, versionID, pid); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE plan_versions SET row_version = row_version + 1
		 WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, versionID); err != nil {
		return 0, err
	}
	return len(unique), nil
}

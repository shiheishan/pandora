package handler

import (
	"time"

	"github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// --- 请求 DTO ---

// createPlanRequest 是新建套餐的请求体：kind 决定后面的字段哪组生效，业务校验在 service。
type createPlanRequest struct {
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	// Prices 只给订阅套餐：四档周期价。
	Prices *[]tierRequest `json:"prices"`
	// UnitAmount、ValidityDays 只给一次性套餐；validity_days 空 = 永久。
	UnitAmount   *int64 `json:"unit_amount"`
	ValidityDays *int   `json:"validity_days"`
	// 用量（流量字节、设备数、限速 kbps）与请求体平铺，nil = 不限。
	usageInput
	// PoolIDs 是要绑定的节点池。
	PoolIDs []string `json:"pool_ids"`
	// Reset 是流量重置，空 = 不卖。
	Reset *resetInput `json:"reset"`
	// Visibility 是可见性；visible_group_ids 只在 visibility = group 时用。
	Visibility      string   `json:"visibility"`
	VisibleGroupIDs []string `json:"visible_group_ids"`
	// AllowNewPurchase、AllowRenewal、AllowUpgrade 空 = 缺省（见 planCreateFrom）。
	AllowNewPurchase *bool `json:"allow_new_purchase"`
	AllowRenewal     *bool `json:"allow_renewal"`
	AllowUpgrade     *bool `json:"allow_upgrade"`
	Recommended      bool  `json:"recommended"`
	// Publish 为真时建成并直接发布上架。
	Publish bool `json:"publish"`
}

// tierRequest 是订阅套餐的一档价：unit_amount 是整数分，空 = 不卖这一档（改价时）。
type tierRequest struct {
	BillingInterval string `json:"billing_interval"`
	IntervalCount   int    `json:"interval_count"`
	UnitAmount      *int64 `json:"unit_amount"`
}

// usageInput 是用量三项，空 = 不限；嵌入请求体，字段平铺在顶层。
type usageInput struct {
	TrafficBytes *int64 `json:"traffic_bytes"`
	MaxDevices   *int   `json:"max_devices"`
	ThrottleKbps *int   `json:"throttle_kbps"`
}

// resetInput 是流量重置：price 是每次的整数分，limit 空 = 不限次数。
type resetInput struct {
	Price int64 `json:"price"`
	Limit *int  `json:"limit"`
}

// basicUpdateRequest 是改名称与说明。
type basicUpdateRequest struct {
	ExpectedRowVersion int64   `json:"expected_row_version"`
	Name               string  `json:"name"`
	Description        *string `json:"description"`
}

// salesUpdateRequest 是改销售设置；visible_group_ids 只在 visibility = group 时用。
type salesUpdateRequest struct {
	ExpectedRowVersion int64    `json:"expected_row_version"`
	Visibility         string   `json:"visibility"`
	VisibleGroupIDs    []string `json:"visible_group_ids"`
	AllowNewPurchase   bool     `json:"allow_new_purchase"`
	AllowRenewal       bool     `json:"allow_renewal"`
	AllowUpgrade       bool     `json:"allow_upgrade"`
	Recommended        bool     `json:"recommended"`
}

// resetUpdateRequest 是改流量重置：enabled 为假时 price、limit 不看。
type resetUpdateRequest struct {
	ExpectedRowVersion int64 `json:"expected_row_version"`
	Enabled            bool  `json:"enabled"`
	Price              int64 `json:"price"`
	Limit              *int  `json:"limit"`
}

// pricesUpdateRequest 是改价：订阅套餐给 prices（每档 unit_amount 空 = 不卖这一档），
// 一次性套餐给 unit_amount + validity_days；两组都没给回 422。
type pricesUpdateRequest struct {
	ExpectedRowVersion int64          `json:"expected_row_version"`
	Prices             *[]tierRequest `json:"prices"`
	UnitAmount         *int64         `json:"unit_amount"`
	ValidityDays       *int           `json:"validity_days"`
}

// poolsUpdateRequest 是整体替换套餐线路，空数组 = 清空。
type poolsUpdateRequest struct {
	ExpectedRowVersion int64    `json:"expected_row_version"`
	PoolIDs            []string `json:"pool_ids"`
}

// usageUpdateRequest 是改用量，三项都空 = 不限。
type usageUpdateRequest struct {
	ExpectedRowVersion int64 `json:"expected_row_version"`
	usageInput
}

// expectedRowVersionRequest 是发布、归档这类只带乐观锁的写请求。
type expectedRowVersionRequest struct {
	ExpectedRowVersion int64 `json:"expected_row_version"`
}

// --- 请求到 service 输入的映射 ---

// invalidUUIDList 回 422：列表字段非法或重复，字段名同 JSON 键，文案按项目统一写法。
func invalidUUIDList(field string) error {
	return errs.Invalid(map[string]string{field: "必须是无重复的 UUID 列表"})
}

// parseUUIDList 把 UUID 字符串列表解析成不重复的 UUID 列表；非法或重复回 422，字段名同 JSON 键，空当空列表。
func parseUUIDList(field string, ids []string) ([]uuid.UUID, error) {
	if ids == nil {
		return []uuid.UUID{}, nil
	}
	seen := make(map[string]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, invalidUUIDList(field)
		}
		uid, err := uuid.Parse(id)
		if err != nil {
			return nil, invalidUUIDList(field)
		}
		seen[id] = true
		out = append(out, uid)
	}
	return out, nil
}

// planCreateFrom 把新建请求映射成 service.PlanCreate；类型与业务校验（kind 合不合法、价格档齐不齐等）交给 service。
func planCreateFrom(req createPlanRequest) (service.PlanCreate, error) {
	usage := service.UsageInput{
		TrafficBytes: req.TrafficBytes,
		MaxDevices:   req.MaxDevices,
		ThrottleKbps: req.ThrottleKbps,
	}
	out := service.PlanCreate{
		Kind:        req.Kind,
		Name:        req.Name,
		Description: req.Description,
		Usage:       usage,
		Sales: service.SalesInput{
			Visibility:       req.Visibility,
			AllowNewPurchase: req.AllowNewPurchase == nil || *req.AllowNewPurchase,
			AllowUpgrade:     req.AllowUpgrade == nil || *req.AllowUpgrade,
			Recommended:      req.Recommended,
		},
		Publish: req.Publish,
	}
	// allow_renewal 缺省 = 订阅套餐允许（一次性套餐库里 CHECK 恒 false，service 再校验）。
	if req.AllowRenewal != nil {
		out.Sales.AllowRenewal = *req.AllowRenewal
	} else {
		out.Sales.AllowRenewal = req.Kind == service.KindSubscription
	}
	var err error
	if out.PoolIDs, err = parseUUIDList("pool_ids", req.PoolIDs); err != nil {
		return service.PlanCreate{}, err
	}
	if out.Sales.VisibleGroupIDs, err = parseUUIDList("visible_group_ids", req.VisibleGroupIDs); err != nil {
		return service.PlanCreate{}, err
	}
	switch req.Kind {
	case service.KindSubscription:
		if req.Prices != nil {
			for _, t := range *req.Prices {
				if t.UnitAmount == nil {
					continue
				}
				out.Tiers = append(out.Tiers, service.TierInput{
					BillingInterval: t.BillingInterval,
					IntervalCount:   t.IntervalCount,
					UnitAmount:      money.FromCents(*t.UnitAmount),
				})
			}
		}
	case service.KindOneTime:
		if req.UnitAmount != nil {
			out.OneTime = &service.OneTimeInput{
				UnitAmount:   money.FromCents(*req.UnitAmount),
				ValidityDays: req.ValidityDays,
			}
		}
	}
	if req.Reset != nil {
		out.Reset = &service.ResetInput{Price: money.FromCents(req.Reset.Price), Limit: req.Reset.Limit}
	}
	return out, nil
}

// basicUpdateFrom 把改基本信息请求映射成 service.BasicUpdate。
func basicUpdateFrom(req basicUpdateRequest) service.BasicUpdate {
	return service.BasicUpdate{
		ExpectedRowVersion: req.ExpectedRowVersion,
		Name:               req.Name,
		Description:        req.Description,
	}
}

// salesUpdateFrom 把改销售设置请求映射成 service.SalesUpdate。
func salesUpdateFrom(req salesUpdateRequest) (service.SalesUpdate, error) {
	groups, err := parseUUIDList("visible_group_ids", req.VisibleGroupIDs)
	if err != nil {
		return service.SalesUpdate{}, err
	}
	return service.SalesUpdate{
		ExpectedRowVersion: req.ExpectedRowVersion,
		Sales: service.SalesInput{
			Visibility:       req.Visibility,
			VisibleGroupIDs:  groups,
			AllowNewPurchase: req.AllowNewPurchase,
			AllowRenewal:     req.AllowRenewal,
			AllowUpgrade:     req.AllowUpgrade,
			Recommended:      req.Recommended,
		},
	}, nil
}

// resetUpdateFrom 把改流量重置请求映射成 service.ResetUpdate。
func resetUpdateFrom(req resetUpdateRequest) service.ResetUpdate {
	return service.ResetUpdate{
		ExpectedRowVersion: req.ExpectedRowVersion,
		Enabled:            req.Enabled,
		Price:              money.FromCents(req.Price),
		Limit:              req.Limit,
	}
}

// pricesUpdateFrom 把改价请求映射成 service.PricesUpdate；两组价格都没给回 422。
func pricesUpdateFrom(req pricesUpdateRequest) (service.PricesUpdate, error) {
	out := service.PricesUpdate{ExpectedRowVersion: req.ExpectedRowVersion}
	if req.Prices != nil {
		for _, t := range *req.Prices {
			change := service.TierChange{
				BillingInterval: t.BillingInterval,
				IntervalCount:   t.IntervalCount,
			}
			if t.UnitAmount != nil {
				amount := money.FromCents(*t.UnitAmount)
				change.UnitAmount = &amount
			}
			out.Tiers = append(out.Tiers, change)
		}
		return out, nil
	}
	if req.UnitAmount != nil {
		out.OneTime = &service.OneTimeInput{
			UnitAmount:   money.FromCents(*req.UnitAmount),
			ValidityDays: req.ValidityDays,
		}
		return out, nil
	}
	return service.PricesUpdate{}, errs.Invalid(map[string]string{"prices": "请给出价格"})
}

// poolsUpdateFrom 把改线路请求映射成 service.PoolsUpdate。
func poolsUpdateFrom(req poolsUpdateRequest) (service.PoolsUpdate, error) {
	ids, err := parseUUIDList("pool_ids", req.PoolIDs)
	if err != nil {
		return service.PoolsUpdate{}, err
	}
	return service.PoolsUpdate{ExpectedRowVersion: req.ExpectedRowVersion, PoolIDs: ids}, nil
}

// usageUpdateFrom 把改用量请求映射成 service.UsageUpdate。
func usageUpdateFrom(req usageUpdateRequest) service.UsageUpdate {
	return service.UsageUpdate{
		ExpectedRowVersion: req.ExpectedRowVersion,
		Usage: service.UsageInput{
			TrafficBytes: req.TrafficBytes,
			MaxDevices:   req.MaxDevices,
			ThrottleKbps: req.ThrottleKbps,
		},
	}
}

// --- 响应 DTO ---

// priceRow 是一档价格；unit_amount 是整数分。
type priceRow struct {
	ID              string    `json:"id"`
	BillingInterval string    `json:"billing_interval"`
	IntervalCount   int       `json:"interval_count"`
	UnitAmount      int64     `json:"unit_amount"`
	ValidityDays    *int      `json:"validity_days"`
	Status          string    `json:"status"`
	CreatedAt       time.Time `json:"created_at"`
}

// resetRow 是流量重置；整个 reset 在套餐没开重置时为 null。
type resetRow struct {
	Price int64 `json:"price"`
	Limit *int  `json:"limit"`
}

// versionRow 是一个版本（用量快照）。
type versionRow struct {
	ID             string     `json:"id"`
	Version        int        `json:"version"`
	Status         string     `json:"status"`
	PublishedAt    *time.Time `json:"published_at"`
	TrafficBytes   *int64     `json:"traffic_bytes"`
	MaxDevices     *int       `json:"max_devices"`
	ThrottleKbps   *int       `json:"throttle_kbps"`
	CreatedByEmail *string    `json:"created_by_email"`
	CreatedAt      time.Time  `json:"created_at"`
}

// planRow 是后台套餐列表与详情共用的主体；version、draft_version、draft_version_id 与用量三项可为 null。
type planRow struct {
	ID                  string     `json:"id"`
	RowVersion          int64      `json:"row_version"`
	Code                string     `json:"code"`
	Name                string     `json:"name"`
	Description         *string    `json:"description"`
	Kind                string     `json:"kind"`
	Status              string     `json:"status"`
	Visibility          string     `json:"visibility"`
	Recommended         bool       `json:"recommended"`
	Prices              []priceRow `json:"prices"`
	TrafficBytes        *int64     `json:"traffic_bytes"`
	MaxDevices          *int       `json:"max_devices"`
	ThrottleKbps        *int       `json:"throttle_kbps"`
	Version             *int       `json:"version"`
	DraftVersion        *int       `json:"draft_version"`
	DraftVersionID      *string    `json:"draft_version_id"`
	PoolIDs             []string   `json:"pool_ids"`
	Reset               *resetRow  `json:"reset"`
	ActiveSubscriptions int64      `json:"active_subscriptions"`
	NodeCount           int64      `json:"node_count"`
}

// planDetailResponse 是后台套餐详情：planRow 的全部字段 + 销售细节与版本历史。
type planDetailResponse struct {
	planRow
	VisibleGroupIDs  []string     `json:"visible_group_ids"`
	AllowNewPurchase bool         `json:"allow_new_purchase"`
	AllowRenewal     bool         `json:"allow_renewal"`
	AllowUpgrade     bool         `json:"allow_upgrade"`
	Versions         []versionRow `json:"versions"`
}

type listPlansResponse struct {
	Plans []planRow `json:"plans"`
}

type getPlanResponse struct {
	Plan planDetailResponse `json:"plan"`
}

type createPlanResponse struct {
	Plan planDetailResponse `json:"plan"`
}

// --- service 类型到响应 DTO 的转换 ---

func priceRowFrom(p service.Price) priceRow {
	return priceRow{
		ID:              p.ID.String(),
		BillingInterval: p.BillingInterval,
		IntervalCount:   p.IntervalCount,
		UnitAmount:      money.MustCents(p.UnitAmount),
		ValidityDays:    p.ValidityDays,
		Status:          p.Status,
		CreatedAt:       p.CreatedAt,
	}
}

func priceRowsFrom(prices []service.Price) []priceRow {
	out := make([]priceRow, len(prices))
	for i, p := range prices {
		out[i] = priceRowFrom(p)
	}
	return out
}

func resetRowFrom(price *decimal.Decimal, limit *int) *resetRow {
	if price == nil {
		return nil
	}
	return &resetRow{Price: money.MustCents(*price), Limit: limit}
}

func versionRowFrom(v service.Version) versionRow {
	return versionRow{
		ID:             v.ID.String(),
		Version:        v.Version,
		Status:         v.Status,
		PublishedAt:    v.FrozenAt,
		TrafficBytes:   v.TrafficLimit(),
		MaxDevices:     v.MaxDevices,
		ThrottleKbps:   v.ThrottleKbps,
		CreatedByEmail: v.CreatedByEmail,
		CreatedAt:      v.CreatedAt,
	}
}

func versionRowsFrom(versions []service.Version) []versionRow {
	out := make([]versionRow, len(versions))
	for i, v := range versions {
		out[i] = versionRowFrom(v)
	}
	return out
}

// uuidsToStrings 把 UUID 列表转成字符串列表；空列表返回空切片（JSON 里是 [] 不是 null）。
func uuidsToStrings(ids []uuid.UUID) []string {
	if len(ids) == 0 {
		return []string{}
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

func uuidPtrToString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}

// planRowFrom 组装列表与详情共用的主体字段；activeSubs、nodeCount 由调用方批量取。
func planRowFrom(s service.PlanSummary, activeSubs, nodeCount int64) planRow {
	p := s.Plan
	return planRow{
		ID:                  p.ID.String(),
		RowVersion:          p.RowVersion,
		Code:                p.Code,
		Name:                p.Name,
		Description:         p.Description,
		Kind:                p.Kind,
		Status:              p.Status,
		Visibility:          p.Visibility,
		Recommended:         p.Recommended,
		Prices:              priceRowsFrom(s.Prices),
		TrafficBytes:        s.TrafficBytes,
		MaxDevices:          s.MaxDevices,
		ThrottleKbps:        s.ThrottleKbps,
		Version:             s.Version,
		DraftVersion:        s.DraftVersion,
		DraftVersionID:      uuidPtrToString(s.DraftVersionID),
		PoolIDs:             uuidsToStrings(s.PoolIDs),
		Reset:               resetRowFrom(p.ResetPrice, p.ResetLimit),
		ActiveSubscriptions: activeSubs,
		NodeCount:           nodeCount,
	}
}

// planDetailFrom 组装后台详情（versions、prices 全量、销售细节）。
func planDetailFrom(d service.PlanDetail) planDetailResponse {
	row := planRowFrom(service.PlanSummary{Plan: d.Plan, PlanUsage: d.PlanUsage, Prices: d.Prices}, 0, 0)
	return planDetailResponse{
		planRow:          row,
		VisibleGroupIDs:  uuidsToStrings(d.VisibleGroupIDs),
		AllowNewPurchase: d.AllowNewPurchase,
		AllowRenewal:     d.AllowRenewal,
		AllowUpgrade:     d.AllowUpgrade,
		Versions:         versionRowsFrom(d.Versions),
	}
}

// planFilterFrom 把查询参数放进 PlanFilter，原样传给 service（校验在 service）。
func planFilterFrom(c *gin.Context) service.PlanFilter {
	return service.PlanFilter{
		Kind:   c.Query("kind"),
		Status: c.Query("status"),
		Q:      c.Query("q"),
	}
}

// collectPlanIDs 取一批套餐行的 id。
func collectPlanIDs(rows []service.PlanSummary) []uuid.UUID {
	out := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

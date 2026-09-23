package adminops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

var catalogCodePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

type CreatePlanInput struct {
	ActorID              string     `json:"-"`
	Code                 string     `json:"code"`
	Name                 string     `json:"name"`
	Description          *string    `json:"description"`
	Visibility           string     `json:"visibility"`
	VisibleGroupIDs      []string   `json:"visible_group_ids"`
	VisibleFrom          *time.Time `json:"visible_from"`
	VisibleUntil         *time.Time `json:"visible_until"`
	AllowNewPurchase     *bool      `json:"allow_new_purchase"`
	AllowRenewal         *bool      `json:"allow_renewal"`
	AllowUpgrade         *bool      `json:"allow_upgrade"`
	PurchaseLimitPerUser *int       `json:"purchase_limit_per_user"`
	StockTotal           *int       `json:"stock_total"`
	SortOrder            int        `json:"sort_order"`
}

type UpdatePlanInput struct {
	ActorID              string     `json:"-"`
	ExpectedRowVersion   int64      `json:"expected_row_version"`
	Code                 string     `json:"code"`
	Name                 string     `json:"name"`
	Description          *string    `json:"description"`
	Visibility           string     `json:"visibility"`
	VisibleGroupIDs      []string   `json:"visible_group_ids"`
	VisibleFrom          *time.Time `json:"visible_from"`
	VisibleUntil         *time.Time `json:"visible_until"`
	AllowNewPurchase     bool       `json:"allow_new_purchase"`
	AllowRenewal         bool       `json:"allow_renewal"`
	AllowUpgrade         bool       `json:"allow_upgrade"`
	PurchaseLimitPerUser *int       `json:"purchase_limit_per_user"`
	StockTotal           *int       `json:"stock_total"`
	SortOrder            int        `json:"sort_order"`
}

type EntitlementInput struct {
	Code  string          `json:"code"`
	Value json.RawMessage `json:"value"`
}

type QuotaInput struct {
	Metric string `json:"metric"`
	Limit  *int64 `json:"limit"`
	Unit   string `json:"unit"`
	Period string `json:"period"`
}

type VersionSemanticsInput struct {
	ActorID              string             `json:"-"`
	ExpectedRowVersion   int64              `json:"expected_row_version"`
	QuotaResetStrategy   string             `json:"quota_reset_strategy"`
	QuotaResetDay        *int16             `json:"quota_reset_day"`
	GracePeriodHours     int                `json:"grace_period_hours"`
	GraceKeepsService    bool               `json:"grace_keeps_service"`
	RenewalExtendsPeriod bool               `json:"renewal_extends_period"`
	RenewalResetsQuota   bool               `json:"renewal_resets_quota"`
	RenewalKeepsAddons   bool               `json:"renewal_keeps_addons"`
	MaxDevices           *int               `json:"max_devices"`
	MaxConcurrent        *int               `json:"max_concurrent"`
	DeviceReleaseHours   int                `json:"device_release_hours"`
	OveragePolicy        string             `json:"overage_policy"`
	ThrottleKbps         *int               `json:"throttle_kbps"`
	Notes                *string            `json:"notes"`
	Entitlements         []EntitlementInput `json:"entitlements"`
	Quotas               []QuotaInput       `json:"quotas"`
	PoolIDs              []string           `json:"pool_ids"`
}

type CreatePriceInput struct {
	ActorID         string     `json:"-"`
	Currency        string     `json:"currency"`
	UnitAmount      int64      `json:"unit_amount"`
	BillingInterval string     `json:"billing_interval"`
	IntervalCount   int16      `json:"interval_count"`
	TrialDays       int16      `json:"trial_days"`
	UserGroupID     *string    `json:"user_group_id"`
	ValidFrom       *time.Time `json:"valid_from"`
	ValidUntil      *time.Time `json:"valid_until"`
}

type VersionRow struct {
	ID                   string             `json:"id"`
	Version              int                `json:"version"`
	Status               string             `json:"status"`
	FrozenAt             *time.Time         `json:"frozen_at"`
	RowVersion           int64              `json:"row_version"`
	QuotaResetStrategy   string             `json:"quota_reset_strategy"`
	QuotaResetDay        *int16             `json:"quota_reset_day"`
	GracePeriodHours     int                `json:"grace_period_hours"`
	GraceKeepsService    bool               `json:"grace_keeps_service"`
	RenewalExtendsPeriod bool               `json:"renewal_extends_period"`
	RenewalResetsQuota   bool               `json:"renewal_resets_quota"`
	RenewalKeepsAddons   bool               `json:"renewal_keeps_addons"`
	MaxDevices           *int               `json:"max_devices"`
	MaxConcurrent        *int               `json:"max_concurrent"`
	DeviceReleaseHours   int                `json:"device_release_hours"`
	OveragePolicy        string             `json:"overage_policy"`
	ThrottleKbps         *int               `json:"throttle_kbps"`
	Notes                *string            `json:"notes"`
	Entitlements         []EntitlementInput `json:"entitlements"`
	Quotas               []QuotaInput       `json:"quotas"`
	PoolIDs              []string           `json:"pool_ids"`
	CreatedAt            time.Time          `json:"created_at"`
}

type CatalogPlanDetail struct {
	ID                   string       `json:"id"`
	ProductID            string       `json:"product_id"`
	CurrentVersionID     *string      `json:"current_version_id"`
	RowVersion           int64        `json:"row_version"`
	Code                 string       `json:"code"`
	Name                 string       `json:"name"`
	Description          *string      `json:"description"`
	Status               string       `json:"status"`
	Visibility           string       `json:"visibility"`
	VisibleGroupIDs      []string     `json:"visible_group_ids"`
	VisibleFrom          *time.Time   `json:"visible_from"`
	VisibleUntil         *time.Time   `json:"visible_until"`
	AllowNewPurchase     bool         `json:"allow_new_purchase"`
	AllowRenewal         bool         `json:"allow_renewal"`
	AllowUpgrade         bool         `json:"allow_upgrade"`
	PurchaseLimitPerUser *int         `json:"purchase_limit_per_user"`
	StockTotal           *int         `json:"stock_total"`
	StockReserved        int          `json:"stock_reserved"`
	SortOrder            int          `json:"sort_order"`
	Versions             []VersionRow `json:"versions"`
	Prices               []PriceRow   `json:"prices"`
}

func defaultTrue(v *bool) bool { return v == nil || *v }

func uuidArray(ids []string) []uuid.UUID {
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		out = append(out, uuid.MustParse(id))
	}
	return out
}

func validCatalogIDs(ids ...string) bool {
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return false
		}
	}
	return true
}

func validatePlanFields(code, name, visibility string, groupIDs []string, from, until *time.Time, purchaseLimit, stock *int) error {
	fields := map[string]string{}
	if !catalogCodePattern.MatchString(strings.TrimSpace(code)) {
		fields["code"] = "需为 2-64 位小写字母、数字、下划线或连字符"
	}
	if n := len([]rune(strings.TrimSpace(name))); n == 0 || n > 120 {
		fields["name"] = "必填且最多 120 个字符"
	}
	switch visibility {
	case "public", "authenticated", "group", "invite_only", "hidden":
	default:
		fields["visibility"] = "不支持的可见性"
	}
	seen := map[string]bool{}
	for _, id := range groupIDs {
		if _, err := uuid.Parse(id); err != nil || seen[id] {
			fields["visible_group_ids"] = "必须是无重复的 UUID 列表"
			break
		}
		seen[id] = true
	}
	if visibility == "group" && len(groupIDs) == 0 {
		fields["visible_group_ids"] = "分组可见套餐至少需要一个用户组"
	}
	if visibility != "group" && len(groupIDs) != 0 {
		fields["visible_group_ids"] = "只有分组可见套餐可以设置用户组"
	}
	if from != nil && until != nil && !until.After(*from) {
		fields["visible_until"] = "必须晚于 visible_from"
	}
	if purchaseLimit != nil && *purchaseLimit <= 0 {
		fields["purchase_limit_per_user"] = "必须为正整数"
	}
	if stock != nil && *stock < 0 {
		fields["stock_total"] = "不能为负数"
	}
	if len(fields) > 0 {
		return httpx.Invalid(fields)
	}
	return nil
}

func validateVersionSemantics(in VersionSemanticsInput) error {
	fields := map[string]string{}
	switch in.QuotaResetStrategy {
	case "never", "natural_month", "billing_cycle", "fixed_day":
	default:
		fields["quota_reset_strategy"] = "不支持的重置策略"
	}
	if in.QuotaResetStrategy == "fixed_day" {
		if in.QuotaResetDay == nil || *in.QuotaResetDay < 1 || *in.QuotaResetDay > 28 {
			fields["quota_reset_day"] = "固定日必须为 1-28"
		}
	} else if in.QuotaResetDay != nil {
		fields["quota_reset_day"] = "非固定日策略不能设置该字段"
	}
	if in.GracePeriodHours < 0 {
		fields["grace_period_hours"] = "不能为负数"
	}
	if in.MaxDevices != nil && *in.MaxDevices <= 0 {
		fields["max_devices"] = "必须为正整数"
	}
	if in.MaxConcurrent != nil && *in.MaxConcurrent <= 0 {
		fields["max_concurrent"] = "必须为正整数"
	}
	if in.DeviceReleaseHours < 0 {
		fields["device_release_hours"] = "不能为负数"
	}
	switch in.OveragePolicy {
	case "suspend", "throttle", "metered_billing":
	default:
		fields["overage_policy"] = "不支持的超额策略"
	}
	if in.OveragePolicy == "throttle" {
		if in.ThrottleKbps == nil || *in.ThrottleKbps <= 0 {
			fields["throttle_kbps"] = "限速策略必须设置正整数速率"
		}
	} else if in.ThrottleKbps != nil {
		fields["throttle_kbps"] = "非限速策略不能设置速率"
	}
	entSeen, quotaSeen := map[string]bool{}, map[string]bool{}
	for i, e := range in.Entitlements {
		code := strings.TrimSpace(e.Code)
		if code == "" || entSeen[code] || (len(e.Value) > 0 && !json.Valid(e.Value)) {
			fields[fmt.Sprintf("entitlements.%d", i)] = "能力码必须唯一且 value 必须为有效 JSON"
		}
		entSeen[code] = true
	}
	for i, q := range in.Quotas {
		key := strings.TrimSpace(q.Metric) + "\x00" + q.Period
		if strings.TrimSpace(q.Metric) == "" || strings.TrimSpace(q.Unit) == "" || quotaSeen[key] {
			fields[fmt.Sprintf("quotas.%d", i)] = "指标、单位必填且指标/周期必须唯一"
		}
		quotaSeen[key] = true
		switch q.Period {
		case "total", "cycle", "day", "month":
		default:
			fields[fmt.Sprintf("quotas.%d.period", i)] = "不支持的周期"
		}
		if q.Limit != nil && *q.Limit < 0 {
			fields[fmt.Sprintf("quotas.%d.limit", i)] = "不能为负数"
		}
	}
	if len(fields) > 0 {
		return httpx.Invalid(fields)
	}
	return nil
}

func validateVersionUpdatePoolContract(poolIDs []string) error {
	// nil means the field was omitted and the existing bindings must survive the
	// semantic update. Any present value, including [], belongs to the separately
	// authorized, recently reauthenticated and idempotent pool endpoint.
	if poolIDs == nil {
		return nil
	}
	return httpx.Invalid(map[string]string{
		"pool_ids": "请使用 POST /v1/plans/{id}/pools 专用端点更新节点池绑定",
	})
}

func validatePrice(in CreatePriceInput) error {
	fields := map[string]string{}
	if in.Currency != "CNY" && in.Currency != "USD" {
		fields["currency"] = "仅允许 CNY 或 USD"
	}
	if in.UnitAmount < 0 {
		fields["unit_amount"] = "不能为负数"
	}
	switch in.BillingInterval {
	case "day", "week", "month", "quarter", "year", "one_time":
	default:
		fields["billing_interval"] = "不支持的计费周期"
	}
	if in.IntervalCount <= 0 {
		fields["interval_count"] = "必须为正整数"
	}
	if in.TrialDays < 0 {
		fields["trial_days"] = "不能为负数"
	}
	if in.UserGroupID != nil {
		if _, err := uuid.Parse(*in.UserGroupID); err != nil {
			fields["user_group_id"] = "必须是 UUID"
		}
	}
	if in.ValidFrom != nil && in.ValidUntil != nil && !in.ValidUntil.After(*in.ValidFrom) {
		fields["valid_until"] = "必须晚于 valid_from"
	}
	if len(fields) > 0 {
		return httpx.Invalid(fields)
	}
	return nil
}

func validatePublishPrerequisites(visibility string, priceOK bool, poolCount int, nodeOK bool) error {
	if visibility == "invite_only" {
		return httpx.Invalid(map[string]string{"visibility": "邀请购买授权尚未实现，禁止发布"})
	}
	if !priceOK {
		return httpx.Invalid(map[string]string{"prices": "至少需要一个当前有效且适用的 CNY 或 USD 活动价格"})
	}
	if poolCount == 0 {
		return httpx.Invalid(map[string]string{"pool_ids": "发布前至少绑定一个可用节点池"})
	}
	if !nodeOK {
		return httpx.Invalid(map[string]string{"pool_ids": "绑定池中至少需要一个可服务节点"})
	}
	return nil
}

func validatePublishTokens(planCurrent, planExpected, versionCurrent, versionExpected int64) error {
	if planExpected <= 0 || versionExpected <= 0 {
		return httpx.Invalid(map[string]string{"expected_plan_row_version": "必须为正整数", "expected_version_row_version": "必须为正整数"})
	}
	if planCurrent != planExpected {
		return rowConflict("套餐", planCurrent)
	}
	if versionCurrent != versionExpected {
		return rowConflict("套餐版本", versionCurrent)
	}
	return nil
}

func publishPriceCoverage(visibility string, visibleGroups []string, hasPublic bool, covered map[string]bool) bool {
	if hasPublic {
		return true
	}
	if visibility != "group" {
		return false
	}
	for _, id := range visibleGroups {
		if !covered[id] {
			return false
		}
	}
	return len(visibleGroups) > 0
}

func ensureGroups(ctx context.Context, tx pgx.Tx, tenantID string, ids []string) error {
	for _, id := range ids {
		var lockedID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM user_groups WHERE tenant_id=$1 AND id=$2::uuid FOR KEY SHARE`, tenantID, id).Scan(&lockedID); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			return httpx.Invalid(map[string]string{"visible_group_ids": "包含不存在的用户组"})
		}
	}
	return nil
}

func catalogResult(err error) error {
	if err == nil {
		return nil
	}
	var he *httpx.Error
	if errors.As(err, &he) {
		return he
	}
	if db.IsUniqueViolation(err) {
		return httpx.New(httpx.CodeConflict, "目录对象已存在或活动报价发生冲突")
	}
	if db.IsForeignKeyViolation(err) {
		return httpx.Invalid(map[string]string{"reference": "关联对象不存在或已被删除"})
	}
	if db.IsCheckViolation(err) {
		return httpx.New(httpx.CodeValidationFailed, "目录语义校验未通过")
	}
	if db.IsInsufficientPrivilege(err) {
		return httpx.New(httpx.CodeConflict, "已发布目录快照不可修改")
	}
	return httpx.Internal(err)
}

func rowConflict(kind string, current int64) error {
	return &httpx.Error{Code: httpx.CodeConflict, Message: kind + "已被其他管理员修改，请刷新后重试", Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", current)}}
}

func (s *Service) GetPlan(ctx context.Context, tenantID, planID string) (*CatalogPlanDetail, error) {
	if _, err := uuid.Parse(planID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	out := &CatalogPlanDetail{Versions: []VersionRow{}, Prices: []PriceRow{}}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT id,product_id,current_version_id,row_version,code,name,description,status,visibility,
		 visible_group_ids::text[],visible_from,visible_until,allow_new_purchase,allow_renewal,allow_upgrade,
		 purchase_limit_per_user,stock_total,stock_reserved,sort_order FROM plans WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, planID).Scan(
			&out.ID, &out.ProductID, &out.CurrentVersionID, &out.RowVersion, &out.Code, &out.Name, &out.Description, &out.Status, &out.Visibility, &out.VisibleGroupIDs, &out.VisibleFrom, &out.VisibleUntil, &out.AllowNewPurchase, &out.AllowRenewal, &out.AllowUpgrade, &out.PurchaseLimitPerUser, &out.StockTotal, &out.StockReserved, &out.SortOrder); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id,version,status,frozen_at,row_version,quota_reset_strategy,
			quota_reset_day,grace_period_hours,grace_keeps_service,renewal_extends_period,
			renewal_resets_quota,renewal_keeps_addons,max_devices,max_concurrent,
			device_release_hours,overage_policy,throttle_kbps,notes,created_at
			FROM plan_versions WHERE tenant_id=$1 AND plan_id=$2::uuid ORDER BY version DESC`, tenantID, planID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v VersionRow
			if err := rows.Scan(&v.ID, &v.Version, &v.Status, &v.FrozenAt, &v.RowVersion,
				&v.QuotaResetStrategy, &v.QuotaResetDay, &v.GracePeriodHours, &v.GraceKeepsService,
				&v.RenewalExtendsPeriod, &v.RenewalResetsQuota, &v.RenewalKeepsAddons,
				&v.MaxDevices, &v.MaxConcurrent, &v.DeviceReleaseHours, &v.OveragePolicy,
				&v.ThrottleKbps, &v.Notes, &v.CreatedAt); err != nil {
				rows.Close()
				return err
			}
			out.Versions = append(out.Versions, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i := range out.Versions {
			out.Versions[i].Entitlements = []EntitlementInput{}
			out.Versions[i].Quotas = []QuotaInput{}
			out.Versions[i].PoolIDs = []string{}
			erows, err := tx.Query(ctx, `SELECT code,value FROM entitlements WHERE tenant_id=$1 AND plan_version_id=$2::uuid ORDER BY code`, tenantID, out.Versions[i].ID)
			if err != nil {
				return err
			}
			for erows.Next() {
				var e EntitlementInput
				if err := erows.Scan(&e.Code, &e.Value); err != nil {
					erows.Close()
					return err
				}
				out.Versions[i].Entitlements = append(out.Versions[i].Entitlements, e)
			}
			erows.Close()
			if err := erows.Err(); err != nil {
				return err
			}
			qrows, err := tx.Query(ctx, `SELECT metric,limit_value,unit,period FROM quota_definitions WHERE tenant_id=$1 AND plan_version_id=$2::uuid ORDER BY metric,period`, tenantID, out.Versions[i].ID)
			if err != nil {
				return err
			}
			for qrows.Next() {
				var q QuotaInput
				if err := qrows.Scan(&q.Metric, &q.Limit, &q.Unit, &q.Period); err != nil {
					qrows.Close()
					return err
				}
				out.Versions[i].Quotas = append(out.Versions[i].Quotas, q)
			}
			qrows.Close()
			if err := qrows.Err(); err != nil {
				return err
			}
			prows, err := tx.Query(ctx, `SELECT pool_id::text FROM plan_node_pools WHERE tenant_id=$1 AND plan_version_id=$2::uuid ORDER BY pool_id`, tenantID, out.Versions[i].ID)
			if err != nil {
				return err
			}
			for prows.Next() {
				var id string
				if err := prows.Scan(&id); err != nil {
					prows.Close()
					return err
				}
				out.Versions[i].PoolIDs = append(out.Versions[i].PoolIDs, id)
			}
			prows.Close()
			if err := prows.Err(); err != nil {
				return err
			}
		}
		prows, err := tx.Query(ctx, `SELECT id,currency,unit_amount,billing_interval,interval_count,trial_days,status,user_group_id,valid_from,valid_until,row_version FROM prices WHERE tenant_id=$1 AND product_id=$2::uuid ORDER BY created_at DESC`, tenantID, out.ProductID)
		if err != nil {
			return err
		}
		defer prows.Close()
		for prows.Next() {
			var p PriceRow
			if err := prows.Scan(&p.ID, &p.Currency, &p.UnitAmount, &p.Interval, &p.Count, &p.TrialDays, &p.Status, &p.UserGroupID, &p.ValidFrom, &p.ValidUntil, &p.RowVersion); err != nil {
				return err
			}
			out.Prices = append(out.Prices, p)
		}
		return prows.Err()
	})
	if err != nil {
		return nil, catalogResult(err)
	}
	return out, nil
}

func (s *Service) CreatePlan(ctx context.Context, tenantID string, in CreatePlanInput) (*CatalogPlanDetail, error) {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	if in.Visibility == "" {
		in.Visibility = "public"
	}
	if err := validatePlanFields(in.Code, in.Name, in.Visibility, in.VisibleGroupIDs, in.VisibleFrom, in.VisibleUntil, in.PurchaseLimitPerUser, in.StockTotal); err != nil {
		return nil, err
	}
	groupUUIDs := uuidArray(in.VisibleGroupIDs)
	var planID string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		if err := ensureGroups(ctx, tx, tenantID, in.VisibleGroupIDs); err != nil {
			return err
		}
		var productID string
		if err := tx.QueryRow(ctx, `INSERT INTO products(tenant_id,code,name,description,kind,status) VALUES($1,$2,$3,$4,'subscription','draft') RETURNING id`, tenantID, in.Code, in.Name, in.Description).Scan(&productID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO plans(tenant_id,product_id,code,name,description,visibility,visible_group_ids,visible_from,visible_until,allow_new_purchase,allow_renewal,allow_upgrade,purchase_limit_per_user,stock_total,sort_order,status) VALUES($1,$2,$3,$4,$5,$6,$7::uuid[],$8,$9,$10,$11,$12,$13,$14,$15,'draft') RETURNING id`, tenantID, productID, in.Code, in.Name, in.Description, in.Visibility, groupUUIDs, in.VisibleFrom, in.VisibleUntil, defaultTrue(in.AllowNewPurchase), defaultTrue(in.AllowRenewal), defaultTrue(in.AllowUpgrade), in.PurchaseLimitPerUser, in.StockTotal, in.SortOrder).Scan(&planID); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &in.ActorID, Action: "plan.create", ResourceType: "plan", ResourceID: &planID, APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx), AfterDigest: map[string]any{"code": in.Code, "visibility": in.Visibility}})
	})
	if err != nil {
		return nil, catalogResult(err)
	}
	return s.GetPlan(ctx, tenantID, planID)
}

func (s *Service) UpdatePlan(ctx context.Context, tenantID, planID string, in UpdatePlanInput) (int64, error) {
	if !validCatalogIDs(planID) {
		return 0, httpx.NotFoundOrForbidden()
	}
	if in.ExpectedRowVersion <= 0 {
		return 0, httpx.Invalid(map[string]string{"expected_row_version": "必须为正整数"})
	}
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	if err := validatePlanFields(in.Code, in.Name, in.Visibility, in.VisibleGroupIDs, in.VisibleFrom, in.VisibleUntil, in.PurchaseLimitPerUser, in.StockTotal); err != nil {
		return 0, err
	}
	groupUUIDs := uuidArray(in.VisibleGroupIDs)
	var next int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		var current int64
		var status, productID string
		var beforeNewPurchase, beforeRenewal, beforeUpgrade bool
		var reserved int
		if err := tx.QueryRow(ctx, `SELECT row_version,status,product_id,stock_reserved,allow_new_purchase,allow_renewal,allow_upgrade FROM plans WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, planID).Scan(&current, &status, &productID, &reserved, &beforeNewPurchase, &beforeRenewal, &beforeUpgrade); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if status == "archived" {
			return httpx.New(httpx.CodeConflict, "已归档套餐不能恢复或编辑")
		}
		if requiresP0BSalesResume(status, beforeNewPurchase, beforeRenewal, beforeUpgrade, in) {
			if err := s.requireP0BSales(); err != nil {
				return err
			}
		}
		if current != in.ExpectedRowVersion {
			return rowConflict("套餐", current)
		}
		if in.StockTotal != nil && *in.StockTotal < reserved {
			return httpx.Invalid(map[string]string{"stock_total": "不能低于已预留库存"})
		}
		if err := ensureGroups(ctx, tx, tenantID, in.VisibleGroupIDs); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE plans SET code=$3,name=$4,description=$5,visibility=$6,visible_group_ids=$7::uuid[],visible_from=$8,visible_until=$9,allow_new_purchase=$10,allow_renewal=$11,allow_upgrade=$12,purchase_limit_per_user=$13,stock_total=$14,sort_order=$15,row_version=row_version+1 WHERE tenant_id=$1 AND id=$2::uuid AND row_version=$16`, tenantID, planID, in.Code, in.Name, in.Description, in.Visibility, groupUUIDs, in.VisibleFrom, in.VisibleUntil, in.AllowNewPurchase, in.AllowRenewal, in.AllowUpgrade, in.PurchaseLimitPerUser, in.StockTotal, in.SortOrder, in.ExpectedRowVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return rowConflict("套餐", current)
		}
		if _, err := tx.Exec(ctx, `UPDATE products SET code=$3,name=$4,description=$5,updated_at=now() WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, productID, in.Code, in.Name, in.Description); err != nil {
			return err
		}
		next = current + 1
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &in.ActorID, Action: "plan.update", ResourceType: "plan", ResourceID: &planID, APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx), BeforeDigest: map[string]any{"row_version": current}, AfterDigest: map[string]any{"row_version": next, "code": in.Code}})
	})
	return next, catalogResult(err)
}

func (s *Service) CreatePlanVersion(ctx context.Context, tenantID, planID, actorID string) (*VersionRow, error) {
	if !validCatalogIDs(planID) {
		return nil, httpx.NotFoundOrForbidden()
	}
	var out VersionRow
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM plans WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, planID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if status == "archived" {
			return httpx.New(httpx.CodeConflict, "已归档套餐不能创建新版本")
		}
		if err := tx.QueryRow(ctx, `INSERT INTO plan_versions(tenant_id,plan_id,version,created_by) SELECT $1,$2::uuid,coalesce(max(version),0)+1,$3::uuid FROM plan_versions WHERE tenant_id=$1 AND plan_id=$2::uuid RETURNING id,version,status,frozen_at,row_version,created_at`, tenantID, planID, actorID).Scan(&out.ID, &out.Version, &out.Status, &out.FrozenAt, &out.RowVersion, &out.CreatedAt); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &actorID, Action: "plan_version.create", ResourceType: "plan_version", ResourceID: &out.ID, APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx), AfterDigest: map[string]any{"plan_id": planID, "version": out.Version}})
	})
	if err != nil {
		return nil, catalogResult(err)
	}
	return &out, nil
}

func (s *Service) UpdatePlanVersion(ctx context.Context, tenantID, planID, versionID string, in VersionSemanticsInput) (int64, error) {
	if !validCatalogIDs(planID, versionID) {
		return 0, httpx.NotFoundOrForbidden()
	}
	if err := validateVersionUpdatePoolContract(in.PoolIDs); err != nil {
		return 0, err
	}
	if in.ExpectedRowVersion <= 0 {
		return 0, httpx.Invalid(map[string]string{"expected_row_version": "必须为正整数"})
	}
	if err := validateVersionSemantics(in); err != nil {
		return 0, err
	}
	var next int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		var current int64
		var status string
		var frozen *time.Time
		if err := tx.QueryRow(ctx, `SELECT row_version,status,frozen_at FROM plan_versions WHERE tenant_id=$1 AND plan_id=$2::uuid AND id=$3::uuid FOR UPDATE`, tenantID, planID, versionID).Scan(&current, &status, &frozen); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if current != in.ExpectedRowVersion {
			return rowConflict("套餐版本", current)
		}
		if status != "draft" || frozen != nil {
			return httpx.New(httpx.CodeConflict, "只有未发布草稿版本可以编辑")
		}
		tag, err := tx.Exec(ctx, `UPDATE plan_versions SET quota_reset_strategy=$4,quota_reset_day=$5,grace_period_hours=$6,grace_keeps_service=$7,renewal_extends_period=$8,renewal_resets_quota=$9,renewal_keeps_addons=$10,max_devices=$11,max_concurrent=$12,device_release_hours=$13,overage_policy=$14,throttle_kbps=$15,notes=$16,row_version=row_version+1 WHERE tenant_id=$1 AND plan_id=$2::uuid AND id=$3::uuid AND row_version=$17`, tenantID, planID, versionID, in.QuotaResetStrategy, in.QuotaResetDay, in.GracePeriodHours, in.GraceKeepsService, in.RenewalExtendsPeriod, in.RenewalResetsQuota, in.RenewalKeepsAddons, in.MaxDevices, in.MaxConcurrent, in.DeviceReleaseHours, in.OveragePolicy, in.ThrottleKbps, in.Notes, in.ExpectedRowVersion)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return rowConflict("套餐版本", current)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM entitlements WHERE tenant_id=$1 AND plan_version_id=$2::uuid`, tenantID, versionID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM quota_definitions WHERE tenant_id=$1 AND plan_version_id=$2::uuid`, tenantID, versionID); err != nil {
			return err
		}
		for _, e := range in.Entitlements {
			value := e.Value
			if len(value) == 0 {
				value = json.RawMessage(`true`)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO entitlements(tenant_id,plan_version_id,code,value) VALUES($1,$2::uuid,$3,$4)`, tenantID, versionID, strings.TrimSpace(e.Code), value); err != nil {
				return err
			}
		}
		for _, q := range in.Quotas {
			if _, err := tx.Exec(ctx, `INSERT INTO quota_definitions(tenant_id,plan_version_id,metric,limit_value,unit,period) VALUES($1,$2::uuid,$3,$4,$5,$6)`, tenantID, versionID, strings.TrimSpace(q.Metric), q.Limit, strings.TrimSpace(q.Unit), q.Period); err != nil {
				return err
			}
		}
		next = current + 1
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &in.ActorID, Action: "plan_version.update", ResourceType: "plan_version", ResourceID: &versionID, APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx), BeforeDigest: map[string]any{"row_version": current}, AfterDigest: map[string]any{"row_version": next, "entitlements": len(in.Entitlements), "quotas": len(in.Quotas)}})
	})
	return next, catalogResult(err)
}

func (s *Service) PublishPlanVersion(ctx context.Context, tenantID, planID, versionID, actorID string, expectedPlan, expectedVersion int64) (int64, int64, error) {
	if err := s.requireP0BSales(); err != nil {
		return 0, 0, err
	}
	if !validCatalogIDs(planID, versionID) {
		return 0, 0, httpx.NotFoundOrForbidden()
	}
	if expectedPlan <= 0 || expectedVersion <= 0 {
		return 0, 0, validatePublishTokens(0, expectedPlan, 0, expectedVersion)
	}
	var planNext, versionNext int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var productID, status, visibility string
		var planCurrent int64
		var visibleGroupIDs []string
		if err := tx.QueryRow(ctx, `SELECT product_id,status,visibility,visible_group_ids::text[],row_version FROM plans WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, planID).Scan(&productID, &status, &visibility, &visibleGroupIDs, &planCurrent); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if status == "archived" {
			return httpx.New(httpx.CodeConflict, "已归档套餐不能重新发布")
		}
		if visibility == "group" {
			if err := ensureGroups(ctx, tx, tenantID, visibleGroupIDs); err != nil {
				return err
			}
		}
		var current int64
		var vstatus string
		var frozen *time.Time
		if err := tx.QueryRow(ctx, `SELECT row_version,status,frozen_at FROM plan_versions WHERE tenant_id=$1 AND plan_id=$2::uuid AND id=$3::uuid FOR UPDATE`, tenantID, planID, versionID).Scan(&current, &vstatus, &frozen); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if err := validatePublishTokens(planCurrent, expectedPlan, current, expectedVersion); err != nil {
			return err
		}
		if vstatus != "draft" || frozen != nil {
			return httpx.New(httpx.CodeConflict, "只有未发布草稿版本可以发布")
		}
		qualifyingPriceIDs := []string{}
		coveredGroups := map[string]bool{}
		hasPublicPrice := false
		priceRows, err := tx.Query(ctx, `SELECT id::text,user_group_id::text FROM prices WHERE tenant_id=$1 AND product_id=$2::uuid AND status='active' AND currency IN ('CNY','USD') AND (valid_from IS NULL OR valid_from<=now()) AND (valid_until IS NULL OR valid_until>now()) AND (user_group_id IS NULL OR ($3='group' AND user_group_id=ANY($4::uuid[]))) ORDER BY id FOR SHARE`, tenantID, productID, visibility, uuidArray(visibleGroupIDs))
		if err != nil {
			return err
		}
		for priceRows.Next() {
			var id string
			var groupID *string
			if err := priceRows.Scan(&id, &groupID); err != nil {
				priceRows.Close()
				return err
			}
			qualifyingPriceIDs = append(qualifyingPriceIDs, id)
			if groupID == nil {
				hasPublicPrice = true
			} else {
				coveredGroups[*groupID] = true
			}
		}
		priceRows.Close()
		if err := priceRows.Err(); err != nil {
			return err
		}
		priceOK := publishPriceCoverage(visibility, visibleGroupIDs, hasPublicPrice, coveredGroups)
		pools := 1
		var qualifyingPoolID string
		if err := tx.QueryRow(ctx, `SELECT np.id::text FROM plan_node_pools pnp JOIN node_pools np ON np.tenant_id=pnp.tenant_id AND np.id=pnp.pool_id WHERE pnp.tenant_id=$1 AND pnp.plan_version_id=$2::uuid AND np.status<>'disabled' ORDER BY np.id LIMIT 1 FOR SHARE OF np`, tenantID, versionID).Scan(&qualifyingPoolID); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			pools = 0
		}
		nodeOK := true
		var qualifyingNodeID string
		readySQL := nodefabric.StableProtocolReadySQL("n")
		if err := tx.QueryRow(ctx, `SELECT n.id::text FROM plan_node_pools pnp JOIN nodes n ON n.tenant_id=pnp.tenant_id AND n.pool_id=pnp.pool_id JOIN servers s ON s.tenant_id=n.tenant_id AND s.id=n.server_id WHERE pnp.tenant_id=$1 AND pnp.plan_version_id=$2::uuid AND n.serving_status='active' AND s.status='ready' AND s.deleted_at IS NULL AND n.server_port BETWEEN 1 AND 65535 AND `+readySQL+` ORDER BY n.id LIMIT 1 FOR SHARE OF n,s`, tenantID, versionID).Scan(&qualifyingNodeID); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			nodeOK = false
		}
		if err := validatePublishPrerequisites(visibility, priceOK, pools, nodeOK); err != nil {
			return err
		}
		vtag, err := tx.Exec(ctx, `UPDATE plan_versions SET status='published',frozen_at=now(),row_version=row_version+1 WHERE tenant_id=$1 AND id=$2::uuid AND row_version=$3`, tenantID, versionID, expectedVersion)
		if err != nil {
			return err
		}
		if vtag.RowsAffected() != 1 {
			return rowConflict("套餐版本", current)
		}
		ptag, err := tx.Exec(ctx, `UPDATE plans SET current_version_id=$3::uuid,status='active',row_version=row_version+1 WHERE tenant_id=$1 AND id=$2::uuid AND row_version=$4`, tenantID, planID, versionID, expectedPlan)
		if err != nil {
			return err
		}
		if ptag.RowsAffected() != 1 {
			return rowConflict("套餐", planCurrent)
		}
		if _, err := tx.Exec(ctx, `UPDATE products SET status='active',updated_at=now() WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, productID); err != nil {
			return err
		}
		planNext, versionNext = planCurrent+1, current+1
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &actorID, Action: "plan_version.publish", ResourceType: "plan_version", ResourceID: &versionID, APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx), BeforeDigest: map[string]any{"plan_row_version": planCurrent, "version_row_version": current}, AfterDigest: map[string]any{"plan_id": planID, "plan_row_version": planNext, "version_row_version": versionNext, "price_ids": qualifyingPriceIDs, "pool_id": qualifyingPoolID, "node_id": qualifyingNodeID}})
	})
	return planNext, versionNext, catalogResult(err)
}

func (s *Service) CreatePlanPrice(ctx context.Context, tenantID, planID string, in CreatePriceInput) (*PriceRow, error) {
	if err := s.requireP0BSales(); err != nil {
		return nil, err
	}
	if !validCatalogIDs(planID) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err := validatePrice(in); err != nil {
		return nil, err
	}
	var out PriceRow
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		var productID, status string
		if err := tx.QueryRow(ctx, `SELECT product_id,status FROM plans WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, planID).Scan(&productID, &status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if status == "archived" {
			return httpx.New(httpx.CodeConflict, "已归档套餐不能新增价格")
		}
		if in.UserGroupID != nil {
			var lockedID string
			if err := tx.QueryRow(ctx, `SELECT id::text FROM user_groups WHERE tenant_id=$1 AND id=$2::uuid FOR KEY SHARE`, tenantID, *in.UserGroupID).Scan(&lockedID); err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				return httpx.Invalid(map[string]string{"user_group_id": "用户组不存在"})
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO prices(tenant_id,product_id,currency,unit_amount,billing_interval,interval_count,trial_days,status,user_group_id,valid_from,valid_until) VALUES($1,$2::uuid,$3,$4,$5,$6,$7,'active',$8::uuid,$9,$10) RETURNING id,currency,unit_amount,billing_interval,interval_count,trial_days,status,user_group_id,valid_from,valid_until,row_version`, tenantID, productID, in.Currency, in.UnitAmount, in.BillingInterval, in.IntervalCount, in.TrialDays, in.UserGroupID, in.ValidFrom, in.ValidUntil).Scan(&out.ID, &out.Currency, &out.UnitAmount, &out.Interval, &out.Count, &out.TrialDays, &out.Status, &out.UserGroupID, &out.ValidFrom, &out.ValidUntil, &out.RowVersion); err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &in.ActorID, Action: "price.create", ResourceType: "price", ResourceID: &out.ID, APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx), AfterDigest: map[string]any{"plan_id": planID, "currency": in.Currency, "unit_amount": in.UnitAmount, "user_group_id": in.UserGroupID}})
	})
	if err != nil {
		return nil, catalogResult(err)
	}
	return &out, nil
}

func requiresP0BSalesResume(status string, beforeNewPurchase, beforeRenewal, beforeUpgrade bool, in UpdatePlanInput) bool {
	return status == "active" &&
		((!beforeNewPurchase && in.AllowNewPurchase) ||
			(!beforeRenewal && in.AllowRenewal) ||
			(!beforeUpgrade && in.AllowUpgrade))
}

func (s *Service) ArchivePlanPrice(ctx context.Context, tenantID, planID, priceID, actorID string, expected int64) (int64, error) {
	if !validCatalogIDs(planID, priceID) {
		return 0, httpx.NotFoundOrForbidden()
	}
	if expected <= 0 {
		return 0, httpx.Invalid(map[string]string{"expected_row_version": "必须为正整数"})
	}
	var next int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var current int64
		var status string
		if err := tx.QueryRow(ctx, `SELECT pr.row_version,pr.status FROM prices pr JOIN plans pl ON pl.tenant_id=pr.tenant_id AND pl.product_id=pr.product_id WHERE pr.tenant_id=$1 AND pl.id=$2::uuid AND pr.id=$3::uuid FOR UPDATE OF pr`, tenantID, planID, priceID).Scan(&current, &status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if current != expected {
			return rowConflict("价格", current)
		}
		if status != "active" {
			return httpx.New(httpx.CodeConflict, "价格已经归档")
		}
		if _, err := tx.Exec(ctx, `UPDATE prices SET status='archived',row_version=row_version+1 WHERE tenant_id=$1 AND id=$2::uuid AND row_version=$3`, tenantID, priceID, expected); err != nil {
			return err
		}
		next = current + 1
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &actorID, Action: "price.archive", ResourceType: "price", ResourceID: &priceID, APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx), BeforeDigest: map[string]any{"status": status, "row_version": current}, AfterDigest: map[string]any{"status": "archived", "row_version": next}})
	})
	return next, catalogResult(err)
}

func (s *Service) ArchivePlan(ctx context.Context, tenantID, planID, actorID string, expected int64) (int64, error) {
	if !validCatalogIDs(planID) {
		return 0, httpx.NotFoundOrForbidden()
	}
	if expected <= 0 {
		return 0, httpx.Invalid(map[string]string{"expected_row_version": "必须为正整数"})
	}
	var next int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var current int64
		var status, productID string
		if err := tx.QueryRow(ctx, `SELECT row_version,status,product_id FROM plans WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, planID).Scan(&current, &status, &productID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if current != expected {
			return rowConflict("套餐", current)
		}
		if status == "archived" {
			return httpx.New(httpx.CodeConflict, "套餐已经归档")
		}
		if _, err := tx.Exec(ctx, `UPDATE plans SET status='archived',allow_new_purchase=false,row_version=row_version+1 WHERE tenant_id=$1 AND id=$2::uuid AND row_version=$3`, tenantID, planID, expected); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE products SET status='archived',updated_at=now() WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, productID); err != nil {
			return err
		}
		next = current + 1
		return audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &actorID, Action: "plan.archive", ResourceType: "plan", ResourceID: &planID, APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx), BeforeDigest: map[string]any{"status": status, "row_version": current}, AfterDigest: map[string]any{"status": "archived", "row_version": next}})
	})
	return next, catalogResult(err)
}

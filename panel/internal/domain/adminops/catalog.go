// [INPUT]: 依赖 platform/db 的租户事务、platform/audit、platform/httpx，依赖同包 plan_highlights.go 的卖点校验
// [OUTPUT]: 对外提供套餐资料用例（套餐资料带卖点 highlights 与推荐 recommended，R100）GetPlan/CreatePlan/UpdatePlan/ArchivePlan 与目录全部输入输出类型（VersionRow 带建版本人邮箱）；包内提供 loadPlanTx、prepare*PlanInput、createPlanTx / updatePlanTx 与 catalogResult / rowConflict 等共用助手
// [POS]: adminops 的套餐目录核心：套餐资料与目录共用的类型、校验和助手；版本生命周期在 catalog_version.go，价格在 catalog_price.go。每个用例是「事务外校验 + 事务体」两段，事务体可被 plan_wizard.go / plan_wizard_update.go 在同一事务里编排
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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
	// 卖点与推荐（R100）：新建时可选，缺省为空与 false
	Highlights  []string `json:"highlights"`
	Recommended bool     `json:"recommended"`
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
	// 卖点与推荐（R100）：与其余资料一样整体覆盖，客户端要回填当前值
	Highlights  []string `json:"highlights"`
	Recommended bool     `json:"recommended"`
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
	// CreatedByEmail 是建这个版本的管理员（版本行「草稿 · 某人 · 日期」）；
	// 迁移前建的版本或账号已删除时为空。
	CreatedByEmail *string   `json:"created_by_email"`
	CreatedAt      time.Time `json:"created_at"`
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
	Highlights           []string     `json:"highlights"`
	Recommended          bool         `json:"recommended"`
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
		return loadPlanTx(ctx, tx, tenantID, planID, out)
	})
	if err != nil {
		return nil, catalogResult(err)
	}
	return out, nil
}

// loadPlanTx 在调用方的事务里读套餐详情，向导编辑靠它在同一事务里读—改—发布。
func loadPlanTx(ctx context.Context, tx pgx.Tx, tenantID, planID string, out *CatalogPlanDetail) error {
	if err := tx.QueryRow(ctx, `SELECT id,product_id,current_version_id,row_version,code,name,description,status,visibility,
	 visible_group_ids::text[],visible_from,visible_until,allow_new_purchase,allow_renewal,allow_upgrade,
	 purchase_limit_per_user,stock_total,stock_reserved,sort_order,highlights,recommended FROM plans WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, planID).Scan(
		&out.ID, &out.ProductID, &out.CurrentVersionID, &out.RowVersion, &out.Code, &out.Name, &out.Description, &out.Status, &out.Visibility, &out.VisibleGroupIDs, &out.VisibleFrom, &out.VisibleUntil, &out.AllowNewPurchase, &out.AllowRenewal, &out.AllowUpgrade, &out.PurchaseLimitPerUser, &out.StockTotal, &out.StockReserved, &out.SortOrder, &out.Highlights, &out.Recommended); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id,version,status,frozen_at,row_version,quota_reset_strategy,
		quota_reset_day,grace_period_hours,grace_keeps_service,renewal_extends_period,
		renewal_resets_quota,renewal_keeps_addons,max_devices,max_concurrent,
		device_release_hours,overage_policy,throttle_kbps,notes,
		(SELECT u.email FROM users u WHERE u.tenant_id=plan_versions.tenant_id AND u.id=plan_versions.created_by),created_at
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
			&v.ThrottleKbps, &v.Notes, &v.CreatedByEmail, &v.CreatedAt); err != nil {
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
}

func (s *Service) CreatePlan(ctx context.Context, tenantID string, in CreatePlanInput) (*CatalogPlanDetail, error) {
	if err := prepareCreatePlanInput(&in); err != nil {
		return nil, err
	}
	var planID string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		var err error
		planID, err = createPlanTx(ctx, tx, tenantID, in)
		return err
	})
	if err != nil {
		return nil, catalogResult(err)
	}
	return s.GetPlan(ctx, tenantID, planID)
}

// prepareCreatePlanInput 在进事务之前规整并校验套餐资料，CreatePlan 与向导新建共用。
func prepareCreatePlanInput(in *CreatePlanInput) error {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	if in.Visibility == "" {
		in.Visibility = "public"
	}
	return withHighlightFields(validatePlanFields(in.Code, in.Name, in.Visibility, in.VisibleGroupIDs, in.VisibleFrom, in.VisibleUntil, in.PurchaseLimitPerUser, in.StockTotal), &in.Highlights)
}

// createPlanTx 建产品与草稿套餐壳并写审计，返回套餐 ID；输入须已经过 prepareCreatePlanInput。
func createPlanTx(ctx context.Context, tx pgx.Tx, tenantID string, in CreatePlanInput) (string, error) {
	if err := ensureGroups(ctx, tx, tenantID, in.VisibleGroupIDs); err != nil {
		return "", err
	}
	var productID, planID string
	if err := tx.QueryRow(ctx, `INSERT INTO products(tenant_id,code,name,description,kind,status) VALUES($1,$2,$3,$4,'subscription','draft') RETURNING id`, tenantID, in.Code, in.Name, in.Description).Scan(&productID); err != nil {
		return "", err
	}
	if err := tx.QueryRow(ctx, `INSERT INTO plans(tenant_id,product_id,code,name,description,visibility,visible_group_ids,visible_from,visible_until,allow_new_purchase,allow_renewal,allow_upgrade,purchase_limit_per_user,stock_total,sort_order,highlights,recommended,status) VALUES($1,$2,$3,$4,$5,$6,$7::uuid[],$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,'draft') RETURNING id`, tenantID, productID, in.Code, in.Name, in.Description, in.Visibility, uuidArray(in.VisibleGroupIDs), in.VisibleFrom, in.VisibleUntil, defaultTrue(in.AllowNewPurchase), defaultTrue(in.AllowRenewal), defaultTrue(in.AllowUpgrade), in.PurchaseLimitPerUser, in.StockTotal, in.SortOrder, in.Highlights, in.Recommended).Scan(&planID); err != nil {
		return "", err
	}
	return planID, audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &in.ActorID, Action: "plan.create", ResourceType: "plan", ResourceID: &planID, APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx), AfterDigest: map[string]any{"code": in.Code, "visibility": in.Visibility}})
}

func (s *Service) UpdatePlan(ctx context.Context, tenantID, planID string, in UpdatePlanInput) (int64, error) {
	if err := prepareUpdatePlanInput(planID, &in); err != nil {
		return 0, err
	}
	var next int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		var err error
		next, err = s.updatePlanTx(ctx, tx, tenantID, planID, in)
		return err
	})
	return next, catalogResult(err)
}

// prepareUpdatePlanInput 在进事务之前规整并校验套餐资料：失败时一行都不碰。
func prepareUpdatePlanInput(planID string, in *UpdatePlanInput) error {
	if !validCatalogIDs(planID) {
		return httpx.NotFoundOrForbidden()
	}
	if in.ExpectedRowVersion <= 0 {
		return httpx.Invalid(map[string]string{"expected_row_version": "必须为正整数"})
	}
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	return withHighlightFields(validatePlanFields(in.Code, in.Name, in.Visibility, in.VisibleGroupIDs, in.VisibleFrom, in.VisibleUntil, in.PurchaseLimitPerUser, in.StockTotal), &in.Highlights)
}

func (s *Service) updatePlanTx(ctx context.Context, tx pgx.Tx, tenantID, planID string, in UpdatePlanInput) (int64, error) {
	groupUUIDs := uuidArray(in.VisibleGroupIDs)
	var next int64
	var current int64
	var status, productID string
	var beforeNewPurchase, beforeRenewal, beforeUpgrade bool
	var reserved int
	if err := tx.QueryRow(ctx, `SELECT row_version,status,product_id,stock_reserved,allow_new_purchase,allow_renewal,allow_upgrade FROM plans WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, planID).Scan(&current, &status, &productID, &reserved, &beforeNewPurchase, &beforeRenewal, &beforeUpgrade); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, httpx.NotFoundOrForbidden()
		}
		return 0, err
	}
	if status == "archived" {
		return 0, httpx.New(httpx.CodeConflict, "已归档套餐不能恢复或编辑")
	}
	if requiresP0BSalesResume(status, beforeNewPurchase, beforeRenewal, beforeUpgrade, in) {
		if err := s.requireP0BSales(); err != nil {
			return 0, err
		}
	}
	if current != in.ExpectedRowVersion {
		return 0, rowConflict("套餐", current)
	}
	if in.StockTotal != nil && *in.StockTotal < reserved {
		return 0, httpx.Invalid(map[string]string{"stock_total": "不能低于已预留库存"})
	}
	if err := ensureGroups(ctx, tx, tenantID, in.VisibleGroupIDs); err != nil {
		return 0, err
	}
	tag, err := tx.Exec(ctx, `UPDATE plans SET code=$3,name=$4,description=$5,visibility=$6,visible_group_ids=$7::uuid[],visible_from=$8,visible_until=$9,allow_new_purchase=$10,allow_renewal=$11,allow_upgrade=$12,purchase_limit_per_user=$13,stock_total=$14,sort_order=$15,highlights=$17,recommended=$18,row_version=row_version+1 WHERE tenant_id=$1 AND id=$2::uuid AND row_version=$16`, tenantID, planID, in.Code, in.Name, in.Description, in.Visibility, groupUUIDs, in.VisibleFrom, in.VisibleUntil, in.AllowNewPurchase, in.AllowRenewal, in.AllowUpgrade, in.PurchaseLimitPerUser, in.StockTotal, in.SortOrder, in.ExpectedRowVersion, in.Highlights, in.Recommended)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() != 1 {
		return 0, rowConflict("套餐", current)
	}
	if _, err := tx.Exec(ctx, `UPDATE products SET code=$3,name=$4,description=$5,updated_at=now() WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, productID, in.Code, in.Name, in.Description); err != nil {
		return 0, err
	}
	next = current + 1
	return next, audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &in.ActorID, Action: "plan.update", ResourceType: "plan", ResourceID: &planID, APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx), BeforeDigest: map[string]any{"row_version": current}, AfterDigest: map[string]any{"row_version": next, "code": in.Code}})
}

func requiresP0BSalesResume(status string, beforeNewPurchase, beforeRenewal, beforeUpgrade bool, in UpdatePlanInput) bool {
	return status == "active" &&
		((!beforeNewPurchase && in.AllowNewPurchase) ||
			(!beforeRenewal && in.AllowRenewal) ||
			(!beforeUpgrade && in.AllowUpgrade))
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

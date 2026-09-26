// [INPUT]: 依赖 catalog.go 的输入类型、loadPlanTx 与 catalogResult / rowConflict，依赖 domain/nodefabric 的 StableProtocolReadySQL 判定可服务节点，依赖 platform/db、audit、httpx
// [OUTPUT]: 对外提供 Service 的 CreatePlanVersion、UpdatePlanVersion、PublishPlanVersion；包内提供版本语义与发布前置的校验函数、createPlanVersionTx / updatePlanVersionTx / publishPlanVersionTx 事务体
// [POS]: adminops 套餐目录的版本生命周期：从 catalog.go 拆出。建草稿版本、改版本语义（限速与超额策略解耦、新写入只收 suspend，R99；旧 pool_ids 字段一律拒绝，绑池只走 setPlanPools）、发布（套餐与版本双令牌、价格覆盖可见用户组、有池与可服务节点才放行，过 P0B 销售闸门）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

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
	// R99（D-C-5）：超额策略运行时只有一种效果——流量用完且无流量包余额后停止
	// 下发；throttle / metered_billing 从没实现过，新写入只收 suspend（省略按
	// suspend），存量行不改。限速与策略无关：写多少就全程限多少，null = 不限。
	switch in.OveragePolicy {
	case "", "suspend":
	default:
		fields["overage_policy"] = "只支持 suspend：流量用完后停止服务"
	}
	if in.ThrottleKbps != nil && *in.ThrottleKbps <= 0 {
		fields["throttle_kbps"] = "必须为正整数；不限速请留空"
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

func (s *Service) CreatePlanVersion(ctx context.Context, tenantID, planID, actorID string) (*VersionRow, error) {
	if !validCatalogIDs(planID) {
		return nil, httpx.NotFoundOrForbidden()
	}
	var out VersionRow
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		created, err := s.createPlanVersionTx(ctx, tx, tenantID, planID, actorID)
		if created != nil {
			out = *created
		}
		return err
	})
	if err != nil {
		return nil, catalogResult(err)
	}
	return &out, nil
}

func (s *Service) createPlanVersionTx(ctx context.Context, tx pgx.Tx, tenantID, planID, actorID string) (*VersionRow, error) {
	var out VersionRow
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM plans WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, planID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFoundOrForbidden()
		}
		return nil, err
	}
	if status == "archived" {
		return nil, httpx.New(httpx.CodeConflict, "已归档套餐不能创建新版本")
	}
	if err := tx.QueryRow(ctx, `INSERT INTO plan_versions(tenant_id,plan_id,version,created_by) SELECT $1,$2::uuid,coalesce(max(version),0)+1,$3::uuid FROM plan_versions WHERE tenant_id=$1 AND plan_id=$2::uuid RETURNING id,version,status,frozen_at,row_version,created_at`, tenantID, planID, actorID).Scan(&out.ID, &out.Version, &out.Status, &out.FrozenAt, &out.RowVersion, &out.CreatedAt); err != nil {
		return nil, err
	}
	return &out, audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &actorID, Action: "plan_version.create", ResourceType: "plan_version", ResourceID: &out.ID, APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx), AfterDigest: map[string]any{"plan_id": planID, "version": out.Version}})
}

func (s *Service) UpdatePlanVersion(ctx context.Context, tenantID, planID, versionID string, in VersionSemanticsInput) (int64, error) {
	if err := prepareVersionSemanticsInput(planID, versionID, in); err != nil {
		return 0, err
	}
	var next int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		var err error
		next, err = s.updatePlanVersionTx(ctx, tx, tenantID, planID, versionID, in)
		return err
	})
	return next, catalogResult(err)
}

// prepareVersionSemanticsInput 在进事务之前校验版本语义，UpdatePlanVersion 与向导编辑共用。
func prepareVersionSemanticsInput(planID, versionID string, in VersionSemanticsInput) error {
	if !validCatalogIDs(planID, versionID) {
		return httpx.NotFoundOrForbidden()
	}
	if err := validateVersionUpdatePoolContract(in.PoolIDs); err != nil {
		return err
	}
	if in.ExpectedRowVersion <= 0 {
		return httpx.Invalid(map[string]string{"expected_row_version": "必须为正整数"})
	}
	return validateVersionSemantics(in)
}

func (s *Service) updatePlanVersionTx(ctx context.Context, tx pgx.Tx, tenantID, planID, versionID string, in VersionSemanticsInput) (int64, error) {
	var next int64
	var current int64
	var status string
	var frozen *time.Time
	if err := tx.QueryRow(ctx, `SELECT row_version,status,frozen_at FROM plan_versions WHERE tenant_id=$1 AND plan_id=$2::uuid AND id=$3::uuid FOR UPDATE`, tenantID, planID, versionID).Scan(&current, &status, &frozen); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, httpx.NotFoundOrForbidden()
		}
		return 0, err
	}
	if current != in.ExpectedRowVersion {
		return 0, rowConflict("套餐版本", current)
	}
	if status != "draft" || frozen != nil {
		return 0, httpx.New(httpx.CodeConflict, "只有未发布草稿版本可以编辑")
	}
	if in.OveragePolicy == "" {
		in.OveragePolicy = "suspend"
	}
	tag, err := tx.Exec(ctx, `UPDATE plan_versions SET quota_reset_strategy=$4,quota_reset_day=$5,grace_period_hours=$6,grace_keeps_service=$7,renewal_extends_period=$8,renewal_resets_quota=$9,renewal_keeps_addons=$10,max_devices=$11,max_concurrent=$12,device_release_hours=$13,overage_policy=$14,throttle_kbps=$15,notes=$16,row_version=row_version+1 WHERE tenant_id=$1 AND plan_id=$2::uuid AND id=$3::uuid AND row_version=$17`, tenantID, planID, versionID, in.QuotaResetStrategy, in.QuotaResetDay, in.GracePeriodHours, in.GraceKeepsService, in.RenewalExtendsPeriod, in.RenewalResetsQuota, in.RenewalKeepsAddons, in.MaxDevices, in.MaxConcurrent, in.DeviceReleaseHours, in.OveragePolicy, in.ThrottleKbps, in.Notes, in.ExpectedRowVersion)
	if err != nil {
		return 0, err
	}
	if tag.RowsAffected() != 1 {
		return 0, rowConflict("套餐版本", current)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM entitlements WHERE tenant_id=$1 AND plan_version_id=$2::uuid`, tenantID, versionID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM quota_definitions WHERE tenant_id=$1 AND plan_version_id=$2::uuid`, tenantID, versionID); err != nil {
		return 0, err
	}
	for _, e := range in.Entitlements {
		value := e.Value
		if len(value) == 0 {
			value = json.RawMessage(`true`)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO entitlements(tenant_id,plan_version_id,code,value) VALUES($1,$2::uuid,$3,$4)`, tenantID, versionID, strings.TrimSpace(e.Code), value); err != nil {
			return 0, err
		}
	}
	for _, q := range in.Quotas {
		if _, err := tx.Exec(ctx, `INSERT INTO quota_definitions(tenant_id,plan_version_id,metric,limit_value,unit,period) VALUES($1,$2::uuid,$3,$4,$5,$6)`, tenantID, versionID, strings.TrimSpace(q.Metric), q.Limit, strings.TrimSpace(q.Unit), q.Period); err != nil {
			return 0, err
		}
	}
	next = current + 1
	return next, audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &in.ActorID, Action: "plan_version.update", ResourceType: "plan_version", ResourceID: &versionID, APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx), BeforeDigest: map[string]any{"row_version": current}, AfterDigest: map[string]any{"row_version": next, "entitlements": len(in.Entitlements), "quotas": len(in.Quotas)}})
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
		var err error
		planNext, versionNext, err = s.publishPlanVersionTx(ctx, tx, tenantID, planID, versionID, actorID, expectedPlan, expectedVersion)
		return err
	})
	return planNext, versionNext, catalogResult(err)
}

func (s *Service) publishPlanVersionTx(ctx context.Context, tx pgx.Tx, tenantID, planID, versionID, actorID string, expectedPlan, expectedVersion int64) (int64, int64, error) {
	var planNext, versionNext int64
	var productID, status, visibility string
	var planCurrent int64
	var visibleGroupIDs []string
	if err := tx.QueryRow(ctx, `SELECT product_id,status,visibility,visible_group_ids::text[],row_version FROM plans WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, planID).Scan(&productID, &status, &visibility, &visibleGroupIDs, &planCurrent); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, httpx.NotFoundOrForbidden()
		}
		return 0, 0, err
	}
	if status == "archived" {
		return 0, 0, httpx.New(httpx.CodeConflict, "已归档套餐不能重新发布")
	}
	if visibility == "group" {
		if err := ensureGroups(ctx, tx, tenantID, visibleGroupIDs); err != nil {
			return 0, 0, err
		}
	}
	var current int64
	var vstatus string
	var frozen *time.Time
	if err := tx.QueryRow(ctx, `SELECT row_version,status,frozen_at FROM plan_versions WHERE tenant_id=$1 AND plan_id=$2::uuid AND id=$3::uuid FOR UPDATE`, tenantID, planID, versionID).Scan(&current, &vstatus, &frozen); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, httpx.NotFoundOrForbidden()
		}
		return 0, 0, err
	}
	if err := validatePublishTokens(planCurrent, expectedPlan, current, expectedVersion); err != nil {
		return 0, 0, err
	}
	if vstatus != "draft" || frozen != nil {
		return 0, 0, httpx.New(httpx.CodeConflict, "只有未发布草稿版本可以发布")
	}
	qualifyingPriceIDs := []string{}
	coveredGroups := map[string]bool{}
	hasPublicPrice := false
	priceRows, err := tx.Query(ctx, `SELECT id::text,user_group_id::text FROM prices WHERE tenant_id=$1 AND product_id=$2::uuid AND status='active' AND currency IN ('CNY','USD') AND (valid_from IS NULL OR valid_from<=now()) AND (valid_until IS NULL OR valid_until>now()) AND (user_group_id IS NULL OR ($3='group' AND user_group_id=ANY($4::uuid[]))) ORDER BY id FOR SHARE`, tenantID, productID, visibility, uuidArray(visibleGroupIDs))
	if err != nil {
		return 0, 0, err
	}
	for priceRows.Next() {
		var id string
		var groupID *string
		if err := priceRows.Scan(&id, &groupID); err != nil {
			priceRows.Close()
			return 0, 0, err
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
		return 0, 0, err
	}
	priceOK := publishPriceCoverage(visibility, visibleGroupIDs, hasPublicPrice, coveredGroups)
	pools := 1
	var qualifyingPoolID string
	if err := tx.QueryRow(ctx, `SELECT np.id::text FROM plan_node_pools pnp JOIN node_pools np ON np.tenant_id=pnp.tenant_id AND np.id=pnp.pool_id WHERE pnp.tenant_id=$1 AND pnp.plan_version_id=$2::uuid AND np.status<>'disabled' ORDER BY np.id LIMIT 1 FOR SHARE OF np`, tenantID, versionID).Scan(&qualifyingPoolID); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, err
		}
		pools = 0
	}
	nodeOK := true
	var qualifyingNodeID string
	readySQL := nodefabric.StableProtocolReadySQL("n")
	if err := tx.QueryRow(ctx, `SELECT n.id::text FROM plan_node_pools pnp JOIN nodes n ON n.tenant_id=pnp.tenant_id AND n.pool_id=pnp.pool_id JOIN servers s ON s.tenant_id=n.tenant_id AND s.id=n.server_id WHERE pnp.tenant_id=$1 AND pnp.plan_version_id=$2::uuid AND n.serving_status='active' AND s.status='ready' AND s.deleted_at IS NULL AND n.server_port BETWEEN 1 AND 65535 AND `+readySQL+` ORDER BY n.id LIMIT 1 FOR SHARE OF n,s`, tenantID, versionID).Scan(&qualifyingNodeID); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, 0, err
		}
		nodeOK = false
	}
	if err := validatePublishPrerequisites(visibility, priceOK, pools, nodeOK); err != nil {
		return 0, 0, err
	}
	vtag, err := tx.Exec(ctx, `UPDATE plan_versions SET status='published',frozen_at=now(),row_version=row_version+1 WHERE tenant_id=$1 AND id=$2::uuid AND row_version=$3`, tenantID, versionID, expectedVersion)
	if err != nil {
		return 0, 0, err
	}
	if vtag.RowsAffected() != 1 {
		return 0, 0, rowConflict("套餐版本", current)
	}
	ptag, err := tx.Exec(ctx, `UPDATE plans SET current_version_id=$3::uuid,status='active',row_version=row_version+1 WHERE tenant_id=$1 AND id=$2::uuid AND row_version=$4`, tenantID, planID, versionID, expectedPlan)
	if err != nil {
		return 0, 0, err
	}
	if ptag.RowsAffected() != 1 {
		return 0, 0, rowConflict("套餐", planCurrent)
	}
	if _, err := tx.Exec(ctx, `UPDATE products SET status='active',updated_at=now() WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, productID); err != nil {
		return 0, 0, err
	}
	planNext, versionNext = planCurrent+1, current+1
	return planNext, versionNext, audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &actorID, Action: "plan_version.publish", ResourceType: "plan_version", ResourceID: &versionID, APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx), BeforeDigest: map[string]any{"plan_row_version": planCurrent, "version_row_version": current}, AfterDigest: map[string]any{"plan_id": planID, "plan_row_version": planNext, "version_row_version": versionNext, "price_ids": qualifyingPriceIDs, "pool_id": qualifyingPoolID, "node_id": qualifyingNodeID}})
}

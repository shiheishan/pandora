// [INPUT]: 依赖 catalog.go 的 CreatePriceInput、catalogResult / rowConflict，依赖 platform/db、audit、httpx
// [OUTPUT]: 对外提供 Service 的 CreatePlanPrice、ArchivePlanPrice；包内提供 validatePrice 与 createPlanPriceTx（供向导在同一事务里编排）
// [POS]: adminops 套餐目录的价格：从 catalog.go 拆出。价格没有修改入口，只有新建与归档；新建过 P0B 销售闸门，归档带 row_version 乐观锁，两者都同事务审计
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

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
	var out *PriceRow
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		var err error
		out, err = createPlanPriceTx(ctx, tx, tenantID, planID, in)
		return err
	})
	if err != nil {
		return nil, catalogResult(err)
	}
	return out, nil
}

// createPlanPriceTx 在调用方事务里给套餐加一档价格并写审计；销售开关与价格
// 字段须已在事务外判过（CreatePlanPrice 与向导新建共用）。
func createPlanPriceTx(ctx context.Context, tx pgx.Tx, tenantID, planID string, in CreatePriceInput) (*PriceRow, error) {
	var out PriceRow
	var productID, status string
	if err := tx.QueryRow(ctx, `SELECT product_id,status FROM plans WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, planID).Scan(&productID, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFoundOrForbidden()
		}
		return nil, err
	}
	if status == "archived" {
		return nil, httpx.New(httpx.CodeConflict, "已归档套餐不能新增价格")
	}
	if in.UserGroupID != nil {
		var lockedID string
		if err := tx.QueryRow(ctx, `SELECT id::text FROM user_groups WHERE tenant_id=$1 AND id=$2::uuid FOR KEY SHARE`, tenantID, *in.UserGroupID).Scan(&lockedID); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			return nil, httpx.Invalid(map[string]string{"user_group_id": "用户组不存在"})
		}
	}
	if err := tx.QueryRow(ctx, `INSERT INTO prices(tenant_id,product_id,currency,unit_amount,billing_interval,interval_count,trial_days,status,user_group_id,valid_from,valid_until) VALUES($1,$2::uuid,$3,$4,$5,$6,$7,'active',$8::uuid,$9,$10) RETURNING id,currency,unit_amount,billing_interval,interval_count,trial_days,status,user_group_id,valid_from,valid_until,row_version`, tenantID, productID, in.Currency, in.UnitAmount, in.BillingInterval, in.IntervalCount, in.TrialDays, in.UserGroupID, in.ValidFrom, in.ValidUntil).Scan(&out.ID, &out.Currency, &out.UnitAmount, &out.Interval, &out.Count, &out.TrialDays, &out.Status, &out.UserGroupID, &out.ValidFrom, &out.ValidUntil, &out.RowVersion); err != nil {
		return nil, err
	}
	return &out, audit.Write(ctx, tx, tenantID, audit.Entry{ActorKind: "admin", ActorID: &in.ActorID, Action: "price.create", ResourceType: "price", ResourceID: &out.ID, APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx), AfterDigest: map[string]any{"plan_id": planID, "currency": in.Currency, "unit_amount": in.UnitAmount, "user_group_id": in.UserGroupID}})
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

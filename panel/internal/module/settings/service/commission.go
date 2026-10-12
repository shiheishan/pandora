package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
)

// 分销佣金这一组的键。
const (
	keyCommissionRatePercent = "commission.rate_percent" // int，0–50
	keyCommissionFreezeDays  = "commission.freeze_days"  // int，0–90
	keyCommissionMinWithdraw = "commission.min_withdraw" // string，元，两位小数，如 "100.00"（R9：钱不以数字放 jsonb）
	keyCommissionScope       = "commission.scope"        // string：every_order / first_order
)

// 计佣范围。
const (
	// CommissionScopeEveryOrder 是被推荐人每一笔订单都返佣。
	CommissionScopeEveryOrder = "every_order"
	// CommissionScopeFirstOrder 是只对被推荐人第一笔计佣订单返佣。
	CommissionScopeFirstOrder = "first_order"
)

// 取值上限。
const (
	// CommissionMaxRatePercent 是佣金比例上限：再高就不是分销而是送钱了，挡手滑多打一个零。
	CommissionMaxRatePercent = 50
	// CommissionMaxFreezeDays 是冻结天数上限。
	CommissionMaxFreezeDays = 90
)

const (
	defaultCommissionRatePercent = 0
	defaultCommissionFreezeDays  = 3
	defaultCommissionMinWithdraw = "100.00"
	auditCommissionChanged       = "commission.config_changed"
)

var commissionKeys = []string{keyCommissionMinWithdraw, keyCommissionFreezeDays, keyCommissionRatePercent, keyCommissionScope}

// Commission 是分销参数。MinWithdraw 是元（两位小数）。
type Commission struct {
	RatePercent int
	FreezeDays  int
	MinWithdraw decimal.Decimal
	Scope       string
}

// CommissionUpdate 是分销参数的部分更新：nil 字段保持原值。
type CommissionUpdate struct {
	RatePercent *int
	FreezeDays  *int
	MinWithdraw *decimal.Decimal
	Scope       *string
}

// ValidCommissionScope 判断计佣范围是否合法。
func ValidCommissionScope(s string) bool {
	return s == CommissionScopeEveryOrder || s == CommissionScopeFirstOrder
}

// decodeCommission 解出分销参数：缺行取缺省；比例、天数、金额被手改坏按内部错误；范围不认识按每笔订单兜底（旧行为）。
func decodeCommission(vals map[string]json.RawMessage) (Commission, error) {
	rate, err := decodeOr(vals, keyCommissionRatePercent, defaultCommissionRatePercent)
	if err != nil {
		return Commission{}, err
	}
	if rate < 0 || rate > CommissionMaxRatePercent {
		return Commission{}, fmt.Errorf("设置 %s 的值 %d 超出范围", keyCommissionRatePercent, rate)
	}
	freeze, err := decodeOr(vals, keyCommissionFreezeDays, defaultCommissionFreezeDays)
	if err != nil {
		return Commission{}, err
	}
	if freeze < 0 || freeze > CommissionMaxFreezeDays {
		return Commission{}, fmt.Errorf("设置 %s 的值 %d 超出范围", keyCommissionFreezeDays, freeze)
	}
	minText, err := decodeOr(vals, keyCommissionMinWithdraw, defaultCommissionMinWithdraw)
	if err != nil {
		return Commission{}, err
	}
	minW, perr := decimal.NewFromString(minText)
	if perr != nil || money.Check(minW) != nil || minW.IsNegative() {
		return Commission{}, fmt.Errorf("设置 %s 的值 %q 不是合法金额", keyCommissionMinWithdraw, minText)
	}
	scope, err := decodeOr(vals, keyCommissionScope, CommissionScopeEveryOrder)
	if err != nil {
		return Commission{}, err
	}
	if !ValidCommissionScope(scope) {
		scope = CommissionScopeEveryOrder
	}
	return Commission{RatePercent: rate, FreezeDays: freeze, MinWithdraw: minW, Scope: scope}, nil
}

// Commission 读取分销参数。
func (s *Service) Commission(ctx context.Context) (Commission, error) {
	vals, err := s.store.Values(ctx, commissionKeys)
	if err != nil {
		return Commission{}, err
	}
	return decodeCommission(vals)
}

// SetCommission 在锁住本组各行的事务里合并、写入分销参数；有改动才写 commission.config_changed 审计。
func (s *Service) SetCommission(ctx context.Context, u CommissionUpdate) error {
	fields := map[string]string{}
	if u.RatePercent != nil && (*u.RatePercent < 0 || *u.RatePercent > CommissionMaxRatePercent) {
		fields["rate_percent"] = "佣金比例需在 0 到 50 之间"
	}
	if u.FreezeDays != nil && (*u.FreezeDays < 0 || *u.FreezeDays > CommissionMaxFreezeDays) {
		fields["freeze_days"] = "冻结天数需在 0 到 90 之间"
	}
	if u.MinWithdraw != nil {
		if u.MinWithdraw.IsNegative() {
			fields["min_withdraw"] = "最低提现金额不能为负"
		} else if money.Check(*u.MinWithdraw) != nil {
			fields["min_withdraw"] = "最低提现金额最多两位小数"
		}
	}
	if u.Scope != nil && !ValidCommissionScope(*u.Scope) {
		fields["scope"] = "计佣范围只能是 first_order 或 every_order"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, commissionKeys)
		if err != nil {
			return err
		}
		old, err := decodeCommission(vals)
		if err != nil {
			return err
		}
		v := old
		if u.RatePercent != nil {
			v.RatePercent = *u.RatePercent
		}
		if u.FreezeDays != nil {
			v.FreezeDays = *u.FreezeDays
		}
		if u.MinWithdraw != nil {
			v.MinWithdraw = *u.MinWithdraw
		}
		if u.Scope != nil {
			v.Scope = *u.Scope
		}
		changes, b, a := diff(commissionMap(old), commissionMap(v))
		if len(changes) == 0 {
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		return s.writeAudit(ctx, auditCommissionChanged, auditResourceSettings, map[string]any{
			"group":  "commission",
			"before": b,
			"after":  a,
		})
	})
}

// commissionMap 把分销参数摊成「键 → 存储值」；金额存成固定两位小数的字符串。
func commissionMap(c Commission) map[string]any {
	return map[string]any{
		keyCommissionRatePercent: c.RatePercent,
		keyCommissionFreezeDays:  c.FreezeDays,
		keyCommissionMinWithdraw: c.MinWithdraw.StringFixed(2),
		keyCommissionScope:       c.Scope,
	}
}

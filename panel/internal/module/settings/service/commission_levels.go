// 分销设置中的多级分销与提现开关 / 提现方式两组键。
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 多级分销与提现相关键。
const (
	keyCommissionDistributionEnabled = "commission.distribution_enabled" // bool，缺省 false
	keyCommissionDistributionL1      = "commission.distribution_l1"      // int，0–100，第一级（直接邀请人）分到的百分比
	keyCommissionDistributionL2      = "commission.distribution_l2"      // int，0–100
	keyCommissionDistributionL3      = "commission.distribution_l3"      // int，0–100
	keyCommissionWithdrawEnabled     = "commission.withdraw_enabled"     // bool，缺省 true
	keyCommissionWithdrawMethods     = "commission.withdraw_methods"     // string，逗号分隔、按目录顺序，缺省 "alipay"
)

// 提现方式目录里的取值（与库里 withdrawals.method 的 CHECK 一致，另有迁移前的 other 只用于显示）。
const (
	WithdrawMethodAlipay    = "alipay"
	WithdrawMethodWechat    = "wechat"
	WithdrawMethodBank      = "bank"
	WithdrawMethodPayPal    = "paypal"
	WithdrawMethodUSDTTRC20 = "usdt_trc20"
	WithdrawMethodUSDTERC20 = "usdt_erc20"
	WithdrawMethodUSDTBEP20 = "usdt_bep20"
)

// WithdrawMethodCatalog 是后台可勾选的提现方式，按展示顺序；迁移前的自由填写（other）不在目录里，不能再选。
var WithdrawMethodCatalog = []string{
	WithdrawMethodAlipay,
	WithdrawMethodWechat,
	WithdrawMethodBank,
	WithdrawMethodPayPal,
	WithdrawMethodUSDTTRC20,
	WithdrawMethodUSDTERC20,
	WithdrawMethodUSDTBEP20,
}

// ValidWithdrawMethod 判断是否目录里的方式。
func ValidWithdrawMethod(m string) bool {
	for _, c := range WithdrawMethodCatalog {
		if m == c {
			return true
		}
	}
	return false
}

// CommissionMaxSharePercent 是各级分销比例上限。
const CommissionMaxSharePercent = 100

// defaultCommissionWithdrawMethods 是缺省只开支付宝（与迁移前多数人自由填写的支付宝账号一致）。
const defaultCommissionWithdrawMethods = WithdrawMethodAlipay

var commissionDistributionKeys = []string{
	keyCommissionDistributionEnabled,
	keyCommissionDistributionL1,
	keyCommissionDistributionL2,
	keyCommissionDistributionL3,
}

var commissionWithdrawKeys = []string{keyCommissionWithdrawEnabled, keyCommissionWithdrawMethods}

// CommissionDistribution 是多级分销参数：Levels[0..2] 是第一到第三级分到这一单佣金总额的百分比。关闭时只有直接邀请人拿 100%。
type CommissionDistribution struct {
	Enabled bool
	Levels  [3]int
}

// CommissionDistributionUpdate 是部分更新：nil 保持原值。
type CommissionDistributionUpdate struct {
	Enabled    *bool
	L1, L2, L3 *int
}

// CommissionWithdraw 是提现开关与可用方式（按目录顺序，去重）。关闭提现只关站点币种：站点币种的佣金解冻后自动转入余额，
// 其他币种仍按 Methods 提现。
type CommissionWithdraw struct {
	Enabled bool
	Methods []string
}

// CommissionWithdrawUpdate 是部分更新：nil 保持原值。
type CommissionWithdrawUpdate struct {
	Enabled *bool
	Methods *[]string
}

// decodeCommissionDistribution 解出多级分销参数：缺行取缺省（关、0/0/0）；比例被手改坏（单项越界或三级合计超过 100）按内部错误，
// 不让合计超过一单佣金总额的份额进到计提。
func decodeCommissionDistribution(vals map[string]json.RawMessage) (CommissionDistribution, error) {
	enabled, err := decodeOr(vals, keyCommissionDistributionEnabled, false)
	if err != nil {
		return CommissionDistribution{}, err
	}
	l1, err := decodeOr(vals, keyCommissionDistributionL1, 0)
	if err != nil {
		return CommissionDistribution{}, err
	}
	if l1 < 0 || l1 > CommissionMaxSharePercent {
		return CommissionDistribution{}, fmt.Errorf("设置 %s 的值 %d 超出范围", keyCommissionDistributionL1, l1)
	}
	l2, err := decodeOr(vals, keyCommissionDistributionL2, 0)
	if err != nil {
		return CommissionDistribution{}, err
	}
	if l2 < 0 || l2 > CommissionMaxSharePercent {
		return CommissionDistribution{}, fmt.Errorf("设置 %s 的值 %d 超出范围", keyCommissionDistributionL2, l2)
	}
	l3, err := decodeOr(vals, keyCommissionDistributionL3, 0)
	if err != nil {
		return CommissionDistribution{}, err
	}
	if l3 < 0 || l3 > CommissionMaxSharePercent {
		return CommissionDistribution{}, fmt.Errorf("设置 %s 的值 %d 超出范围", keyCommissionDistributionL3, l3)
	}
	if sum := l1 + l2 + l3; sum > CommissionMaxSharePercent {
		return CommissionDistribution{}, fmt.Errorf("多级分销三级比例合计 %d 超过 %d", sum, CommissionMaxSharePercent)
	}
	return CommissionDistribution{
		Enabled: enabled,
		Levels:  [3]int{l1, l2, l3},
	}, nil
}

// decodeCommissionWithdraw 解出提现设置：缺行时开着、只开支付宝；不认识的方式丢掉（向前兼容）。
func decodeCommissionWithdraw(vals map[string]json.RawMessage) (CommissionWithdraw, error) {
	enabled, err := decodeOr(vals, keyCommissionWithdrawEnabled, true)
	if err != nil {
		return CommissionWithdraw{}, err
	}
	methodsText, err := decodeOr(vals, keyCommissionWithdrawMethods, defaultCommissionWithdrawMethods)
	if err != nil {
		return CommissionWithdraw{}, err
	}
	parts := strings.Split(methodsText, ",")
	var raw []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		raw = append(raw, p)
	}
	methods, _ := normalizeWithdrawMethods(raw)
	return CommissionWithdraw{Enabled: enabled, Methods: methods}, nil
}

// normalizeWithdrawMethods 按目录顺序去重；第二个返回值为 false 表示输入里有目录外的值。
func normalizeWithdrawMethods(in []string) ([]string, bool) {
	seen := make(map[string]bool)
	allValid := true
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		if !ValidWithdrawMethod(m) {
			allValid = false
			continue
		}
		seen[m] = true
	}
	var out []string
	for _, c := range WithdrawMethodCatalog {
		if seen[c] {
			out = append(out, c)
		}
	}
	return out, allValid
}

// validateCommissionDistribution 校验合并后的多级分销参数，返回字段错误（没有问题时为空）。
func validateCommissionDistribution(d CommissionDistribution) map[string]string {
	fields := map[string]string{}
	keys := []string{"distribution_l1", "distribution_l2", "distribution_l3"}
	for i := 0; i < 3; i++ {
		v := d.Levels[i]
		if v < 0 || v > CommissionMaxSharePercent {
			fields[keys[i]] = "比例需在 0 到 100 之间"
		}
	}
	sum := d.Levels[0] + d.Levels[1] + d.Levels[2]
	if sum > CommissionMaxSharePercent {
		fields["distribution_l1"] = "三级比例之和不能超过 100"
	}
	if d.Enabled && sum == 0 {
		fields["distribution_l1"] = "开启多级分销时至少要给一级设比例"
	}
	return fields
}

// CommissionDistribution 读取多级分销参数。
func (s *Service) CommissionDistribution(ctx context.Context) (CommissionDistribution, error) {
	vals, err := s.store.Values(ctx, commissionDistributionKeys)
	if err != nil {
		return CommissionDistribution{}, err
	}
	return decodeCommissionDistribution(vals)
}

// SetCommissionDistribution 在锁住本组各行的事务里合并、写入多级分销参数。
func (s *Service) SetCommissionDistribution(ctx context.Context, u CommissionDistributionUpdate) error {
	fields := map[string]string{}
	if u.L1 != nil && (*u.L1 < 0 || *u.L1 > CommissionMaxSharePercent) {
		fields["distribution_l1"] = "比例需在 0 到 100 之间"
	}
	if u.L2 != nil && (*u.L2 < 0 || *u.L2 > CommissionMaxSharePercent) {
		fields["distribution_l2"] = "比例需在 0 到 100 之间"
	}
	if u.L3 != nil && (*u.L3 < 0 || *u.L3 > CommissionMaxSharePercent) {
		fields["distribution_l3"] = "比例需在 0 到 100 之间"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, commissionDistributionKeys)
		if err != nil {
			return err
		}
		old, err := decodeCommissionDistribution(vals)
		if err != nil {
			return err
		}
		v := old
		if u.Enabled != nil {
			v.Enabled = *u.Enabled
		}
		if u.L1 != nil {
			v.Levels[0] = *u.L1
		}
		if u.L2 != nil {
			v.Levels[1] = *u.L2
		}
		if u.L3 != nil {
			v.Levels[2] = *u.L3
		}
		vf := validateCommissionDistribution(v)
		if len(vf) > 0 {
			return errs.Invalid(vf)
		}
		changes, b, a := diff(distributionMap(old), distributionMap(v))
		if len(changes) == 0 {
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		return s.writeAudit(ctx, auditCommissionChanged, auditResourceSettings, map[string]any{
			"group":  "commission_distribution",
			"before": b,
			"after":  a,
		})
	})
}

// CommissionWithdraw 读取提现开关与方式。
func (s *Service) CommissionWithdraw(ctx context.Context) (CommissionWithdraw, error) {
	vals, err := s.store.Values(ctx, commissionWithdrawKeys)
	if err != nil {
		return CommissionWithdraw{}, err
	}
	return decodeCommissionWithdraw(vals)
}

// SetCommissionWithdraw 在锁住本组各行的事务里合并、写入提现设置。
func (s *Service) SetCommissionWithdraw(ctx context.Context, u CommissionWithdrawUpdate) error {
	if u.Methods != nil {
		_, ok := normalizeWithdrawMethods(*u.Methods)
		if !ok {
			return errs.Invalid(map[string]string{"withdraw_methods": "提现方式不在可选范围内"})
		}
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, commissionWithdrawKeys)
		if err != nil {
			return err
		}
		old, err := decodeCommissionWithdraw(vals)
		if err != nil {
			return err
		}
		v := old
		if u.Enabled != nil {
			v.Enabled = *u.Enabled
		}
		if u.Methods != nil {
			v.Methods, _ = normalizeWithdrawMethods(*u.Methods)
		}
		if v.Enabled && len(v.Methods) == 0 {
			return errs.Invalid(map[string]string{"withdraw_methods": "开着提现时至少要选一种提现方式"})
		}
		changes, b, a := diff(withdrawMap(old), withdrawMap(v))
		if len(changes) == 0 {
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		return s.writeAudit(ctx, auditCommissionChanged, auditResourceSettings, map[string]any{
			"group":  "commission_withdraw",
			"before": b,
			"after":  a,
		})
	})
}

// distributionMap 把多级分销参数摊成「键 → 存储值」。
func distributionMap(d CommissionDistribution) map[string]any {
	return map[string]any{
		keyCommissionDistributionEnabled: d.Enabled,
		keyCommissionDistributionL1:      d.Levels[0],
		keyCommissionDistributionL2:      d.Levels[1],
		keyCommissionDistributionL3:      d.Levels[2],
	}
}

// withdrawMap 把提现设置摊成「键 → 存储值」；方式列表存成逗号串，diff 才能比较。
func withdrawMap(w CommissionWithdraw) map[string]any {
	return map[string]any{
		keyCommissionWithdrawEnabled: w.Enabled,
		keyCommissionWithdrawMethods: strings.Join(w.Methods, ","),
	}
}

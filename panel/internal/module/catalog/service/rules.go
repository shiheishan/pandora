package service

// 后台写用例的输入规整与校验（事务外做完，事务里的 *Tx 假定输入已校验）。字段错误一律汇成 422 的 fields。

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

const (
	// maxInt4 是库里 int 列的上界；超出时回 422，不让它变成 22003 → 500。
	maxInt4 = math.MaxInt32
	// maxTrafficBytes 是流量额度的上界（1 PiB）。
	maxTrafficBytes = int64(1 << 50)
	// maxValidityDays 是一次性价有效天数的上界（与库里 CHECK 一致）。
	maxValidityDays = 36500
	// maxResetLimit 是流量重置次数上限的上界（与库里 CHECK 一致）。
	maxResetLimit = 1000
	// maxPlanPools 是一个套餐最多绑的节点池数（与 node 的 MaxPlanPools 一致）。
	maxPlanPools = 500
	maxNameRunes = 120
	maxDescRunes = 2000
)

// maxAmountYuan 是价格与重置价的上界（元），换成分不会溢出 int64。
var maxAmountYuan = decimal.New(1_000_000, 0)

// subscriptionTiers 是订阅套餐允许的四档周期（月 / 季 / 半年 / 年），顺序即展示顺序。
var subscriptionTiers = []struct {
	Interval string
	Count    int
}{
	{IntervalMonth, 1}, {IntervalMonth, 3}, {IntervalMonth, 6}, {IntervalYear, 1},
}

// isSubscriptionTier 报告 (interval, count) 是不是订阅套餐的四档之一。
func isSubscriptionTier(interval string, count int) bool {
	for _, t := range subscriptionTiers {
		if t.Interval == interval && t.Count == count {
			return true
		}
	}
	return false
}

func tierKey(interval string, count int) string { return fmt.Sprintf("%s/%d", interval, count) }

func hasNUL(s string) bool { return strings.ContainsRune(s, 0) }

// invalid 把字段错误转成 422；没有错误回 nil。
func invalid(fields map[string]string) error {
	if len(fields) == 0 {
		return nil
	}
	return errs.Invalid(fields)
}

// prepareBasic 规整名称与说明（去首尾空白，空说明记 nil），把字段错误写进 fields。
func prepareBasic(name *string, desc **string, fields map[string]string) {
	*name = strings.TrimSpace(*name)
	if n := utf8.RuneCountInString(*name); n == 0 || n > maxNameRunes || hasNUL(*name) {
		fields["name"] = fmt.Sprintf("必填，最多 %d 个字符，且不能包含非法字符", maxNameRunes)
	}
	if *desc != nil {
		d := strings.TrimSpace(**desc)
		switch {
		case d == "":
			*desc = nil
		case hasNUL(d) || utf8.RuneCountInString(d) > maxDescRunes:
			fields["description"] = fmt.Sprintf("最多 %d 个字符，且不能包含非法字符", maxDescRunes)
		default:
			*desc = &d
		}
	}
}

// checkAmount 校验一个金额（元）：不为负、最多两位小数、不超过上界；positive 为真时还要大于 0。
func checkAmount(key string, v decimal.Decimal, positive bool, fields map[string]string) {
	switch {
	case v.IsNegative():
		fields[key] = "不能为负数"
	case positive && v.IsZero():
		fields[key] = "必须大于 0"
	case money.Check(v) != nil:
		fields[key] = "最多两位小数"
	case v.GreaterThan(maxAmountYuan):
		fields[key] = "不能超过 1000000 元"
	}
}

// validateSales 规整并校验销售设置：可见组去重检查、只有分组可见才带组；一次性套餐不能续费。
func validateSales(kind string, in *SalesInput, fields map[string]string) {
	if in.Visibility == "" {
		in.Visibility = VisibilityPublic
	}
	switch in.Visibility {
	case VisibilityPublic, VisibilityAuthenticated, VisibilityGroup, VisibilityHidden:
	default:
		fields["visibility"] = "不支持的可见范围"
	}
	if in.VisibleGroupIDs == nil {
		in.VisibleGroupIDs = []uuid.UUID{}
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range in.VisibleGroupIDs {
		if id == uuid.Nil || seen[id] {
			fields["visible_group_ids"] = "必须是无重复的 UUID 列表"
			break
		}
		seen[id] = true
	}
	if in.Visibility == VisibilityGroup && len(in.VisibleGroupIDs) == 0 {
		fields["visible_group_ids"] = "指定用户组可见时至少选一个用户组"
	}
	if in.Visibility != VisibilityGroup && len(in.VisibleGroupIDs) != 0 {
		fields["visible_group_ids"] = "只有指定用户组可见时才能选用户组"
	}
	if kind == KindOneTime && in.AllowRenewal {
		fields["allow_renewal"] = "一次性套餐不能续费"
	}
}

// validateReset 校验流量重置：每次价格大于 0；次数上限为空（不限）或 1–1000。
func validateReset(price decimal.Decimal, limit *int, fields map[string]string) {
	checkAmount("reset.price", price, true, fields)
	if limit != nil && (*limit < 1 || *limit > maxResetLimit) {
		fields["reset.limit"] = fmt.Sprintf("次数上限为 1–%d，不限请留空", maxResetLimit)
	}
}

// validateUsage 校验用量：都可为空（不限）；流量 1 字节到 1 PiB，设备数与限速为正整数。
func validateUsage(in UsageInput, fields map[string]string) {
	if in.TrafficBytes != nil && (*in.TrafficBytes <= 0 || *in.TrafficBytes > maxTrafficBytes) {
		fields["traffic_bytes"] = "必须大于 0 且不超过 1 PiB；不限流量请留空"
	}
	if in.MaxDevices != nil && (*in.MaxDevices <= 0 || *in.MaxDevices > maxInt4) {
		fields["max_devices"] = "必须为正整数；不限设备请留空"
	}
	if in.ThrottleKbps != nil && (*in.ThrottleKbps <= 0 || *in.ThrottleKbps > maxInt4) {
		fields["throttle_kbps"] = "必须为正整数；不限速请留空"
	}
}

// validateTiers 校验新建订阅套餐的价格档：至少一档，每档是四档之一且不重复，金额合法。
func validateTiers(tiers []TierInput, fields map[string]string) {
	if len(tiers) == 0 {
		fields["prices"] = "订阅套餐至少设一档价格"
		return
	}
	seen := map[string]bool{}
	for i, t := range tiers {
		key := fmt.Sprintf("prices.%d", i)
		if !isSubscriptionTier(t.BillingInterval, t.IntervalCount) {
			fields[key] = "周期只能是月付、季付、半年付或年付"
			continue
		}
		k := tierKey(t.BillingInterval, t.IntervalCount)
		if seen[k] {
			fields[key] = "同一周期只能有一档价格"
			continue
		}
		seen[k] = true
		checkAmount(key+".unit_amount", t.UnitAmount, false, fields)
	}
}

// validateTierChanges 校验订阅套餐改价：至少一档，每档是四档之一且不重复；金额为空表示不卖这一档。
func validateTierChanges(changes []TierChange, fields map[string]string) {
	if len(changes) == 0 {
		fields["prices"] = "至少给出一档价格"
		return
	}
	seen := map[string]bool{}
	for i, t := range changes {
		key := fmt.Sprintf("prices.%d", i)
		if !isSubscriptionTier(t.BillingInterval, t.IntervalCount) {
			fields[key] = "周期只能是月付、季付、半年付或年付"
			continue
		}
		k := tierKey(t.BillingInterval, t.IntervalCount)
		if seen[k] {
			fields[key] = "同一周期只能出现一次"
			continue
		}
		seen[k] = true
		if t.UnitAmount != nil {
			checkAmount(key+".unit_amount", *t.UnitAmount, false, fields)
		}
	}
}

// validateOneTime 校验一次性套餐的价格与有效天数（空 = 永久）。
func validateOneTime(in OneTimeInput, fields map[string]string) {
	checkAmount("unit_amount", in.UnitAmount, false, fields)
	if in.ValidityDays != nil && (*in.ValidityDays < 1 || *in.ValidityDays > maxValidityDays) {
		fields["validity_days"] = fmt.Sprintf("有效天数为 1–%d，永久请留空", maxValidityDays)
	}
}

// validatePoolIDs 校验线路：不含空 UUID、不重复、不超过 500 个。
func validatePoolIDs(ids []uuid.UUID, fields map[string]string) {
	if len(ids) > maxPlanPools {
		fields["pool_ids"] = fmt.Sprintf("一个套餐最多绑定 %d 个节点池", maxPlanPools)
		return
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range ids {
		if id == uuid.Nil || seen[id] {
			fields["pool_ids"] = "必须是无重复的 UUID 列表"
			return
		}
		seen[id] = true
	}
}

// validatePlanCreate 规整并校验新建套餐的全部输入（先选类型：订阅给 Tiers，一次性给 OneTime）。
func validatePlanCreate(in *PlanCreate) error {
	fields := map[string]string{}
	switch in.Kind {
	case KindSubscription:
		validateTiers(in.Tiers, fields)
		if in.OneTime != nil {
			fields["unit_amount"] = "订阅套餐按周期定价，不能设一次性价格"
		}
	case KindOneTime:
		if in.OneTime == nil {
			fields["unit_amount"] = "一次性套餐必须设价格"
		} else {
			validateOneTime(*in.OneTime, fields)
		}
		if len(in.Tiers) > 0 {
			fields["prices"] = "一次性套餐只有一个价格，不能设周期价"
		}
	default:
		fields["kind"] = "套餐类型只能是订阅套餐或一次性套餐"
	}
	prepareBasic(&in.Name, &in.Description, fields)
	validateUsage(in.Usage, fields)
	validatePoolIDs(in.PoolIDs, fields)
	if in.Reset != nil {
		validateReset(in.Reset.Price, in.Reset.Limit, fields)
	}
	validateSales(in.Kind, &in.Sales, fields)
	return invalid(fields)
}

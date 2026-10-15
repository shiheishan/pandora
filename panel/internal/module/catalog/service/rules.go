package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

var catalogCodePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

const (
	maxPlanHighlights     = 5
	maxPlanHighlightRunes = 40
	maxPackBytes          = int64(1 << 50)
	// maxInt4 是库里 int 列的上界；超出时回 422，不让它变成 22003 → 500。
	maxInt4 = math.MaxInt32
	// maxTrafficGB 是向导里流量额度的上界（1 PiB，与流量包同口径），乘 BytesPerGB 不会溢出 int64。
	maxTrafficGB = int64(1 << 20)
)

var maxPackAmountYuan = decimal.New(1_000_000, 0)

func hasNUL(s string) bool {
	return strings.ContainsRune(s, 0)
}

// normalizeHighlights 规整卖点列表并写回 raw；返回字段级错误（永不为 nil 切片）。
func normalizeHighlights(raw []string) ([]string, map[string]string) {
	out := make([]string, 0, len(raw))
	fields := map[string]string{}
	if len(raw) > maxPlanHighlights {
		fields["highlights"] = fmt.Sprintf("最多 %d 条卖点", maxPlanHighlights)
	}
	seen := map[string]bool{}
	for i, item := range raw {
		item = strings.TrimSpace(item)
		key := fmt.Sprintf("highlights.%d", i)
		switch n := len([]rune(item)); {
		case n == 0:
			fields[key] = "卖点不能为空"
		case n > maxPlanHighlightRunes:
			fields[key] = fmt.Sprintf("每条卖点最多 %d 个字", maxPlanHighlightRunes)
		case seen[item]:
			fields[key] = "卖点不能重复"
		}
		if hasNUL(item) {
			fields[key] = "卖点不能包含非法字符"
		}
		seen[item] = true
		out = append(out, item)
	}
	return out, fields
}

func validatePlanFields(code, name, visibility string, groupIDs []uuid.UUID, from, until *time.Time, purchaseLimit, stock *int) map[string]string {
	fields := map[string]string{}
	if !catalogCodePattern.MatchString(strings.TrimSpace(code)) {
		fields["code"] = "需为 2-64 位小写字母、数字、下划线或连字符"
	}
	if n := len([]rune(strings.TrimSpace(name))); n == 0 || n > 120 {
		fields["name"] = "必填且最多 120 个字符"
	}
	if hasNUL(code) {
		fields["code"] = "不能包含非法字符"
	}
	if hasNUL(name) {
		fields["name"] = "不能包含非法字符"
	}
	switch visibility {
	case VisibilityPublic, VisibilityAuthenticated, VisibilityGroup, VisibilityInviteOnly, VisibilityHidden:
	default:
		fields["visibility"] = "不支持的可见性"
	}
	seen := map[uuid.UUID]bool{}
	for _, id := range groupIDs {
		if id == uuid.Nil || seen[id] {
			fields["visible_group_ids"] = "必须是无重复的 UUID 列表"
			break
		}
		seen[id] = true
	}
	if visibility == VisibilityGroup && len(groupIDs) == 0 {
		fields["visible_group_ids"] = "分组可见套餐至少需要一个用户组"
	}
	if visibility != VisibilityGroup && len(groupIDs) != 0 {
		fields["visible_group_ids"] = "只有分组可见套餐可以设置用户组"
	}
	if from != nil && until != nil && !until.After(*from) {
		fields["visible_until"] = "必须晚于 visible_from"
	}
	if purchaseLimit != nil && (*purchaseLimit <= 0 || *purchaseLimit > maxInt4) {
		fields["purchase_limit_per_user"] = "必须为正整数，且不超过 2147483647"
	}
	if stock != nil && (*stock < 0 || *stock > maxInt4) {
		fields["stock_total"] = "不能为负数，且不超过 2147483647"
	}
	return fields
}

// preparePlanInput 在进事务前规整并校验套餐资料。
func preparePlanInput(in *PlanInput) error {
	in.Code = strings.TrimSpace(in.Code)
	in.Name = strings.TrimSpace(in.Name)
	if in.Visibility == "" {
		in.Visibility = VisibilityPublic
	}
	if in.Description != nil {
		s := strings.TrimSpace(*in.Description)
		if s == "" {
			in.Description = nil
		} else if hasNUL(s) || utf8.RuneCountInString(s) > 2000 {
			return errs.Invalid(map[string]string{"description": "最多 2000 个字符，且不能包含非法字符"})
		} else {
			in.Description = &s
		}
	}
	hl, hlFields := normalizeHighlights(in.Highlights)
	in.Highlights = hl
	fields := validatePlanFields(in.Code, in.Name, in.Visibility, in.VisibleGroupIDs,
		in.VisibleFrom, in.VisibleUntil, in.PurchaseLimitPerUser, in.StockTotal)
	for k, v := range hlFields {
		fields[k] = v
	}
	if in.SortOrder < -maxInt4 || in.SortOrder > maxInt4 {
		fields["sort_order"] = "排序号超出范围"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

// validateVersionInput 校验并写回版本语义（权益码 trim、空 value 默认 true）。
func validateVersionInput(in *VersionInput) error {
	fields := map[string]string{}
	switch in.QuotaResetStrategy {
	case ResetNever, ResetNaturalMonth, ResetBillingCycle, ResetFixedDay:
	default:
		fields["quota_reset_strategy"] = "不支持的重置策略"
	}
	if in.QuotaResetStrategy == ResetFixedDay {
		if in.QuotaResetDay == nil || *in.QuotaResetDay < 1 || *in.QuotaResetDay > 28 {
			fields["quota_reset_day"] = "固定日必须为 1-28"
		}
	} else if in.QuotaResetDay != nil {
		fields["quota_reset_day"] = "非固定日策略不能设置该字段"
	}
	if in.MaxDevices != nil && (*in.MaxDevices <= 0 || *in.MaxDevices > maxInt4) {
		fields["max_devices"] = "必须为正整数，且不超过 2147483647"
	}
	if in.MaxConcurrent != nil && (*in.MaxConcurrent <= 0 || *in.MaxConcurrent > maxInt4) {
		fields["max_concurrent"] = "必须为正整数，且不超过 2147483647"
	}
	if in.DeviceReleaseHours < 0 || in.DeviceReleaseHours > maxInt4 {
		fields["device_release_hours"] = "不能为负数，且不超过 2147483647"
	}
	switch in.OveragePolicy {
	case "", OveragePolicySuspend:
	default:
		fields["overage_policy"] = "只支持 suspend：流量用完后停止服务"
	}
	if in.OveragePolicy == "" {
		in.OveragePolicy = OveragePolicySuspend
	}
	if in.ThrottleKbps != nil && (*in.ThrottleKbps <= 0 || *in.ThrottleKbps > maxInt4) {
		fields["throttle_kbps"] = "必须为正整数且不超过 2147483647；不限速请留空"
	}
	if in.Notes != nil && (hasNUL(*in.Notes) || utf8.RuneCountInString(*in.Notes) > 2000) {
		fields["notes"] = "最多 2000 个字符，且不能包含非法字符"
	}

	entSeen, quotaSeen := map[string]bool{}, map[string]bool{}
	for i := range in.Entitlements {
		code := strings.TrimSpace(in.Entitlements[i].Code)
		in.Entitlements[i].Code = code
		val := in.Entitlements[i].Value
		if code == "" || utf8.RuneCountInString(code) > 64 || entSeen[code] {
			fields[fmt.Sprintf("entitlements.%d", i)] = "能力码必填、最多 64 个字符且不能重复"
		} else if len(val) > 0 && !json.Valid(val) {
			fields[fmt.Sprintf("entitlements.%d", i)] = "value 必须为有效 JSON"
		} else if hasNUL(code) {
			fields[fmt.Sprintf("entitlements.%d", i)] = "能力码不能包含非法字符"
		}
		if len(val) == 0 {
			in.Entitlements[i].Value = json.RawMessage(`true`)
		}
		entSeen[code] = true
	}
	for i := range in.Quotas {
		metric := strings.TrimSpace(in.Quotas[i].Metric)
		unit := strings.TrimSpace(in.Quotas[i].Unit)
		in.Quotas[i].Metric = metric
		in.Quotas[i].Unit = unit
		key := metric + "\x00" + in.Quotas[i].Period
		if metric == "" || unit == "" || utf8.RuneCountInString(metric) > 64 ||
			utf8.RuneCountInString(unit) > 32 || hasNUL(metric) || hasNUL(unit) || quotaSeen[key] {
			fields[fmt.Sprintf("quotas.%d", i)] = "指标、单位必填且指标/周期必须唯一"
		}
		quotaSeen[key] = true
		switch in.Quotas[i].Period {
		case PeriodTotal, PeriodCycle, PeriodDay, PeriodMonth:
		default:
			fields[fmt.Sprintf("quotas.%d.period", i)] = "不支持的周期"
		}
		if in.Quotas[i].Limit != nil && *in.Quotas[i].Limit < 0 {
			fields[fmt.Sprintf("quotas.%d.limit", i)] = "不能为负数"
		}
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

// validatePriceInput 校验新建价格输入。
func validatePriceInput(in *PriceInput) error {
	fields := map[string]string{}
	cur, err := money.ParseCurrency(in.Currency)
	if err != nil {
		fields["currency"] = "仅允许 CNY 或 USD"
	} else {
		in.Currency = string(cur)
	}
	if in.UnitAmount.IsNegative() {
		fields["unit_amount"] = "不能为负数"
	} else if err := money.Check(in.UnitAmount); err != nil {
		fields["unit_amount"] = "最多两位小数"
	}
	switch in.BillingInterval {
	case "day", "week", "month", "quarter", "year", "one_time":
	default:
		fields["billing_interval"] = "不支持的计费周期"
	}
	if in.IntervalCount <= 0 {
		fields["interval_count"] = "必须为正整数"
	}
	if in.IntervalCount > 32767 {
		fields["interval_count"] = "不能超过 32767"
	}
	if in.ValidFrom != nil && in.ValidUntil != nil && !in.ValidUntil.After(*in.ValidFrom) {
		fields["valid_until"] = "必须晚于 valid_from"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

// preparePackInput 校验流量包输入并写回规整后的名称与币种。
func preparePackInput(in *PackInput) error {
	in.Name = strings.TrimSpace(in.Name)
	fields := map[string]string{}
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 60 {
		fields["name"] = "名称 1 到 60 个字"
	}
	if hasNUL(in.Name) {
		fields["name"] = "不能包含非法字符"
	}
	if in.TrafficBytes <= 0 || in.TrafficBytes > maxPackBytes {
		fields["traffic_bytes"] = "容量必须大于 0，且不超过 1 PiB"
	}
	cur, err := money.ParseCurrency(in.Currency)
	if err != nil {
		fields["currency"] = "仅允许 CNY 或 USD"
	} else {
		in.Currency = string(cur)
	}
	if err := money.CheckNonNegative(in.UnitAmount); err != nil || in.UnitAmount.IsZero() {
		fields["unit_amount"] = "价格必须大于 0"
	} else if in.UnitAmount.GreaterThan(maxPackAmountYuan) {
		fields["unit_amount"] = "价格不能超过 100 万元"
	}
	if in.SortOrder < -1_000_000 || in.SortOrder > 1_000_000 {
		fields["sort_order"] = "排序号超出范围"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

func validateWizardCreate(in *WizardCreate) error {
	if err := preparePlanInput(&in.Plan); err != nil {
		return err
	}
	fields := map[string]string{}
	if in.Publish && len(in.Prices) == 0 {
		fields["prices"] = "要上架就至少得有一档价格，否则用户看得到却买不了"
	}
	if in.TrafficGB != nil && *in.TrafficGB < 0 {
		fields["traffic_gb"] = "流量不能是负数；不限流量请留空"
	} else if in.TrafficGB != nil && *in.TrafficGB > maxTrafficGB {
		fields["traffic_gb"] = "流量不能超过 1048576 GB（1 PiB）"
	}
	if in.MaxDevices != nil && *in.MaxDevices < 0 {
		fields["max_devices"] = "设备数不能是负数；不限请留空"
	}
	seen := map[string]int{}
	for i := range in.Prices {
		in.Prices[i].Currency = strings.ToUpper(strings.TrimSpace(in.Prices[i].Currency))
		if err := validateWizardPriceFields(in.Prices[i], i, seen, fields); err != nil {
			return err
		}
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

func validateWizardPriceFields(p WizardPrice, i int, seen map[string]int, fields map[string]string) error {
	n := i + 1
	pin := wizardPriceInput(p)
	if err := validatePriceInput(&pin); err != nil {
		var ve *errs.Error
		if errors.As(err, &ve) && ve.Code == errs.ValidationFailed {
			for k, v := range ve.Fields {
				fields[fmt.Sprintf("prices.%d.%s", i, k)] = v
			}
		} else {
			return err
		}
	}
	if pin.UnitAmount.IsZero() {
		fields[fmt.Sprintf("prices.%d.unit_amount", i)] = fmt.Sprintf("第 %d 档价格要大于 0", n)
	}
	key := wizardKey(p.BillingInterval, p.IntervalCount, p.Currency)
	if prev, dup := seen[key]; dup {
		fields[fmt.Sprintf("prices.%d.billing_interval", i)] =
			fmt.Sprintf("第 %d 档和第 %d 档的周期与币种完全相同", n, prev)
	}
	seen[key] = n
	return nil
}

func wizardVersionInput(in WizardCreate) VersionInput {
	quotas := make([]Quota, 0, 2)
	if in.TrafficGB != nil && *in.TrafficGB > 0 {
		lim := *in.TrafficGB * BytesPerGB
		quotas = append(quotas, Quota{
			Metric: MetricTraffic, Limit: &lim, Unit: "bytes", Period: PeriodCycle,
		})
	}
	devices := in.MaxDevices
	if devices != nil && *devices == 0 {
		devices = nil
	}
	if devices != nil && *devices > 0 {
		lim := int64(*devices)
		quotas = append(quotas, Quota{
			Metric: MetricDevices, Limit: &lim, Unit: "count", Period: PeriodCycle,
		})
	}
	strategy := in.QuotaResetStrategy
	if strategy == "" {
		strategy = ResetBillingCycle
	}
	return VersionInput{
		QuotaResetStrategy:   strategy,
		QuotaResetDay:        in.QuotaResetDay,
		RenewalExtendsPeriod: true,
		RenewalResetsQuota:   true,
		RenewalKeepsAddons:   true,
		MaxDevices:           devices,
		ThrottleKbps:         in.ThrottleKbps,
		DeviceReleaseHours:   24,
		OveragePolicy:        OveragePolicySuspend,
		Quotas:               quotas,
	}
}

func wizardPriceInput(p WizardPrice) PriceInput {
	return PriceInput{
		Currency:        p.Currency,
		UnitAmount:      p.UnitAmount,
		BillingInterval: p.BillingInterval,
		IntervalCount:   p.IntervalCount,
	}
}

func wizardKey(interval string, count int, currency string) string {
	return fmt.Sprintf("%s/%d/%s", interval, count, currency)
}

func validateWizardQuotaEdits(in WizardUpdate) error {
	fields := map[string]string{}
	if in.TrafficGB != nil && *in.TrafficGB < 0 {
		fields["traffic_gb"] = "流量不能是负数；不限流量填 0"
	} else if in.TrafficGB != nil && *in.TrafficGB > maxTrafficGB {
		fields["traffic_gb"] = "流量不能超过 1048576 GB（1 PiB）"
	}
	if in.MaxDevices.Set && in.MaxDevices.Value != nil && *in.MaxDevices.Value <= 0 {
		fields["max_devices"] = "必须为正整数；不限设备请传 null"
	}
	if in.ThrottleKbps.Set && in.ThrottleKbps.Value != nil && *in.ThrottleKbps.Value <= 0 {
		fields["throttle_kbps"] = "必须为正整数；不限速请传 null"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

func optionalIntResolve(o OptionalInt, current *int) *int {
	if !o.Set {
		return current
	}
	return o.Value
}

// quotaDiffers 报告向导这次是否真的改了额度（cur 是当前发布版本，nil 表示没发布过）：
// 原样提交当前值不算变化，免得平白多出一个版本。
func quotaDiffers(cur *Version, in WizardUpdate) bool {
	if in.TrafficGB == nil && !in.MaxDevices.Set && !in.ThrottleKbps.Set {
		return false
	}
	if cur == nil {
		return true
	}
	if in.MaxDevices.Set && !sameIntPtr(cur.MaxDevices, in.MaxDevices.Value) {
		return true
	}
	if in.ThrottleKbps.Set && !sameIntPtr(cur.ThrottleKbps, in.ThrottleKbps.Value) {
		return true
	}
	if in.TrafficGB != nil {
		var curGB int64
		for _, q := range cur.Quotas {
			if q.Metric == MetricTraffic && q.Limit != nil {
				curGB = *q.Limit / BytesPerGB
			}
		}
		if curGB != *in.TrafficGB {
			return true
		}
	}
	return false
}

func sameIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func inheritVersionInput(cur *Version, in WizardUpdate) VersionInput {
	out := VersionInput{
		QuotaResetStrategy:   ResetBillingCycle,
		RenewalExtendsPeriod: true,
		RenewalResetsQuota:   true,
		RenewalKeepsAddons:   true,
		OveragePolicy:        OveragePolicySuspend,
		Entitlements:         []Entitlement{},
	}
	var curQuotas []Quota
	var curDevices, curThrottle *int
	if cur != nil {
		if cur.QuotaResetStrategy != "" {
			out.QuotaResetStrategy = cur.QuotaResetStrategy
			out.QuotaResetDay = cur.QuotaResetDay
		}
		out.RenewalExtendsPeriod = cur.RenewalExtendsPeriod
		out.RenewalResetsQuota = cur.RenewalResetsQuota
		out.RenewalKeepsAddons = cur.RenewalKeepsAddons
		out.MaxConcurrent = cur.MaxConcurrent
		out.DeviceReleaseHours = cur.DeviceReleaseHours
		out.Notes = cur.Notes
		out.Entitlements = append(out.Entitlements, cur.Entitlements...)
		curQuotas, curDevices, curThrottle = cur.Quotas, cur.MaxDevices, cur.ThrottleKbps
	}
	out.MaxDevices = optionalIntResolve(in.MaxDevices, curDevices)
	out.ThrottleKbps = optionalIntResolve(in.ThrottleKbps, curThrottle)

	out.Quotas = []Quota{}
	trafficPeriod, devicesPeriod := PeriodCycle, PeriodCycle
	for _, q := range curQuotas {
		switch q.Metric {
		case MetricTraffic:
			if in.TrafficGB == nil {
				out.Quotas = append(out.Quotas, q)
			} else {
				trafficPeriod = q.Period
			}
		case MetricDevices:
			devicesPeriod = q.Period
		default:
			out.Quotas = append(out.Quotas, q)
		}
	}
	if in.TrafficGB != nil && *in.TrafficGB > 0 {
		lim := *in.TrafficGB * BytesPerGB
		out.Quotas = append(out.Quotas, Quota{
			Metric: MetricTraffic, Limit: &lim, Unit: "bytes", Period: trafficPeriod,
		})
	}
	if out.MaxDevices != nil {
		lim := int64(*out.MaxDevices)
		out.Quotas = append(out.Quotas, Quota{
			Metric: MetricDevices, Limit: &lim, Unit: "count", Period: devicesPeriod,
		})
	}
	return out
}

func prepareWizardPrices(raw []WizardPrice) (map[string]WizardPrice, error) {
	wanted := map[string]WizardPrice{}
	seen := map[string]int{}
	fields := map[string]string{}
	for i, p := range raw {
		// 币种先规整成大写：键与库里的价格比对、写库都用它
		p.Currency = strings.ToUpper(strings.TrimSpace(p.Currency))
		if err := validateWizardPriceFields(p, i, seen, fields); err != nil {
			return nil, err
		}
		wanted[wizardKey(p.BillingInterval, p.IntervalCount, p.Currency)] = p
	}
	if len(fields) > 0 {
		return nil, errs.Invalid(fields)
	}
	return wanted, nil
}

// priceInWindow 判断价格是否在有效期内（与 billing 口径一致）。
func priceInWindow(p Price, now time.Time) bool {
	return (p.ValidFrom == nil || !p.ValidFrom.After(now)) &&
		(p.ValidUntil == nil || p.ValidUntil.After(now))
}

// publishPriceCoverage 判断发布时在售价格是否覆盖可见性要求（CNY/USD、在窗、公开或组价）。
func publishPriceCoverage(visibility string, groups []uuid.UUID, prices []Price, now time.Time) bool {
	groupSet := make(map[uuid.UUID]bool, len(groups))
	for _, g := range groups {
		groupSet[g] = true
	}
	hasPublic := false
	covered := map[uuid.UUID]bool{}
	for _, p := range prices {
		if p.Status != PriceActive {
			continue
		}
		if p.Currency != money.CNY && p.Currency != money.USD {
			continue
		}
		if !priceInWindow(p, now) {
			continue
		}
		if p.UserGroupID == nil {
			hasPublic = true
			continue
		}
		if visibility == VisibilityGroup && groupSet[*p.UserGroupID] {
			covered[*p.UserGroupID] = true
		}
	}
	if hasPublic {
		return true
	}
	if visibility != VisibilityGroup {
		return false
	}
	for _, id := range groups {
		if !covered[id] {
			return false
		}
	}
	return len(groups) > 0
}

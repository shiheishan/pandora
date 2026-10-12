package service

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/promo/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// couponAlphabet 去掉 I、L、O、0、1：人工抄写易混。
const couponAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// codePattern 与 prefixPattern 是券码字符集，与 00010 里 coupons.code 的 CHECK 一致。
var (
	codePattern   = regexp.MustCompile(`^[A-Z0-9_-]{1,64}$`)
	prefixPattern = regexp.MustCompile(`^[A-Z0-9_-]{0,8}$`)
)

var redemptionTransitions = map[string]map[string]bool{
	RedemptionHeld: {
		RedemptionCaptured: true,
		RedemptionReleased: true,
	},
}

// NormalizeCode 规范化用户输入的券码：去首尾空白，只把 ASCII 小写字母转大写。
// 不用 strings.ToUpper：它会把 ı、ſ 这类字符折成 ASCII，与 PG 的 upper() 口径不同。
func NormalizeCode(code string) string {
	return upperASCII(strings.TrimSpace(code))
}

func upperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 'a' + 'A'
		}
	}
	return string(b)
}

// validCode 报告规范化后的券码是否合法：1 到 64 位 ASCII 大写字母、数字、下划线、短横线（与迁移里的 CHECK 同一字符集）。
func validCode(code string) bool {
	return codePattern.MatchString(code)
}

func validateCode(code string) error {
	if !validCode(code) {
		return errs.Invalid(map[string]string{"code": "优惠码 1 到 64 位，只能用字母、数字、下划线和短横线"})
	}
	return nil
}

func normalizePrefix(prefix string) (string, error) {
	p := upperASCII(strings.TrimSpace(prefix))
	if !prefixPattern.MatchString(p) {
		return "", errs.Invalid(map[string]string{"prefix": "前缀只能用字母、数字、下划线和短横线，最多 8 位"})
	}
	return p, nil
}

// validateSpec 校验并规范化后台新建券的规格。
func validateSpec(spec Spec) (Spec, error) {
	fields := map[string]string{}
	out := spec

	name := strings.TrimSpace(spec.Name)
	if len([]rune(name)) < 1 || len([]rune(name)) > 100 || strings.ContainsRune(name, 0) {
		fields["name"] = "名称 1 到 100 字"
	}
	out.Name = name

	switch spec.Face.Kind {
	case KindPercent:
		if spec.Face.PercentBP < 1 || spec.Face.PercentBP > 10000 {
			fields["discount_value"] = "折扣需在 0.01% 到 100% 之间"
		}
		out.Face = Face{Kind: KindPercent, PercentBP: spec.Face.PercentBP, AmountOff: decimal.Zero}
	case KindFixed:
		if !spec.Face.AmountOff.IsPositive() || money.Check(spec.Face.AmountOff) != nil {
			fields["discount_value"] = "立减金额需大于 0"
		}
		out.Face = Face{Kind: KindFixed, PercentBP: 0, AmountOff: spec.Face.AmountOff}
	default:
		fields["discount_type"] = "只支持 percent 或 fixed"
	}

	cur, err := money.ParseCurrency(spec.Currency)
	if err != nil {
		fields["currency"] = "只支持 CNY 或 USD"
	} else {
		out.Currency = string(cur)
	}

	if spec.MaxDiscount != nil {
		if spec.Face.Kind != KindPercent {
			fields["max_discount"] = "定额券不设封顶"
		} else if !spec.MaxDiscount.IsPositive() || money.Check(*spec.MaxDiscount) != nil {
			fields["max_discount"] = "封顶金额需大于 0"
		}
	}

	if money.CheckNonNegative(spec.MinOrder) != nil {
		fields["min_order_amount"] = "门槛金额不能为负，最多两位小数"
	}

	if spec.MaxRedemptions != nil && *spec.MaxRedemptions <= 0 {
		fields["max_redemptions"] = "填正整数，留空为不限"
	}

	if spec.MaxPerUser < 1 {
		fields["max_redemptions_per_user"] = "每人至少可用 1 次"
	}

	if spec.ValidFrom != nil && spec.ValidUntil != nil && !spec.ValidUntil.After(*spec.ValidFrom) {
		fields["valid_until"] = "结束时间必须晚于开始时间"
	}

	planIDs := spec.PlanIDs
	if planIDs == nil {
		planIDs = []uuid.UUID{}
	}
	seen := make(map[uuid.UUID]struct{}, len(planIDs))
	deduped := make([]uuid.UUID, 0, len(planIDs))
	for _, id := range planIDs {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		deduped = append(deduped, id)
	}
	if len(deduped) > 200 {
		fields["applicable_plan_ids"] = "最多 200 个套餐"
	}
	out.PlanIDs = deduped

	if len(fields) > 0 {
		return Spec{}, errs.Invalid(fields)
	}
	return out, nil
}

func newCode(prefix string) (string, error) {
	var b strings.Builder
	b.Grow(len(prefix) + 10)
	b.WriteString(prefix)
	alphabetLen := big.NewInt(int64(len(couponAlphabet)))
	for i := 0; i < 10; i++ {
		n, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			return "", err
		}
		b.WriteByte(couponAlphabet[n.Int64()])
	}
	return b.String(), nil
}

// faceOf 从库里的券行取出券面。
func faceOf(c model.Coupon) Face {
	if c.PercentOffBP != nil {
		return Face{Kind: KindPercent, PercentBP: *c.PercentOffBP}
	}
	return Face{Kind: KindFixed, AmountOff: *c.AmountOff}
}

func checkBasics(c model.Coupon, t Target, now time.Time) error {
	if c.Status != model.CouponActive {
		return ErrCouponNotFound
	}
	if c.ValidFrom != nil && now.Before(*c.ValidFrom) {
		return ErrCouponNotFound
	}
	if c.ValidUntil != nil && now.After(*c.ValidUntil) {
		return ErrCouponExpired
	}
	if c.MaxRedemptions != nil && c.RedeemedCount+c.ReservedCount >= *c.MaxRedemptions {
		return ErrCouponUsedUp
	}
	if c.Currency != t.Currency {
		return ErrCouponNotFound
	}
	if t.Subtotal.LessThan(c.MinOrderAmount) {
		return ErrCouponMinAmount
	}
	return nil
}

// checkPlan 判断券限定的套餐（planIDs 为空即不限）是否包含这一单的套餐；流量包订单 planID 为 nil。
func checkPlan(planIDs []uuid.UUID, planID *uuid.UUID) error {
	if len(planIDs) == 0 {
		return nil
	}
	if planID == nil {
		return ErrCouponNotForPlan
	}
	for _, id := range planIDs {
		if id == *planID {
			return nil
		}
	}
	return ErrCouponNotForPlan
}

func discountFor(f Face, maxDiscount *decimal.Decimal, subtotal decimal.Decimal) decimal.Decimal {
	var d decimal.Decimal
	switch f.Kind {
	case KindPercent:
		d = money.ApplyBP(subtotal, f.PercentBP)
		if maxDiscount != nil && d.GreaterThan(*maxDiscount) {
			d = *maxDiscount
		}
	default:
		d = f.AmountOff
	}
	if d.GreaterThan(subtotal) {
		d = subtotal
	}
	if d.IsNegative() {
		d = decimal.Zero
	}
	return d
}

// CanTransition 报告占用状态是否允许转换。
func CanTransition(from, to string) bool {
	if m, ok := redemptionTransitions[from]; ok {
		return m[to]
	}
	return false
}

func checkTarget(t Target) error {
	// 币种必须已是规范的大写代码：券的币种按原样比较
	if cur, err := money.ParseCurrency(t.Currency); err != nil || string(cur) != t.Currency {
		return errs.Wrap(errs.Internal, fmt.Errorf("promo target: 币种 %q 不规范", t.Currency))
	}
	if err := money.CheckNonNegative(t.Subtotal); err != nil {
		return errs.Wrap(errs.Internal, fmt.Errorf("promo target: %w", err))
	}
	if t.UserID == uuid.Nil {
		return errs.Wrap(errs.Internal, errors.New("promo target: user_id 不能为空"))
	}
	return nil
}

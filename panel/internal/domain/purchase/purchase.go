// Package purchase 是购买模型统一（2026-10-07 定稿）的规则唯一出处：落点选项与默认值、
// 备注名规范化、余额与支付最低额的计算。
//
// 只依赖标准库：不跑 SQL、不 import 任何业务域。billing（报价、建单、后台开单）、
// giftcard（经 Granter 接口）和 subscription（备注名）都调这里，门户的金额与默认值
// 一律由服务端用这些函数算好下发，前端不再自己算钱。
//
// 错误只回本包的哨兵错误，文案是中文、可以直接给用户看；翻成 httpx 错误由调用方做。
package purchase

import (
	"errors"
	"time"
)

// State 是候选订阅在落点规则里的状态。
type State string

const (
	// StateLive 生效中（active / trialing / grace / past_due）
	StateLive State = "live"
	// StateRevivable 已过期但原地续费窗口没关（过期 30 天内，renewal_closed_at 为空）
	StateRevivable State = "revivable"
	// StateDead 彻底停用（cancelled，或过期且窗口已关）；不出现在任何选项里
	StateDead State = "dead"
)

// Candidate 是一份可能的落点订阅，由 billing 用一条 SQL 取齐。
type Candidate struct {
	SubscriptionID, PlanID, PlanName string
	// Label 是用户起的备注名，没起为空
	Label     string
	State     State
	PeriodEnd *time.Time
	// TrafficUsed 本期套餐流量已用（字节）
	TrafficUsed int64
	// TrafficCap 本期套餐流量上限（字节），0 表示不限
	TrafficCap int64
	// PackRemaining 挂在这一份上的流量包余量（字节）
	PackRemaining int64
}

// OfferKind 是要落地的东西是什么。
type OfferKind string

const (
	OfferPlan    OfferKind = "plan"    // 套餐卡、后台开单
	OfferDays    OfferKind = "days"    // 加时长卡
	OfferReset   OfferKind = "reset"   // 流量重置卡
	OfferTraffic OfferKind = "traffic" // 送流量、购买流量包
	OfferMixed   OfferKind = "mixed"   // 普通卡多项奖励、盲盒：多项奖励共用一个落点
)

// Offer 描述一次要落地的东西。PlanID 只对 OfferPlan 有意义；Has* 只对 OfferMixed 有意义。
// PriceID 与 Days 不影响选项，只用来算每个选项「之后到哪天」（Placement.NewPeriodEnd）。
type Offer struct {
	Kind                          OfferKind
	PlanID                        string
	HasDays, HasReset, HasTraffic bool
	PriceID                       string
	Days                          int
}

// Kind 是一个选项的动作。
type Kind string

const (
	KindRenew        Kind = "renew"         // 续到同款那份
	KindChange       Kind = "change"        // 把那份换成 P（Expired 时是「恢复并改成 P」）
	KindNew          Kind = "new"           // 另开一份
	KindExtendDays   Kind = "extend_days"   // 加时长
	KindResetTraffic Kind = "reset_traffic" // 本期流量清零重算
	KindAddTraffic   Kind = "add_traffic"   // 加流量
)

// 徽标：前端按它显示「同款，最常见」「最快到期」「用得最多」「剩得最少」。
const (
	BadgeSamePlan       = "same_plan"
	BadgeSoonestExpiry  = "soonest_expiry"
	BadgeMostUsed       = "most_used"
	BadgeLeastRemaining = "least_remaining"
)

// Option 是一个可选的落点。Key 前后端同一个：new 是 "new"，其余是 "<kind>:<订阅 ID>"。
type Option struct {
	Key            string `json:"key"`
	Kind           Kind   `json:"kind"`
	SubscriptionID string `json:"subscription_id,omitempty"`
	// Expired 只用在 change 上：目标是过期 30 天内的那份，即「恢复并改成 P」
	Expired bool   `json:"expired,omitempty"`
	Badge   string `json:"badge,omitempty"`
}

// Choice 是用户（或管理员）选定的落点。
type Choice struct {
	Kind           Kind   `json:"kind"`
	SubscriptionID string `json:"subscription_id,omitempty"`
}

// OptionKey 按选项规则拼 Key。
func OptionKey(kind Kind, subscriptionID string) string {
	if kind == KindNew {
		return string(KindNew)
	}
	return string(kind) + ":" + subscriptionID
}

// ErrChoiceStale 是选定的落点不在当前选项里：订阅状态在报价之后变了，或请求被篡改。
// 调用方翻成 422。
var ErrChoiceStale = errors.New("这个用法现在不能用了，请刷新后再选")

// ErrChoiceRequired 是选项多于一个却没有给选择。调用方翻成 422。
var ErrChoiceRequired = errors.New("请先选一种用法")

// Match 在当前选项里找到与选择对应的那一项；找不到回 ErrChoiceStale。
func (c Choice) Match(opts []Option) (Option, error) {
	key := OptionKey(c.Kind, c.SubscriptionID)
	for _, o := range opts {
		if o.Key == key {
			return o, nil
		}
	}
	return Option{}, ErrChoiceStale
}

// Resolve 是兑换与开单共用的「选定落点」：给了选择就 Match；没给且只有一个选项就用它；
// 没给且有多个选项回 ErrChoiceRequired；一个选项都没有回 ok=false（调用方按场景处理：
// 送流量记为未分配，其余拒绝）。
func Resolve(choice *Choice, opts []Option) (opt Option, ok bool, err error) {
	if choice != nil && choice.Kind != "" {
		o, err := choice.Match(opts)
		if err != nil {
			return Option{}, false, err
		}
		return o, true, nil
	}
	switch len(opts) {
	case 0:
		return Option{}, false, nil
	case 1:
		return opts[0], true, nil
	default:
		return Option{}, false, ErrChoiceRequired
	}
}

// Placement 是一个落点选项连同它的「会发生什么」：门户兑换卡、后台开单 preview 共用的展示结构。
// 字段全是基本类型，giftcard 经 Granter 接口拿到它，不必 import billing。
type Placement struct {
	Option
	// 这一份现在的样子；new 选项这几项为空
	Label     string     `json:"label,omitempty"`
	PlanID    string     `json:"plan_id,omitempty"`
	PlanName  string     `json:"plan_name,omitempty"`
	State     State      `json:"state,omitempty"`
	PeriodEnd *time.Time `json:"period_end,omitempty"`
	// NewPeriodEnd 落地之后的到期日（续一期、加时长、换套餐与新开都会给；重置与加流量不变，为空）
	NewPeriodEnd *time.Time `json:"new_period_end,omitempty"`
	// Credit 只用在 change 上：原套餐没用完的部分（剩余价值）。套餐卡与后台赠送全额退到余额
	Credit   int64  `json:"credit,omitempty"`
	Currency string `json:"currency,omitempty"`
	// 本期套餐流量与挂在这一份上的流量包余量（字节），重置与加流量的说明要用
	TrafficUsed   int64 `json:"traffic_used,omitempty"`
	TrafficCap    int64 `json:"traffic_cap,omitempty"`
	PackRemaining int64 `json:"pack_remaining,omitempty"`
}

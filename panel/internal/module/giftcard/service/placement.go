package service

// 落点：这张卡落到哪一份订阅由人选（购买模型统一，2026-10-07 定稿）。本文件只有纯函数：
// 卡要落地的是什么（offerOf）、问题怎么问（placementQuestion）、给哪些选项与默认哪一个（placementOptions）。
// 候选由 subscription 的 ByUser 给出，状态口径只用 subscription 的 State。

import (
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
)

// 选项的动作（Option.Kind），与前端 PLACEMENT_KINDS 一致。
const (
	KindRenew        = "renew"         // 续到同款那份
	KindStack        = "stack"         // 叠加到同款的一次性那份（总流量加上、天数顺延）
	KindChange       = "change"        // 把那份换成卡上的套餐（Expired 时是「恢复并改成」）
	KindNew          = "new"           // 另开一份
	KindExtendDays   = "extend_days"   // 加时长
	KindResetTraffic = "reset_traffic" // 本期流量清零重算
	KindAddTraffic   = "add_traffic"   // 加流量
)

// 徽标：前端按它显示「同款，最常见」「最快到期」「用得最多」「剩得最少」。
const (
	BadgeSamePlan       = "same_plan"
	BadgeSoonestExpiry  = "soonest_expiry"
	BadgeMostUsed       = "most_used"
	BadgeLeastRemaining = "least_remaining"
)

// 候选在落点规则里的状态（与前端 state 枚举一致）。
const (
	StateLive      = "live"
	StateRevivable = "revivable"
	StateDead      = "dead"
)

// offerKind 是要落地的东西是什么。
type offerKind int

const (
	offerNone    offerKind = iota // 不需要落点（纯余额卡、盲盒里全是余额）
	offerPlan                     // 套餐卡
	offerDays                     // 加时长
	offerReset                    // 流量重置
	offerTraffic                  // 送流量
	offerMixed                    // 多项奖励、盲盒：共用一个落点
)

// offer 描述一次要落地的东西。PlanID、PlanKind 只对 offerPlan 有意义；has* 只对 offerMixed 有意义；
// Days 只用来算「之后到哪天」；Mystery 为真时不排除永久 / 不限流量的订阅。
type offer struct {
	Kind                          offerKind
	PlanID                        uuid.UUID
	PlanKind                      string
	Mystery                       bool
	HasDays, HasReset, HasTraffic bool
	Days                          int
}

// noTargetErr 报告没有选到落点时的对外错误。
func (o offer) noTargetErr() error {
	if o.Kind == offerTraffic || (o.Kind == offerMixed && o.HasTraffic && !o.HasDays && !o.HasReset) {
		return ErrNoTrafficTarget
	}
	return ErrNoPlacement
}

// candidate 是一份可能的落点订阅。
type candidate struct {
	SubscriptionID uuid.UUID
	PlanID         uuid.UUID
	PlanName       string
	Label          string
	State          string
	PeriodEnd      time.Time
	Permanent      bool
	Unlimited      bool
	// TrafficUsed / TrafficCap 本期套餐流量已用与上限（字节），上限 0 表示不限
	TrafficUsed int64
	TrafficCap  int64
}

// Option 是一个可选的落点。Key 前后端同一个：new 是 "new"，其余是 "<kind>:<订阅 ID>"。
type Option struct {
	Key            string
	Kind           string
	SubscriptionID *uuid.UUID
	// Expired 只用在 change 上：目标是过期 30 天内的那份，即「恢复并改成」
	Expired bool
	Badge   string
}

// Placement 是一个落点选项连同它「会发生什么」。new 选项没有「这一份现在的样子」。
type Placement struct {
	Option
	Label        string
	PlanID       *uuid.UUID
	PlanName     string
	State        string
	PeriodEnd    *time.Time
	NewPermanent bool
	// NewPeriodEnd 落地之后的到期日（续一期、加时长、换套餐与新开会给；重置、加流量与 NewPermanent 为真时为 nil）
	NewPeriodEnd *time.Time
	// Credit、GiftDaysLost 只用在 change 上：原套餐没用完的已付价值（全额退进余额）与不保留的赠送天数
	Credit       decimal.Decimal
	GiftDaysLost int
	TrafficUsed  int64
	TrafficCap   int64
}

// optionKey 按选项规则拼 Key。
func optionKey(kind string, subID uuid.UUID) string {
	if kind == KindNew {
		return KindNew
	}
	return kind + ":" + subID.String()
}

// choiceKey 是用户选择对应的 Key；Kind 为空返回空串。
func choiceKey(c *Choice) string {
	if c == nil || c.Kind == "" {
		return ""
	}
	if c.Kind == KindNew {
		return KindNew
	}
	if c.SubscriptionID == nil {
		return c.Kind + ":"
	}
	return optionKey(c.Kind, *c.SubscriptionID)
}

// resolveChoice 是兑换时的「选定落点」：给了选择就在当前选项里找（找不到回 ErrChoiceStale）；
// 没给且只有一个选项就用它；没给且有多个回 ErrChoiceRequired；一个选项都没有回 ok=false。
func resolveChoice(choice *Choice, opts []Option) (Option, bool, error) {
	if key := choiceKey(choice); key != "" {
		for _, o := range opts {
			if o.Key == key {
				return o, true, nil
			}
		}
		return Option{}, false, ErrChoiceStale
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

// offerOf 算一个模板要落地的是什么。盲盒按奖池里可能抽到的奖励算：先选好落点，抽中什么就落到那一份。
func offerOf(t Template, planKind string) offer {
	r := t.Rewards
	switch t.Type {
	case TypePlan:
		if r.PlanID == nil {
			return offer{}
		}
		return offer{Kind: offerPlan, PlanID: *r.PlanID, PlanKind: planKind}
	case TypeMystery:
		var days, traffic bool
		minDays := 0
		for _, p := range r.Pool {
			if p.ExpireDays > 0 {
				days = true
				if minDays == 0 || p.ExpireDays < minDays {
					minDays = p.ExpireDays
				}
			}
			traffic = traffic || p.TrafficBytes > 0
		}
		if !days && !traffic {
			return offer{}
		}
		return offer{Kind: offerMixed, Mystery: true, HasDays: days, HasTraffic: traffic, Days: minDays}
	}
	days, reset, traffic := r.ExpireDays > 0, r.ResetQuota, r.TrafficBytes > 0
	n := 0
	for _, b := range []bool{days, reset, traffic} {
		if b {
			n++
		}
	}
	switch {
	case n == 0:
		return offer{}
	case n > 1:
		return offer{Kind: offerMixed, HasDays: days, HasReset: reset, HasTraffic: traffic, Days: r.ExpireDays}
	case days:
		return offer{Kind: offerDays, Days: r.ExpireDays}
	case reset:
		return offer{Kind: offerReset}
	default:
		return offer{Kind: offerTraffic}
	}
}

// placementQuestion 是兑换页上那一句问题。
func placementQuestion(o offer) string {
	switch o.Kind {
	case offerPlan:
		return "怎么用这张卡？"
	case offerReset:
		return "重算哪一份？"
	case offerMixed:
		return "用在哪一份？"
	default:
		return "加到哪一份？"
	}
}

// candidateState 把 subscription 的状态口径映射成落点规则的三档。
func candidateState(st subsvc.State) string {
	switch st {
	case subsvc.StateLive:
		return StateLive
	case subsvc.StateRevivable:
		return StateRevivable
	default:
		return StateDead
	}
}

// candidatesOf 把用户的订阅摘要转成候选：本期流量取 traffic.bytes 的 cycle / total 配额行（不限量的上限记 0）。
func candidatesOf(us subsvc.UserSubscriptions) []candidate {
	out := make([]candidate, 0, len(us.Subscriptions))
	for _, s := range us.Subscriptions {
		c := candidate{
			SubscriptionID: s.ID,
			PlanID:         s.PlanID,
			PlanName:       s.PlanName,
			State:          candidateState(s.State),
			PeriodEnd:      s.PeriodEnd,
			Permanent:      subsvc.IsPermanent(s.PeriodEnd),
			Unlimited:      true,
		}
		if s.Label != nil {
			c.Label = *s.Label
		}
		for _, q := range s.Quotas {
			if q.Metric != subsvc.MetricTraffic || (q.Period != subsvc.PeriodCycle && q.Period != subsvc.PeriodTotal) || q.Limit == nil {
				continue
			}
			c.Unlimited = false
			c.TrafficUsed += q.Consumed
			c.TrafficCap += *q.Limit
		}
		out = append(out, c)
	}
	return out
}

// placementOptions 给出一次落地的全部选项与默认选中的 Key（空串表示不预选）。规则：
//
//	套餐卡      订阅套餐：同款可续（生效中按到期从早到晚，再过期 30 天内按到期从晚到早）→ 不同款过期 30 天内（恢复并改成）→
//	            新开一份 → 不同款生效中（换掉）。默认第一个同款；只剩新开就新开；否则不预选。
//	            只有一份而且就是同款时只返回这一项。换掉生效中的那份永不自动默认。allowChange 为假时不给「换掉」。
//	            一次性套餐：同款生效中叠加（按到期从早到晚）→ 不同款过期 30 天内（恢复并改成）→
//	            新开一份 → 不同款生效中（换掉）。默认第一个叠加；只剩新开就新开；否则不预选。不套用只有一份同款就只给这一项。
//	加时长      生效中按到期从早到晚，再接过期 30 天内的；排除永久；默认第一个（最快到期）。
//	流量重置    只列生效中的；默认本期用量比例最高的（用得最多）。
//	加流量      只列生效中的；排除不限流量；默认剩余最少的（剩得最少）。
//	多项奖励    同一个落点；含加时长按加时长取（与重置或流量同卡时只列生效中的），否则含重置按重置取，否则按加流量取。
//	盲盒奖品    不排除永久 / 不限流量的订阅，发放时跳过不适用的奖励。
//
// 已彻底停用的订阅不出现在任何选项里。
func placementOptions(o offer, cands []candidate, allowChange bool) ([]Option, string) {
	if o.Kind == offerPlan {
		return planOptions(o.PlanID, o.PlanKind, cands, allowChange)
	}
	if !o.Mystery {
		hasDays := o.Kind == offerDays || (o.Kind == offerMixed && o.HasDays)
		hasTraffic := o.Kind == offerTraffic || (o.Kind == offerMixed && o.HasTraffic)
		filtered := make([]candidate, 0, len(cands))
		for _, c := range cands {
			if hasDays && c.Permanent {
				continue
			}
			if hasTraffic && c.Unlimited {
				continue
			}
			filtered = append(filtered, c)
		}
		cands = filtered
	}
	switch o.Kind {
	case offerDays:
		return daysOptions(cands)
	case offerReset:
		return resetOptions(cands)
	case offerTraffic:
		return trafficOptions(cands)
	case offerMixed:
		switch {
		case o.HasDays && !o.HasReset && !o.HasTraffic:
			return daysOptions(cands)
		case o.HasDays:
			return daysOptions(filterState(cands, StateLive))
		case o.HasReset:
			return resetOptions(cands)
		case o.HasTraffic:
			return trafficOptions(cands)
		}
	}
	return nil, ""
}

func filterState(cands []candidate, state string) []candidate {
	var out []candidate
	for _, c := range cands {
		if c.State == state {
			out = append(out, c)
		}
	}
	return out
}

// byEndAsc 按到期从早到晚，同到期按订阅 ID 定序，结果与输入次序无关。
func byEndAsc(cs []candidate) []candidate {
	out := append([]candidate(nil), cs...)
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].PeriodEnd.Equal(out[j].PeriodEnd) {
			return out[i].PeriodEnd.Before(out[j].PeriodEnd)
		}
		return out[i].SubscriptionID.String() < out[j].SubscriptionID.String()
	})
	return out
}

func byEndDesc(cs []candidate) []candidate {
	out := byEndAsc(cs)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func optionFor(kind string, c candidate) Option {
	id := c.SubscriptionID
	return Option{Key: optionKey(kind, id), Kind: kind, SubscriptionID: &id}
}

func planOptions(planID uuid.UUID, planKind string, cands []candidate, allowChange bool) ([]Option, string) {
	var same, other []candidate
	alive := 0
	for _, c := range cands {
		if c.State == StateDead {
			continue
		}
		alive++
		if c.PlanID == planID {
			same = append(same, c)
		} else {
			other = append(other, c)
		}
	}
	if planKind == catalogsvc.KindOneTime {
		var opts []Option
		for _, c := range byEndAsc(filterState(same, StateLive)) {
			opts = append(opts, optionFor(KindStack, c))
		}
		stacks := len(opts)
		if allowChange {
			for _, c := range byEndDesc(filterState(other, StateRevivable)) {
				op := optionFor(KindChange, c)
				op.Expired = true
				opts = append(opts, op)
			}
		}
		opts = append(opts, Option{Key: KindNew, Kind: KindNew})
		if allowChange {
			for _, c := range byEndAsc(filterState(other, StateLive)) {
				opts = append(opts, optionFor(KindChange, c))
			}
		}
		switch {
		case stacks > 0:
			opts[0].Badge = BadgeSamePlan
			return opts, opts[0].Key
		case len(opts) == 1:
			return opts, opts[0].Key
		default:
			return opts, ""
		}
	}

	var opts []Option
	for _, c := range byEndAsc(filterState(same, StateLive)) {
		opts = append(opts, optionFor(KindRenew, c))
	}
	for _, c := range byEndDesc(filterState(same, StateRevivable)) {
		opts = append(opts, optionFor(KindRenew, c))
	}
	renews := len(opts)
	// 只有一份、而且就是同款：不问，直接续
	if alive == 1 && renews == 1 {
		return opts, opts[0].Key
	}
	if allowChange {
		for _, c := range byEndDesc(filterState(other, StateRevivable)) {
			op := optionFor(KindChange, c)
			op.Expired = true
			opts = append(opts, op)
		}
	}
	opts = append(opts, Option{Key: KindNew, Kind: KindNew})
	if allowChange {
		for _, c := range byEndAsc(filterState(other, StateLive)) {
			opts = append(opts, optionFor(KindChange, c))
		}
	}
	switch {
	case renews > 0:
		opts[0].Badge = BadgeSamePlan
		return opts, opts[0].Key
	case len(opts) == 1:
		return opts, opts[0].Key
	default:
		// 不同款一律不预选：让人自己选「另开一份」还是「换掉哪一份」
		return opts, ""
	}
}

func daysOptions(cands []candidate) ([]Option, string) {
	var opts []Option
	for _, c := range byEndAsc(filterState(cands, StateLive)) {
		opts = append(opts, optionFor(KindExtendDays, c))
	}
	for _, c := range byEndAsc(filterState(cands, StateRevivable)) {
		opts = append(opts, optionFor(KindExtendDays, c))
	}
	if len(opts) == 0 {
		return nil, ""
	}
	opts[0].Badge = BadgeSoonestExpiry
	return opts, opts[0].Key
}

// usedMore 比较两份的本期用量比例（a 是否严格高于 b），交叉相乘不做除法；不限量的那份按 0 算。
func usedMore(a, b candidate) bool {
	ua, ca := a.TrafficUsed, a.TrafficCap
	ub, cb := b.TrafficUsed, b.TrafficCap
	if ca <= 0 {
		ua, ca = 0, 1
	}
	if cb <= 0 {
		ub, cb = 0, 1
	}
	// ua/ca > ub/cb ⇔ ua*cb > ub*ca；用 decimal 防溢出（字节数相乘会超过 int64）
	return decimal.NewFromInt(ua).Mul(decimal.NewFromInt(cb)).GreaterThan(decimal.NewFromInt(ub).Mul(decimal.NewFromInt(ca)))
}

func resetOptions(cands []candidate) ([]Option, string) {
	live := byEndAsc(filterState(cands, StateLive))
	if len(live) == 0 {
		return nil, ""
	}
	best := 0
	for i, c := range live {
		if usedMore(c, live[best]) {
			best = i
		}
	}
	opts := make([]Option, 0, len(live))
	for _, c := range live {
		opts = append(opts, optionFor(KindResetTraffic, c))
	}
	if len(opts) > 1 {
		opts[best].Badge = BadgeMostUsed
	}
	return opts, opts[best].Key
}

// remaining 是这一份还能用的流量：套餐本期剩余；不限量的那份最大。
func remaining(c candidate) int64 {
	if c.Unlimited || c.TrafficCap <= 0 {
		return 1<<63 - 1
	}
	left := c.TrafficCap - c.TrafficUsed
	if left < 0 {
		left = 0
	}
	return left
}

func trafficOptions(cands []candidate) ([]Option, string) {
	live := byEndAsc(filterState(cands, StateLive))
	if len(live) == 0 {
		return nil, ""
	}
	best := 0
	for i, c := range live {
		if remaining(c) < remaining(live[best]) {
			best = i
		}
	}
	opts := make([]Option, 0, len(live))
	for _, c := range live {
		opts = append(opts, optionFor(KindAddTraffic, c))
	}
	if len(opts) > 1 {
		opts[best].Badge = BadgeLeastRemaining
	}
	return opts, opts[best].Key
}

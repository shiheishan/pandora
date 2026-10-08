package purchase

import (
	"sort"
	"time"
)

// Options 给出一次落地的全部选项与默认选中的 Key（空串表示不预选，前端按钮置灰
// 「先选一种用法」）。entrySub 是入口上下文（后台从订阅行进来、门户从卡片进来），
// 它对应的选项优先作为默认。
//
// 规则原样实现原型 redeemOptions 与 Q4、Q8（设计稿 2.1）：
//
//	套餐卡、后台开单（P）  同款可续（生效中按到期从早到晚，再过期 30 天内按到期从晚到早）→
//	                      不同款过期 30 天内（恢复并改成 P）→ 新开一份 → 不同款生效中（换掉）。
//	                      默认：入口那份；否则第一个同款；否则只剩新开就新开；否则不预选。
//	                      只有一份而且就是同款时只返回这一项。换掉生效中的那份永不自动默认。
//	加时长卡              生效中按到期从早到晚，再接过期 30 天内的；默认第一个（最快到期）。
//	流量重置卡            只列生效中的；默认本期用量比例最高的（用得最多）。
//	加流量                只列生效中的；默认剩余最少的（剩得最少）。一份都没有时返回空。
//	多项奖励              同一个落点；含加时长按加时长取，否则含重置按重置取，否则按加流量取。
//	                      候选取各项奖励都能落的交集（含重置或流量时只列生效中的）。
//
// 已彻底停用（StateDead）的订阅不出现在任何选项里。
func Options(o Offer, cands []Candidate, entrySub string) ([]Option, string) {
	var opts []Option
	var def string
	switch o.Kind {
	case OfferPlan:
		opts, def = planOptions(o.PlanID, cands)
	case OfferDays:
		opts, def = daysOptions(cands)
	case OfferReset:
		opts, def = resetOptions(cands)
	case OfferTraffic:
		opts, def = trafficOptions(cands)
	case OfferMixed:
		switch {
		case o.HasDays && !o.HasReset && !o.HasTraffic:
			opts, def = daysOptions(cands)
		case o.HasDays:
			// 加时长与重置 / 流量同卡：候选只能是生效中的，按加时长的次序与默认取
			opts, def = daysOptions(filterState(cands, StateLive))
		case o.HasReset:
			opts, def = resetOptions(cands)
		case o.HasTraffic:
			opts, def = trafficOptions(cands)
		}
	}
	if entrySub != "" {
		for _, op := range opts {
			if op.SubscriptionID == entrySub {
				def = op.Key
				break
			}
		}
	}
	return opts, def
}

func filterState(cands []Candidate, states ...State) []Candidate {
	var out []Candidate
	for _, c := range cands {
		for _, s := range states {
			if c.State == s {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

// endOf 把没有到期日（永不过期）的排到最后。
func endOf(c Candidate) time.Time {
	if c.PeriodEnd == nil {
		return time.Unix(1<<62, 0)
	}
	return *c.PeriodEnd
}

// byEndAsc 按到期从早到晚，同到期按订阅 ID 定序，结果与输入次序无关。
func byEndAsc(cs []Candidate) []Candidate {
	out := append([]Candidate(nil), cs...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := endOf(out[i]), endOf(out[j])
		if !a.Equal(b) {
			return a.Before(b)
		}
		return out[i].SubscriptionID < out[j].SubscriptionID
	})
	return out
}

func byEndDesc(cs []Candidate) []Candidate {
	out := byEndAsc(cs)
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func option(kind Kind, c Candidate) Option {
	return Option{Key: OptionKey(kind, c.SubscriptionID), Kind: kind, SubscriptionID: c.SubscriptionID}
}

func planOptions(planID string, cands []Candidate) ([]Option, string) {
	var same, other []Candidate
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
	var opts []Option
	for _, c := range byEndAsc(filterState(same, StateLive)) {
		opts = append(opts, option(KindRenew, c))
	}
	for _, c := range byEndDesc(filterState(same, StateRevivable)) {
		opts = append(opts, option(KindRenew, c))
	}
	renews := len(opts)
	// 只有一份、而且就是同款：不问，直接续
	if alive == 1 && renews == 1 {
		return opts, opts[0].Key
	}
	for _, c := range byEndDesc(filterState(other, StateRevivable)) {
		op := option(KindChange, c)
		op.Expired = true
		opts = append(opts, op)
	}
	opts = append(opts, Option{Key: string(KindNew), Kind: KindNew})
	for _, c := range byEndAsc(filterState(other, StateLive)) {
		opts = append(opts, option(KindChange, c))
	}
	switch {
	case renews > 0:
		opts[0].Badge = BadgeSamePlan
		return opts, opts[0].Key
	case len(opts) == 1:
		return opts, opts[0].Key
	default:
		// 不同款一律不预选（10-07 补充确认）：让人自己选「另开一份」还是「换掉哪一份」
		return opts, ""
	}
}

func daysOptions(cands []Candidate) ([]Option, string) {
	var opts []Option
	for _, c := range byEndAsc(filterState(cands, StateLive)) {
		opts = append(opts, option(KindExtendDays, c))
	}
	for _, c := range byEndAsc(filterState(cands, StateRevivable)) {
		opts = append(opts, option(KindExtendDays, c))
	}
	if len(opts) == 0 {
		return nil, ""
	}
	opts[0].Badge = BadgeSoonestExpiry
	return opts, opts[0].Key
}

// usedRatio 本期用量比例；不限量的那份按 0 算（清零对它没有意义）。
func usedRatio(c Candidate) float64 {
	if c.TrafficCap <= 0 {
		return 0
	}
	return float64(c.TrafficUsed) / float64(c.TrafficCap)
}

func resetOptions(cands []Candidate) ([]Option, string) {
	live := byEndAsc(filterState(cands, StateLive))
	if len(live) == 0 {
		return nil, ""
	}
	best := 0
	for i, c := range live {
		if usedRatio(c) > usedRatio(live[best]) {
			best = i
		}
	}
	opts := make([]Option, 0, len(live))
	for _, c := range live {
		opts = append(opts, option(KindResetTraffic, c))
	}
	if len(opts) > 1 {
		opts[best].Badge = BadgeMostUsed
	}
	return opts, opts[best].Key
}

// remaining 是这一份还能用的流量：套餐本期剩余加挂在它上面的流量包；不限量的那份最大。
func remaining(c Candidate) int64 {
	if c.TrafficCap <= 0 {
		return 1<<63 - 1
	}
	left := c.TrafficCap - c.TrafficUsed
	if left < 0 {
		left = 0
	}
	return left + c.PackRemaining
}

func trafficOptions(cands []Candidate) ([]Option, string) {
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
		opts = append(opts, option(KindAddTraffic, c))
	}
	if len(opts) > 1 {
		opts[best].Badge = BadgeLeastRemaining
	}
	return opts, opts[best].Key
}

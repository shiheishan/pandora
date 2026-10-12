package service

// 后台开单「这单落到哪一份」（对照旧 purchase.Options 的套餐卡 / 后台开单分支与 placement_candidates.go）：
//
//	同款可续（生效中按到期从早到晚，再可救回的按到期从晚到早）→ 不同款可救回的（恢复并改成它）→ 新开一份 →
//	不同款生效中的（换掉）。
//	默认：入口那份；否则第一个同款；否则只剩新开就新开；否则不预选。只有一份而且就是同款时只返回这一项。
//	换掉生效中的那份永不自动默认。已停用的订阅不出现在任何选项里。

import (
	"sort"
	"time"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// maxCandidates 是一个用户参与落点的订阅上限（生效中的优先、新的优先）。
const maxCandidates = 50

// 选落点的两种 422（文案沿用旧代码，落在 target 上）。
var (
	errChoiceRequired = errs.Invalid(map[string]string{"target": "请选择这单落到哪一份"})
	errChoiceStale    = errs.Invalid(map[string]string{"target": "这个用法现在不能用了，请刷新后再选"})
)

// candidate 是一份可能的落点订阅。
type candidate struct {
	Sub      subsvc.Subscription
	PlanName string
	State    subsvc.State
	// TrafficUsed、TrafficCap 是本期套餐流量（cycle / total 的 traffic.bytes，不限量的上限记 0）
	TrafficUsed   int64
	TrafficCap    int64
	PackRemaining int64
}

// candidatesFrom 从用户的订阅摘要里取还能落地的（未停用的），生效中的优先、新的优先，最多 maxCandidates 份。
func candidatesFrom(us subsvc.UserSubscriptions) []candidate {
	out := make([]candidate, 0, len(us.Subscriptions))
	for _, sum := range us.Subscriptions {
		if sum.State == subsvc.StateEnded {
			continue
		}
		c := candidate{Sub: sum.Subscription, PlanName: sum.PlanName, State: sum.State, PackRemaining: sum.PackRemainingBytes}
		for _, q := range sum.Quotas {
			if q.Metric != catalogsvc.MetricTraffic || q.Limit == nil ||
				(q.Period != catalogsvc.PeriodCycle && q.Period != catalogsvc.PeriodTotal) {
				continue
			}
			c.TrafficUsed += q.Consumed
			c.TrafficCap += *q.Limit
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		li, lj := out[i].State == subsvc.StateLive, out[j].State == subsvc.StateLive
		if li != lj {
			return li
		}
		if !out[i].Sub.CreatedAt.Equal(out[j].Sub.CreatedAt) {
			return out[i].Sub.CreatedAt.After(out[j].Sub.CreatedAt)
		}
		return out[i].Sub.ID.String() < out[j].Sub.ID.String()
	})
	if len(out) > maxCandidates {
		out = out[:maxCandidates]
	}
	return out
}

// placementKey 按选项规则拼 Key：new 是 "new"，其余是 "<kind>:<订阅 ID>"。
func placementKey(kind string, subID *uuid.UUID) string {
	if kind == PlacementNew || subID == nil {
		return PlacementNew
	}
	return kind + ":" + subID.String()
}

// placementOf 是落到某一份的选项，带上这一份现在的样子。
func placementOf(kind string, c candidate) Placement {
	id, planID, end := c.Sub.ID, c.Sub.PlanID, c.Sub.PeriodEnd
	label := ""
	if c.Sub.Label != nil {
		label = *c.Sub.Label
	}
	return Placement{
		Key:            placementKey(kind, &id),
		Kind:           kind,
		SubscriptionID: &id,
		Label:          label,
		PlanID:         &planID,
		PlanName:       c.PlanName,
		State:          c.State,
		PeriodEnd:      &end,
		TrafficUsed:    c.TrafficUsed,
		TrafficCap:     c.TrafficCap,
		PackRemaining:  c.PackRemaining,
	}
}

// byEnd 按到期从早到晚（desc 为真时从晚到早），同到期按订阅 ID 定序，结果与输入次序无关。
func byEnd(cs []candidate, desc bool) []candidate {
	out := append([]candidate(nil), cs...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].Sub.PeriodEnd, out[j].Sub.PeriodEnd
		if !a.Equal(b) {
			return a.Before(b) != desc
		}
		return (out[i].Sub.ID.String() < out[j].Sub.ID.String()) != desc
	})
	return out
}

func filterState(cs []candidate, st subsvc.State) []candidate {
	var out []candidate
	for _, c := range cs {
		if c.State == st {
			out = append(out, c)
		}
	}
	return out
}

// planPlacements 给出开单 planID 的全部落点与默认 Key（空串表示不预选）。entrySub 是后台从哪一行订阅点进来的，
// 它对应的选项优先作为默认。
func planPlacements(planID uuid.UUID, cands []candidate, entrySub *uuid.UUID) ([]Placement, string) {
	var same, other []candidate
	alive := 0
	for _, c := range cands {
		if c.State == subsvc.StateEnded {
			continue
		}
		alive++
		if c.Sub.PlanID == planID {
			same = append(same, c)
		} else {
			other = append(other, c)
		}
	}
	var opts []Placement
	for _, c := range byEnd(filterState(same, subsvc.StateLive), false) {
		opts = append(opts, placementOf(PlacementRenew, c))
	}
	for _, c := range byEnd(filterState(same, subsvc.StateRevivable), true) {
		opts = append(opts, placementOf(PlacementRenew, c))
	}
	renews := len(opts)
	// 只有一份、而且就是同款：不问，直接续
	if alive == 1 && renews == 1 {
		return withEntry(opts, opts[0].Key, entrySub)
	}
	for _, c := range byEnd(filterState(other, subsvc.StateRevivable), true) {
		op := placementOf(PlacementChange, c)
		op.Expired = true
		opts = append(opts, op)
	}
	opts = append(opts, Placement{Key: PlacementNew, Kind: PlacementNew})
	for _, c := range byEnd(filterState(other, subsvc.StateLive), false) {
		opts = append(opts, placementOf(PlacementChange, c))
	}
	def := ""
	switch {
	case renews > 0:
		opts[0].Badge = BadgeSamePlan
		def = opts[0].Key
	case len(opts) == 1:
		def = opts[0].Key
	}
	// 不同款一律不预选：让人自己选「另开一份」还是「换掉哪一份」
	return withEntry(opts, def, entrySub)
}

// withEntry 让入口那份的选项成为默认（有的话）。
func withEntry(opts []Placement, def string, entrySub *uuid.UUID) ([]Placement, string) {
	if entrySub == nil {
		return opts, def
	}
	for _, op := range opts {
		if op.SubscriptionID != nil && *op.SubscriptionID == *entrySub {
			return opts, op.Key
		}
	}
	return opts, def
}

// resolvePlacement 是开单时的「选定落点」：给了选择就按 Key 找，找不到回 422（状态在预览之后变了）；
// 没给且只有一个选项就用它；没给且有多个选项回 422「请选择这单落到哪一份」。
func resolvePlacement(choice *Choice, opts []Placement) (Placement, error) {
	if choice != nil && choice.Kind != "" {
		key := placementKey(choice.Kind, choice.SubscriptionID)
		for _, op := range opts {
			if op.Key == key {
				return op, nil
			}
		}
		return Placement{}, errChoiceStale
	}
	if len(opts) == 1 {
		return opts[0], nil
	}
	return Placement{}, errChoiceRequired
}

// newPeriodEnd 是落地之后的到期日：续一期时生效中的接在原到期之后、可救回的从现在起算；换套餐与新开从现在起算。
func newPeriodEnd(op Placement, now time.Time, interval string, count int) time.Time {
	base := now
	if op.Kind == PlacementRenew && op.State == subsvc.StateLive && op.PeriodEnd != nil && op.PeriodEnd.After(now) {
		base = *op.PeriodEnd
	}
	return subsvc.AddInterval(base, interval, count)
}

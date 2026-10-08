package giftcard

// 兑换卡「用在哪一份」（购买模型统一 Q8，设计稿 2.5）：选项与默认值由计费域按 purchase.Options
// 给出（Granter.Placements），这里只决定这张卡要落地的是什么（Offer）、问题怎么问。

import (
	"errors"

	"github.com/aegispanel/aegis/internal/domain/purchase"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// cardOffer 是这张卡要落到某一份订阅上的东西；ok=false 表示不需要落点（纯余额卡、盲盒里
// 全是余额）。盲盒按奖池里可能抽到的奖励算：先选好落点，抽中什么就落到那一份。
func cardOffer(t Template) (purchase.Offer, bool) {
	r := t.Rewards
	if t.Type == "plan" {
		return purchase.Offer{Kind: purchase.OfferPlan, PlanID: r.PlanID, PriceID: r.PriceID}, true
	}
	var days, reset, traffic bool
	daysN := 0
	if t.Type == "mystery" {
		for _, p := range r.Pool {
			if p.ExpireDays > 0 {
				days = true
				if daysN == 0 || p.ExpireDays < daysN {
					daysN = p.ExpireDays
				}
			}
			traffic = traffic || p.TrafficBytes > 0
		}
		if !days && !traffic {
			return purchase.Offer{}, false
		}
		return purchase.Offer{Kind: purchase.OfferMixed, HasDays: days, HasTraffic: traffic, Days: daysN}, true
	}
	days, reset, traffic = r.ExpireDays > 0, r.ResetQuota, r.TrafficBytes > 0
	n := 0
	for _, b := range []bool{days, reset, traffic} {
		if b {
			n++
		}
	}
	switch {
	case n == 0:
		return purchase.Offer{}, false
	case n > 1:
		return purchase.Offer{Kind: purchase.OfferMixed, HasDays: days, HasReset: reset,
			HasTraffic: traffic, Days: r.ExpireDays}, true
	case days:
		return purchase.Offer{Kind: purchase.OfferDays, Days: r.ExpireDays}, true
	case reset:
		return purchase.Offer{Kind: purchase.OfferReset}, true
	default:
		return purchase.Offer{Kind: purchase.OfferTraffic}, true
	}
}

// placementQuestion 是兑换页上那一句问题（原型 redeemPanel）。
func placementQuestion(o purchase.Offer) string {
	switch o.Kind {
	case purchase.OfferPlan:
		return "怎么用这张卡？"
	case purchase.OfferReset:
		return "重算哪一份？"
	case purchase.OfferMixed:
		return "用在哪一份？"
	default:
		return "加到哪一份？"
	}
}

// optionsOf 取出展示结构里的选项。
func optionsOf(views []PlacementView) []purchase.Option {
	out := make([]purchase.Option, 0, len(views))
	for _, v := range views {
		out = append(out, v.Option)
	}
	return out
}

// resolveChoice 在兑换事务里按当前选项校验用户的选择：选项变了、或多于一个却没选，都回 422，
// 卡不会被用掉（事务回滚）。一个选项都没有时 ok=false：送流量记为未分配，其余由计费域拒绝。
func resolveChoice(views []PlacementView, choice *purchase.Choice) (purchase.Option, bool, error) {
	opt, ok, err := purchase.Resolve(choice, optionsOf(views))
	if errors.Is(err, purchase.ErrChoiceStale) || errors.Is(err, purchase.ErrChoiceRequired) {
		return purchase.Option{}, false, httpx.Invalid(map[string]string{"choice": err.Error()})
	}
	return opt, ok, err
}

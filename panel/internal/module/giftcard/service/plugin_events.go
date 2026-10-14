package service

import (
	"fmt"

	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/pluginevents"
)

// UsePluginEvents 注入插件事件口（plugin 模块实现，见 internal/pkg/pluginevents）。
func (s *Service) UsePluginEvents(p pluginevents.Emitter) { s.events = p }

// pluginGranted 是 giftcard.redeemed 事件里的 granted：字段名是插件事件口径（产品决定），没有的项不出现；金额是分。
// 金额换算失败时返回错误，让兑换失败。
func pluginGranted(g granted) (map[string]any, error) {
	m := map[string]any{}
	if g.prizeLabel != "" {
		m["prize_label"] = g.prizeLabel
	}
	if g.balance.IsPositive() {
		cents, err := money.ToCents(g.balance)
		if err != nil {
			return nil, fmt.Errorf("礼品卡余额: %w", err)
		}
		m["balance"] = cents
	}
	if g.traffic > 0 {
		m["traffic_bytes"] = g.traffic
	}
	if g.days > 0 {
		m["expire_days"] = g.days
	}
	if g.reset {
		m["quota_reset"] = true
	}
	if g.planID != nil {
		m["plan_id"] = g.planID.String()
	}
	if g.planMode != "" {
		m["plan_mode"] = g.planMode
	}
	if g.subID != nil {
		m["subscription_id"] = g.subID.String()
	}
	if g.refund.IsPositive() {
		cents, err := money.ToCents(g.refund)
		if err != nil {
			return nil, fmt.Errorf("套餐退款: %w", err)
		}
		m["plan_refund"] = cents
		m["refund_currency"] = string(g.refundCur)
	}
	if g.ledgerTxn != nil {
		m["ledger_txn_id"] = g.ledgerTxn.String()
	}
	return m, nil
}

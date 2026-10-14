package service

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 佣金按订单币种计提，可能和站点币种不同，也可能一个人有多个币种。
// 汇总、提现按用户实际有佣金的币种来：请求里没有币种字段，只有一个币种有可用额时就用它，
// 多个币种都有可用额时不合并、不猜，回 ErrWithdrawCurrencyAmbiguous。
// 转余额只认站点币种（门户钱包只显示站点币种），别的币种的佣金走提现。

// currencyAvail 是某币种的可用佣金。
type currencyAvail struct {
	cur   money.Currency
	avail decimal.Decimal
}

// availableByCurrency 按用户有佣金条目的每个币种算可用额（币种升序）。要据此放行动账的调用方必须先在本事务里 LockUser。
func (s *Service) availableByCurrency(ctx context.Context, userID uuid.UUID) ([]currencyAvail, error) {
	curs, err := s.repo.EntryCurrenciesOf(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]currencyAvail, 0, len(curs))
	for _, c := range curs {
		cur := money.Currency(c)
		avail, err := s.available(ctx, userID, cur)
		if err != nil {
			return nil, err
		}
		out = append(out, currencyAvail{cur: cur, avail: avail})
	}
	return out, nil
}

// pickCurrency 选提现用的币种：恰一个币种可用额为正就是它；多于一个回 ErrWithdrawCurrencyAmbiguous；
// 一个都没有时 ok 为 false（调用方按各自的「没有可用佣金」报错）。skip 非空时不考虑这个币种（提现关闭时跳过站点币种）。
// 调用方必须已在本事务里 LockUser。
func (s *Service) pickCurrency(ctx context.Context, userID uuid.UUID, skip money.Currency) (cur money.Currency, avail decimal.Decimal, ok bool, err error) {
	list, err := s.availableByCurrency(ctx, userID)
	if err != nil {
		return "", decimal.Zero, false, err
	}
	for _, ca := range list {
		if !ca.avail.IsPositive() || (skip != "" && ca.cur == skip) {
			continue
		}
		if ok {
			return "", decimal.Zero, false, ErrWithdrawCurrencyAmbiguous
		}
		cur, avail, ok = ca.cur, ca.avail, true
	}
	return cur, avail, ok, nil
}

// summaryCurrency 选门户汇总展示的币种：没有佣金时用站点币种；只有一个币种就是它；
// 多个币种时取唯一有可用额的那个（就是能提、能转的那个），否则站点币种（在其中时）或最小的那个。
func (s *Service) summaryCurrency(ctx context.Context, userID uuid.UUID) (money.Currency, error) {
	list, err := s.availableByCurrency(ctx, userID)
	if err != nil {
		return "", err
	}
	site, err := s.ledger.DefaultCurrency(ctx)
	if err != nil {
		return "", err
	}
	switch len(list) {
	case 0:
		return site, nil
	case 1:
		return list[0].cur, nil
	}
	var positive []money.Currency
	hasSite := false
	for _, ca := range list {
		if ca.avail.IsPositive() {
			positive = append(positive, ca.cur)
		}
		if ca.cur == site {
			hasSite = true
		}
	}
	if len(positive) == 1 {
		return positive[0], nil
	}
	if hasSite {
		return site, nil
	}
	return list[0].cur, nil
}

// transferCurrency 返回转余额用的币种（站点币种）及其可用额。站点币种没有可用额、而别的币种有时
// 回 ErrTransferNotSiteCurrency，提示走提现。调用方必须已在本事务里 LockUser。
func (s *Service) transferCurrency(ctx context.Context, userID uuid.UUID) (money.Currency, decimal.Decimal, error) {
	site, err := s.ledger.DefaultCurrency(ctx)
	if err != nil {
		return "", decimal.Zero, err
	}
	list, err := s.availableByCurrency(ctx, userID)
	if err != nil {
		return "", decimal.Zero, err
	}
	siteAvail, otherPositive := decimal.Zero, false
	for _, ca := range list {
		switch {
		case ca.cur == site:
			siteAvail = ca.avail
		case ca.avail.IsPositive():
			otherPositive = true
		}
	}
	if !siteAvail.IsPositive() && otherPositive {
		return "", decimal.Zero, ErrTransferNotSiteCurrency
	}
	return site, siteAvail, nil
}

package billing

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/purchase"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 统一报价（设计稿 2.2）：门户的金额与默认值一律由服务端算好下发，前端只在两组数之间切换。
// 确认下单时四个建单入口按同一套函数重算，与 Expectation 不符回 409 quote_changed。

// 报价的 action
const (
	QuoteRenew  = "renew"
	QuoteChange = "change"
	QuoteNew    = "new"
	QuotePack   = "pack"
)

// quoteAsOfWindow 是报价时刻的有效窗口：as_of 只接受 [now-10min, now]。剩余价值里的
// 时间比例按 as_of 算，篡改 as_of 最多多拿 10 分钟的折算（约 ¥30×10/43200≈¥0.007）。
const quoteAsOfWindow = 10 * time.Minute

// maxQuoteRows 是换套餐按订阅或按套餐展开时最多返回的条数。
const maxQuoteRows = 20

// QuoteInput 是 POST /v1/me/checkout/quote 的请求。
type QuoteInput struct {
	UserID         string
	Action         string
	SubscriptionID string
	PlanID         string
	PackID         string
	CouponCode     string
	NewCopy        bool
}

// QuoteOutput 是报价响应。Balance 是报价时的可用余额，MinPayment 是能在线付的最低额（分，
// 启用渠道里最小的那个）。两组余额用法里 below_minimum 为真表示这一条付不了、不能下单。
type QuoteOutput struct {
	AsOf       time.Time `json:"as_of"`
	Currency   string    `json:"currency"`
	Balance    int64     `json:"balance"`
	MinPayment int64     `json:"min_payment"`
	Quotes     []Quote   `json:"quotes"`
}

// Quote 是一条报价：一个（订阅，套餐或流量包，价格档）组合。
type Quote struct {
	SubscriptionID *string `json:"subscription_id"`
	PlanID         *string `json:"plan_id"`
	PackID         *string `json:"pack_id"`
	PriceID        *string `json:"price_id"`
	Interval       string  `json:"interval"`
	IntervalCount  int     `json:"interval_count"`
	Subtotal       int64   `json:"subtotal"`
	Discount       int64   `json:"discount"`
	// Coupon 是生效的优惠码；CouponError 是优惠码不能用的中文原因（不让整个报价失败）
	Coupon      *QuoteCoupon `json:"coupon"`
	CouponError *string      `json:"coupon_error"`
	// Credit 是原套餐没用完的部分（只有换套餐非零），CreditDetail 是「怎么算的」
	Credit       int64         `json:"credit"`
	CreditDetail *CreditDetail `json:"credit_detail"`
	// Total = max(小计 − 折扣 − 剩余价值, 0)；Refund 是换便宜套餐时退进余额的差额
	Total          int64            `json:"total"`
	Refund         int64            `json:"refund"`
	WithBalance    purchase.Balance `json:"with_balance"`
	WithoutBalance purchase.Balance `json:"without_balance"`
	PeriodStart    *time.Time       `json:"period_start"`
	PeriodEnd      *time.Time       `json:"period_end"`
	PreviousEnd    *time.Time       `json:"previous_end"`

	currency string
}

// QuoteCoupon 是报价里生效的优惠码。
type QuoteCoupon struct {
	Code string `json:"code"`
}

// CreditDetail 是剩余价值的明细：本期付费合计 × min(剩余天数比, 剩余流量比)。
// RatioPPM 是取到的那个比例，按百万分之一。
type CreditDetail struct {
	Paid         int64 `json:"paid"`
	DaysLeft     int   `json:"days_left"`
	DaysTotal    int   `json:"days_total"`
	TrafficLeft  int64 `json:"traffic_left"`
	TrafficTotal int64 `json:"traffic_total"`
	RatioPPM     int64 `json:"ratio_ppm"`
}

// Expectation 是确认下单时前端带回的报价：AsOf 是报价时刻（剩余价值的时间比例按它算），
// 其余三项任一与服务端重算不相等就回 409 quote_changed。为 nil 时照旧执行（后台人工单、
// 老客户端、测试），余额同样经过 purchase.ApplyBalance 规范化。
type Expectation struct {
	AsOf           time.Time
	Total          int64
	BalanceApplied int64
	Payable        int64
}

// Quote 是统一报价（POST /v1/me/checkout/quote）：在一个不加锁、不写库的事务里按建单同一套
// 读取与函数算出每一条报价，最后统一过 purchase.ApplyBalance（开、关余额各一组）。
// 优惠码不能用时不让整个报价失败，原因写在 coupon_error。
func (s *Service) Quote(ctx context.Context, tenantID string, in QuoteInput) (*QuoteOutput, error) {
	if _, err := uuid.Parse(in.UserID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	out := QuoteOutput{Quotes: []Quote{}}
	err := s.pool.InTx(ctx, dbScope(tenantID, in.UserID), func(tx pgx.Tx) error {
		now := time.Now().UTC()
		out.AsOf = now
		var err error
		switch in.Action {
		case QuoteRenew:
			out.Quotes, err = quoteRenewTx(ctx, tx, tenantID, in, now)
		case QuoteNew:
			out.Quotes, err = quoteNewTx(ctx, tx, tenantID, in, now)
		case QuoteChange:
			out.Quotes, err = quoteChangeTx(ctx, tx, tenantID, in, now)
		case QuotePack:
			out.Quotes, err = quotePackTx(ctx, tx, tenantID, in)
		default:
			return httpx.Invalid(map[string]string{"action": "只能是 renew、change、new 或 pack"})
		}
		if err != nil {
			return err
		}
		out.Currency = "CNY"
		if len(out.Quotes) > 0 {
			out.Currency = out.Quotes[0].currency
		}
		if out.Balance, err = availableBalance(ctx, tx, tenantID, in.UserID, out.Currency); err != nil {
			return err
		}
		if out.MinPayment, err = minPayment(ctx, tx, tenantID, out.Currency); err != nil {
			return err
		}
		for i := range out.Quotes {
			q := &out.Quotes[i]
			avail := out.Balance
			if q.currency != out.Currency {
				avail = 0 // 不同币种的余额不能抵（目录只有 CNY 时不会发生）
			}
			// 只有换套餐的零头能免；其余入口付不了的标 below_minimum，前端提示先充值或用余额
			waive := in.Action == QuoteChange
			q.WithBalance = purchase.WaiveSmallDue(purchase.ApplyBalance(q.Total, avail, avail, out.MinPayment), waive)
			q.WithoutBalance = purchase.WaiveSmallDue(purchase.ApplyBalance(q.Total, avail, 0, out.MinPayment), waive)
		}
		return nil
	})
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, he
		}
		return nil, httpx.Internal(err)
	}
	return &out, nil
}

// quoteCoupon 按建单同一口径试算优惠码（不锁券行）；不能用时把原因放进 coupon_error。
func quoteCoupon(ctx context.Context, tx pgx.Tx, tenantID, userID, code, planID, currency string,
	subtotal int64, q *Quote) error {
	if code == "" {
		return nil
	}
	m, err := applyCouponTx(ctx, tx, tenantID, userID, code, planID, currency, subtotal, false)
	var he *httpx.Error
	if errors.As(err, &he) {
		msg := he.Message
		q.CouponError = &msg
		return nil
	}
	if err != nil {
		return err
	}
	if m != nil {
		q.Discount = m.Discount
		q.Coupon = &QuoteCoupon{Code: m.Code}
	}
	return nil
}

func strPtr(s string) *string { return &s }

func timePtr(t time.Time) *time.Time { return &t }

// quoteRenewTx：一份订阅续一期，每个价格档一条。生效中的接在原到期日后，过期 30 天内的从现在起算。
func quoteRenewTx(ctx context.Context, tx pgx.Tx, tenantID string, in QuoteInput,
	now time.Time) ([]Quote, error) {
	if in.SubscriptionID == "" {
		return nil, httpx.Invalid(map[string]string{"subscription_id": "必填"})
	}
	src, err := loadRenewalSourceTx(ctx, tx, tenantID, in.UserID, in.SubscriptionID, false)
	if err != nil {
		return nil, err
	}
	var productID string
	if err := tx.QueryRow(ctx, `SELECT product_id::text FROM plans WHERE tenant_id = $1 AND id = $2::uuid`,
		tenantID, src.PlanID).Scan(&productID); err != nil {
		return nil, err
	}
	prices, err := listPlanPricesTx(ctx, tx, tenantID, productID, src.UserGroupID, now)
	if err != nil {
		return nil, err
	}
	base := now
	if src.Status != "expired" && src.PeriodEnd != nil && src.PeriodEnd.After(now) {
		base = *src.PeriodEnd
	}
	out := make([]Quote, 0, len(prices))
	for _, p := range prices {
		q := Quote{SubscriptionID: strPtr(in.SubscriptionID), PlanID: strPtr(src.PlanID),
			PriceID: strPtr(p.ID), Interval: p.Interval, IntervalCount: int(p.IntervalCount),
			Subtotal: p.UnitAmount, currency: p.Currency, PreviousEnd: src.PeriodEnd,
			PeriodStart: timePtr(base), PeriodEnd: timePtr(addInterval(base, p.Interval, int(p.IntervalCount)))}
		if err := quoteCoupon(ctx, tx, tenantID, in.UserID, in.CouponCode, src.PlanID, p.Currency,
			p.UnitAmount, &q); err != nil {
			return nil, err
		}
		q.Total = orderTotal(q.Subtotal, q.Discount, 0, 0)
		out = append(out, q)
	}
	return out, nil
}

// quoteNewTx：新购一份，每个价格档一条。没带 new_copy 而已有可续的同款时与建单一样回 409
// （门户改走续费）；同一套餐已有未付款的新购单回 409 order_pending。
func quoteNewTx(ctx context.Context, tx pgx.Tx, tenantID string, in QuoteInput,
	now time.Time) ([]Quote, error) {
	if in.PlanID == "" {
		return nil, httpx.Invalid(map[string]string{"plan_id": "必填"})
	}
	plan, err := loadNewPurchasePlanTx(ctx, tx, tenantID, in.UserID, in.PlanID, false, now)
	if err != nil {
		return nil, err
	}
	if !in.NewCopy {
		if sub, err := renewableSamePlanSubscription(ctx, tx, tenantID, in.UserID, in.PlanID, false); err != nil {
			return nil, err
		} else if sub != "" {
			return nil, ErrSamePlanUseRenewal
		}
	}
	if err := ensureNoPendingNewOrder(ctx, tx, tenantID, in.UserID, in.PlanID, plan.PlanName); err != nil {
		return nil, err
	}
	prices, err := listPlanPricesTx(ctx, tx, tenantID, plan.ProductID, plan.UserGroupID, now)
	if err != nil {
		return nil, err
	}
	out := make([]Quote, 0, len(prices))
	for _, p := range prices {
		q := Quote{PlanID: strPtr(in.PlanID), PriceID: strPtr(p.ID), Interval: p.Interval,
			IntervalCount: int(p.IntervalCount), Subtotal: p.UnitAmount, currency: p.Currency,
			PeriodStart: timePtr(now), PeriodEnd: timePtr(addInterval(now, p.Interval, int(p.IntervalCount)))}
		if err := quoteCoupon(ctx, tx, tenantID, in.UserID, in.CouponCode, in.PlanID, p.Currency,
			p.UnitAmount, &q); err != nil {
			return nil, err
		}
		q.Total = orderTotal(q.Subtotal, q.Discount, 0, 0)
		out = append(out, q)
	}
	return out, nil
}

// quoteChangeTx：换套餐。给了订阅没给套餐，按这份能换成的每个套餐展开；给了套餐没给订阅，
// 按每份能换的订阅展开（多份时「把哪一份换成 X」）；都给就一条。每个价格档各一条。
func quoteChangeTx(ctx context.Context, tx pgx.Tx, tenantID string, in QuoteInput,
	now time.Time) ([]Quote, error) {
	if in.SubscriptionID == "" && in.PlanID == "" {
		return nil, httpx.Invalid(map[string]string{"subscription_id": "订阅与套餐至少给一个"})
	}
	var sources []*changeSource
	if in.SubscriptionID != "" {
		src, err := loadChangeSourceTx(ctx, tx, tenantID, in.UserID, in.SubscriptionID, false)
		if err != nil {
			return nil, err
		}
		if in.PlanID == src.PlanID {
			return nil, ErrPlanChangeSamePlan
		}
		sources = append(sources, src)
	} else {
		cands, err := loadCandidatesTx(ctx, tx, tenantID, in.UserID)
		if err != nil {
			return nil, err
		}
		for _, c := range cands {
			if c.State == purchase.StateDead || c.PlanID == in.PlanID || len(sources) >= maxQuoteRows {
				continue
			}
			src, err := loadChangeSourceTx(ctx, tx, tenantID, in.UserID, c.SubscriptionID, false)
			var he *httpx.Error
			if errors.As(err, &he) {
				continue // 有未完结的续费或变更单之类：这一份现在不能换，不列
			}
			if err != nil {
				return nil, err
			}
			sources = append(sources, src)
		}
	}
	var planIDs []string
	if in.PlanID != "" {
		planIDs = []string{in.PlanID}
	}
	out := []Quote{}
	for _, src := range sources {
		exclude := src.PlanID
		targets, err := loadChangeTargetsTx(ctx, tx, tenantID, planIDs, exclude, src.UserGroupID, false, now)
		if err != nil {
			return nil, err
		}
		for _, t := range targets {
			for _, p := range t.Prices {
				credit, detail, err := src.credit(now, p.Currency)
				if err != nil {
					continue // 原订阅本期币种与这个价格不同，换不过去
				}
				q := Quote{SubscriptionID: strPtr(src.SubscriptionID), PlanID: strPtr(t.PlanID),
					PriceID: strPtr(p.ID), Interval: p.Interval, IntervalCount: int(p.IntervalCount),
					Subtotal: p.UnitAmount, Credit: credit, CreditDetail: detail, currency: p.Currency,
					PreviousEnd: src.PeriodEnd, PeriodStart: timePtr(now),
					PeriodEnd: timePtr(addInterval(now, p.Interval, int(p.IntervalCount)))}
				if err := quoteCoupon(ctx, tx, tenantID, in.UserID, in.CouponCode, t.PlanID, p.Currency,
					p.UnitAmount, &q); err != nil {
					return nil, err
				}
				q.Total = orderTotal(q.Subtotal, q.Discount, q.Credit, 0)
				q.Refund = max(q.Credit-(q.Subtotal-q.Discount), 0)
				out = append(out, q)
			}
		}
	}
	return out, nil
}

// quotePackTx：一个流量包。给了订阅就核对它是本人生效中的（建单要求同样）。
func quotePackTx(ctx context.Context, tx pgx.Tx, tenantID string, in QuoteInput) ([]Quote, error) {
	if _, err := uuid.Parse(in.PackID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	q := Quote{PackID: strPtr(in.PackID)}
	if in.SubscriptionID != "" {
		if _, err := uuid.Parse(in.SubscriptionID); err != nil {
			return nil, httpx.NotFoundOrForbidden()
		}
		var status string
		err := tx.QueryRow(ctx, `SELECT status FROM subscriptions
			WHERE tenant_id = $1 AND id = $2::uuid AND user_id = $3::uuid`,
			tenantID, in.SubscriptionID, in.UserID).Scan(&status)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return nil, err
		}
		if !subscriptionAcceptsPaidChange(status, true) {
			return nil, errPackNeedsLiveSubscription
		}
		q.SubscriptionID = strPtr(in.SubscriptionID)
	}
	err := tx.QueryRow(ctx, `
		SELECT currency::text, unit_amount FROM traffic_packs
		 WHERE tenant_id = $1 AND id = $2::uuid AND status = 'active'`,
		tenantID, in.PackID).Scan(&q.currency, &q.Subtotal)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return nil, err
	}
	// 限定套餐的券对流量包不适用（planID 传空，与建单同）
	if err := quoteCoupon(ctx, tx, tenantID, in.UserID, in.CouponCode, "", q.currency, q.Subtotal, &q); err != nil {
		return nil, err
	}
	q.Total = orderTotal(q.Subtotal, q.Discount, 0, 0)
	return []Quote{q}, nil
}

package billing

import (
	"time"

	"github.com/aegispanel/aegis/internal/domain/purchase"
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

// QuoteOutput 是报价响应。Balance 是报价时的可用余额，MinPayment 是支付最低额（分）。
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

package service

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// PlanChangeOrderInput 是门户换套餐（POST /v1/me/subscriptions/{id}/change-plan）：在原订阅上换成 PlanID 的 PriceID 档。
// 金额来自报价（action=change），确认时带回 as_of 与 expect；剩余价值的时间比例按 as_of 算。
type PlanChangeOrderInput struct {
	SubscriptionID uuid.UUID
	PlanID         uuid.UUID
	PriceID        uuid.UUID
	Checkout
	Reply CreatedReply
}

// 人工单的结算方式。
const (
	// SettlementGrant 赠送：全额减免、当场履约（缺省）
	SettlementGrant = "grant"
	// SettlementPending 待用户支付：建一张待支付单交给用户去付
	SettlementPending = "pending"
	// SettlementOffline 线下已收款：建单并按线下渠道当场结清
	SettlementOffline = "offline"
)

// 落点动作。
const (
	PlacementRenew  = "renew"  // 续到同款那份
	PlacementChange = "change" // 把那份换成这个套餐（Expired 时是「恢复并改成它」）
	PlacementNew    = "new"    // 另开一份
)

// 落点徽标：同款那份（前端显示「同款，最常见」）。
const BadgeSamePlan = "same_plan"

// Choice 是管理员选定的落点（预览给出的选项之一）。
type Choice struct {
	Kind           string
	SubscriptionID *uuid.UUID
}

// ManualOrderInput 是后台人工开单（POST /v1/orders/manual）。Settlement 为空按 grant；Reference 只有 offline 必填。
// Target 是这单落到哪一份；选项只有一个时可以不传，多于一个而没带回 422。
type ManualOrderInput struct {
	UserID     uuid.UUID
	PlanID     uuid.UUID
	PriceID    uuid.UUID
	Reason     string
	Settlement string
	Reference  string
	Target     *Choice
	Reply      CreatedReply
}

// ManualPreviewInput 是后台开单前看「这单落到哪一份」（POST /v1/orders/manual/preview）。
// PriceID 为 nil 时只给落点，不报应付与最低额；EntrySubscriptionID 是从哪一行订阅点进来的，只影响默认值。
type ManualPreviewInput struct {
	UserID              uuid.UUID
	PlanID              uuid.UUID
	PriceID             *uuid.UUID
	EntrySubscriptionID *uuid.UUID
}

// ManualPreview 是开单预览：选项、默认选中的 Key（空串表示不预选）与这个币种上的最低付款额（价格档未知时为 0）。
type ManualPreview struct {
	Options    []Placement
	DefaultKey string
	MinPayment decimal.Decimal
}

// Placement 是一个落点选项连同「会发生什么」。Key：new 是 "new"，其余是 "<kind>:<订阅 ID>"。
type Placement struct {
	Key            string
	Kind           string
	SubscriptionID *uuid.UUID
	// Expired 只用在 change 上：目标是可救回的那份，即「恢复并改成这个套餐」
	Expired bool
	Badge   string
	// 这一份现在的样子；new 选项这几项为零值
	Label     string
	PlanID    *uuid.UUID
	PlanName  string
	State     subsvc.State
	PeriodEnd *time.Time
	// NewPeriodEnd 落地之后的到期日（价格档未知时按套餐第一个在售价格，都没有按一个月）
	NewPeriodEnd *time.Time
	// Credit、Currency、GiftDaysLost 只用在 change 上：原套餐没用完的部分、它的币种、不保留的赠送整天数
	Credit       decimal.Decimal
	Currency     money.Currency
	GiftDaysLost int
	// 本期套餐流量（不限量记 0）与挂在这一份上的流量包余量（字节）
	TrafficUsed   int64
	TrafficCap    int64
	PackRemaining int64
	// Due 是按「待用户支付」开时用户要付多少（change 先抵 Credit）；BelowMinimum 是它低于站点最低额、用户没法在线付
	Due          decimal.Decimal
	BelowMinimum bool
	// Unavailable 非空表示按这个价格档开不了这一项，内容是建单会回的那句话（目录不允许、价格下架、币种不一致、
	// 已有未完结的续费或变更单等）；只在给了价格档时检查目录
	Unavailable string
}

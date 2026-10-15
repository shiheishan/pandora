package service

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 本文件是 promo 对外的类型、错误与依赖接口（由块负责人定稿）；handler 与别的模块只看这些，不看 model。

// 折扣类型（JSON 里的 discount_type）。
const (
	KindPercent = "percent"
	KindFixed   = "fixed"
)

// 券状态：后台只能在这两种之间切换；过期看 valid_until，用完看计数。
const (
	StatusActive = "active"
	StatusPaused = "paused"
)

// 占用状态：held 只能走到 captured 或 released（见 CanTransition）。
const (
	RedemptionHeld     = "held"
	RedemptionCaptured = "captured"
	RedemptionReleased = "released"
)

// 审计动作名与资源类型。
const (
	ActionCreated        = "coupon.created"
	ActionBatchGenerated = "coupon.batch_generated"
	ActionStatusChange   = "coupon.status_change"

	ResourceCoupon = "coupon"
)

// 后台的上限。
const (
	// BatchMax 是一次批量生成的上限。
	BatchMax = 1000
	// RedemptionsLimit 是单张券核销明细返回的最多条数。
	RedemptionsLimit = 200
)

// BillingIntervals 是券可限定的计费周期（与 catalog 价格档 billing_interval 的枚举、00046 coupon_intervals 的 CHECK 一致）。
var BillingIntervals = []string{"day", "week", "month", "quarter", "year", "one_time"}

// Face 是券面：Kind 为 percent 时 PercentBP 有效（万分比，1–10000），为 fixed 时 AmountOff 有效（元）。
type Face struct {
	Kind      string
	PercentBP int64
	AmountOff decimal.Decimal
}

// Spec 是后台新建券的规格，单张创建与批量生成共用；handler 已做缺省值（币种 CNY、每人 1 次、名称缺省为码）。
type Spec struct {
	Name           string
	Face           Face
	Currency       string
	MaxDiscount    *decimal.Decimal // 只用于百分比券；nil 为不封顶
	MinOrder       decimal.Decimal  // 0 为没有门槛
	MaxRedemptions *int             // nil 为不限
	MaxPerUser     int
	PlanIDs        []uuid.UUID // 空为全部套餐
	Intervals      []string    // 空为不限周期
	UserGroupID    *uuid.UUID  // nil 为不限
	ValidFrom      *time.Time
	ValidUntil     *time.Time
}

// ListFilter 是后台券列表的筛选；零值不筛。
type ListFilter struct {
	Query  string // 券码或名称包含（不分大小写）
	Status string // active / paused
}

// Coupon 是后台列表的一行。
type Coupon struct {
	ID              uuid.UUID
	Code            string
	Name            string
	Face            Face
	Currency        string
	MaxDiscount     *decimal.Decimal
	MinOrder        decimal.Decimal
	MaxRedemptions  *int
	MaxPerUser      int
	Redeemed        int
	Reserved        int
	PlanIDs         []uuid.UUID
	Intervals       []string // 永不为 nil
	UserGroupID     *uuid.UUID
	ValidFrom       *time.Time
	ValidUntil      *time.Time
	Status          string
	CreatedAt       time.Time
	DiscountedTotal decimal.Decimal // 未释放的占用（held + captured）减掉的总额
}

// Redemption 是单张券的一条核销明细。
type Redemption struct {
	UserID   uuid.UUID
	Email    string // 经 UserDirectory 批量取；取不到为空串
	OrderNo  string
	Discount decimal.Decimal
	Currency string
	At       time.Time
	Released bool
}

// Target 是一次试算或占用的对象：谁、买哪个套餐、什么币种、小计多少。
type Target struct {
	UserID   uuid.UUID
	PlanID   *uuid.UUID // 流量包订单为 nil：限定了套餐的券对它不适用
	Interval *string    // 这一单价格档的计费周期；流量包、流量重置单为 nil：限定了周期的券对它不适用
	Currency string
	Subtotal decimal.Decimal // 已是 2 位小数的非负金额
}

// Applied 是一张券用在一单上的结果；Discount 已向零截断、不超过小计。
type Applied struct {
	CouponID uuid.UUID
	Code     string
	Face     Face
	Discount decimal.Decimal
}

// OrderRef 是占用时绑定的订单。
type OrderRef struct {
	ID uuid.UUID
	No string
}

// PreviewItem 是门户试算的对象：{PlanID, PriceID} 试算套餐，或 {PackID} 试算流量包，二选一（handler 已校验）。
type PreviewItem struct {
	PlanID  *uuid.UUID
	PriceID *uuid.UUID
	PackID  *uuid.UUID
}

// Preview 是门户试算的结果；没填码时 Applied 为 nil、Discount 为 0。
type Preview struct {
	Subtotal decimal.Decimal
	Discount decimal.Decimal
	Payable  decimal.Decimal
	Currency string
	Applied  *Applied
}

// CatalogReader 由 catalog 实现（或由 app 写适配器），启动装配时经 UseCatalog 注入。
type CatalogReader interface {
	// MissingPlans 返回 ids 里不存在的套餐 ID（已下架、已归档的也算存在）；全部存在时返回空切片。
	MissingPlans(ctx context.Context, ids []uuid.UUID) ([]uuid.UUID, error)
	// PriceSubtotal 取在售价格的币种与金额；价格必须属于 planID 这个套餐，不在售或对不上回 errs.NotFound。
	PriceSubtotal(ctx context.Context, planID, priceID uuid.UUID) (currency string, amount decimal.Decimal, err error)
	// PriceInterval 取在售价格的计费周期（billing_interval）；与 PriceSubtotal 同样的可见性规则，买不到回 errs.NotFound。
	PriceInterval(ctx context.Context, planID, priceID uuid.UUID) (string, error)
	// PackSubtotal 取在售流量包的币种与金额；不在售回 errs.NotFound。
	PackSubtotal(ctx context.Context, packID uuid.UUID) (currency string, amount decimal.Decimal, err error)
}

// UserDirectory 由 identity 实现，启动装配时经 UseUsers 注入。
type UserDirectory interface {
	// Emails 批量取用户邮箱（identity 已有同名方法）。
	Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
	// UserGroupID 取用户所在的用户组；不在任何组时返回 nil。
	UserGroupID(ctx context.Context, userID uuid.UUID) (*uuid.UUID, error)
}

// 用户输入的券码不能用时的错误（报价时 order 把它们的文案放进 coupon_error，见 IsRejection）。
// 「不存在」「还没开始」「币种不对」「已停用」对外是同一句：提前说「明天生效」等于泄露未公布的活动。
var (
	ErrCouponNotFound     = errs.Newf(errs.ValidationFailed, "优惠码不存在或已失效")
	ErrCouponExpired      = errs.Newf(errs.ValidationFailed, "优惠码已过期")
	ErrCouponUsedUp       = errs.Newf(errs.Conflict, "优惠码已达使用上限")
	ErrCouponUserLimit    = errs.Newf(errs.Conflict, "你已使用过这个优惠码")
	ErrCouponMinAmount    = errs.Newf(errs.ValidationFailed, "订单金额未达到优惠码的使用门槛")
	ErrCouponNotForPlan   = errs.Newf(errs.ValidationFailed, "这个优惠码不适用于所选套餐")
	ErrCouponNotForPeriod = errs.Newf(errs.ValidationFailed, "这个优惠码不适用于所选时长")
	// ErrCouponNotForReset 是流量重置单带了券码：重置单永远按原价收，不吃券也不吃会员折扣（用户 10-14 定）。
	// promo 自己不判断，由 order 在报价与建单时对重置单直接回它；列在 rejections 里，报价落到 coupon_error。
	ErrCouponNotForReset = errs.Newf(errs.ValidationFailed, "流量重置不能使用优惠码")
	// 不说破「你不在某个组里」，只让他知道这码他用不了。
	ErrCouponNotForUser = errs.Newf(errs.ValidationFailed, "这个优惠码不可用")
)

// 后台与下单链路的其它错误。
var (
	// ErrCodeExists 是新建时券码已存在。
	ErrCodeExists = errs.Newf(errs.Conflict, "这个优惠码已经存在")
	// ErrOrderHasCoupon 是这张订单已经占用过一张券。
	ErrOrderHasCoupon = errs.Newf(errs.Conflict, "这张订单已经用过优惠码")
	// ErrNotHeld 是释放或确认时这张订单没有占用中的券（已释放、已确认或从未占用）。
	ErrNotHeld = errs.Newf(errs.Conflict, "这张订单没有占用中的优惠码")
)

var rejections = []error{
	ErrCouponNotFound, ErrCouponExpired, ErrCouponUsedUp, ErrCouponUserLimit,
	ErrCouponMinAmount, ErrCouponNotForPlan, ErrCouponNotForPeriod, ErrCouponNotForUser,
	ErrCouponNotForReset,
}

// IsRejection 报告 err 是否是「这个码对这一单不能用」（用户输入问题，不是系统故障）。
func IsRejection(err error) bool {
	for _, r := range rejections {
		if errors.Is(err, r) {
			return true
		}
	}
	return false
}

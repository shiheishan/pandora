// Package service 是 ledger 模块的业务层：复式记账、用户余额、订单余额冻结与后台调账。
// 别的模块只调本包；所有公开的写方法都能在调用方事务里执行（ctx 里有事务就加入）。
package service

import (
	"errors"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// AccountType 是科目类型。
type AccountType string

const (
	UserBalance             AccountType = "user_balance"
	UserBalanceHold         AccountType = "user_balance_hold"
	UserCommissionPending   AccountType = "user_commission_pending"
	UserCommissionAvailable AccountType = "user_commission_available"
	ChannelCash             AccountType = "channel_cash"
	PlatformRevenue         AccountType = "platform_revenue"
	PlatformFeeExpense      AccountType = "platform_fee_expense"
	PlatformPromoExpense    AccountType = "platform_promo_expense"
	Suspense                AccountType = "suspense"
	LatePaymentSuspense     AccountType = "late_payment_suspense"
)

// Valid 报告科目类型是否在库里登记的 10 种之一。
func (t AccountType) Valid() bool {
	switch t {
	case UserBalance, UserBalanceHold, UserCommissionPending, UserCommissionAvailable,
		ChannelCash, PlatformRevenue, PlatformFeeExpense, PlatformPromoExpense,
		Suspense, LatePaymentSuspense:
		return true
	default:
		return false
	}
}

// UserOwned 报告是否为用户类科目（前 4 种）。
func (t AccountType) UserOwned() bool {
	switch t {
	case UserBalance, UserBalanceHold, UserCommissionPending, UserCommissionAvailable:
		return true
	default:
		return false
	}
}

// DebitNormal 报告正常方向是否为借方（资产、费用类科目）。
func (t AccountType) DebitNormal() bool {
	switch t {
	case ChannelCash, PlatformFeeExpense, PlatformPromoExpense, Suspense:
		return true
	default:
		return false
	}
}

// 平台科目的常用归属。
const (
	RefMain         = "main"              // platform_revenue、late_payment_suspense
	RefManualAdjust = "manual_adjust"     // suspense：后台人工调账
	RefGift         = "gift"              // platform_promo_expense：礼品卡赠送
	RefPayout       = "payout"            // channel_cash：佣金提现打款
	RefSurcharge    = "payment_surcharge" // platform_revenue：向用户加收的支付手续费
)

// Kind 是交易类型，与库里 CHECK 一致。
type Kind string

const (
	KindBalanceTopup        Kind = "balance_topup"
	KindBalanceAdjusted     Kind = "balance_adjusted"
	KindBalanceHold         Kind = "balance_hold"
	KindBalanceRelease      Kind = "balance_release"
	KindOrderPaid           Kind = "order_paid"
	KindPlanChangeRefund    Kind = "plan_change_refund"
	KindLatePaymentSuspense Kind = "late_payment_suspense"
	KindLatePaymentApplied  Kind = "late_payment_applied"
	KindGiftCardBalance     Kind = "gift_card_balance"
	KindCommissionAccrued   Kind = "commission_accrued"
	KindCommissionSettled   Kind = "commission_settled"
	KindCommissionPaid      Kind = "commission_paid"
	KindCommissionToBalance Kind = "commission_to_balance"
	KindCommissionVoided    Kind = "commission_voided" // 作废待复核佣金：冲回计提（00016）
)

// Valid 报告交易类型是否在库里登记的 14 种之一。
func (k Kind) Valid() bool {
	switch k {
	case KindBalanceTopup, KindBalanceAdjusted, KindBalanceHold, KindBalanceRelease,
		KindOrderPaid, KindPlanChangeRefund, KindLatePaymentSuspense, KindLatePaymentApplied,
		KindGiftCardBalance, KindCommissionAccrued, KindCommissionSettled,
		KindCommissionPaid, KindCommissionToBalance, KindCommissionVoided:
		return true
	default:
		return false
	}
}

// Dedicated 报告该 kind 是否只能走专门的方法（HoldForOrder、ReleaseOrderHold、PostOrderPaid、CaptureFromBalance、AdjustBalance），Post 拒收。
func (k Kind) Dedicated() bool {
	switch k {
	case KindBalanceHold, KindBalanceRelease, KindOrderPaid, KindBalanceAdjusted:
		return true
	default:
		return false
	}
}

// OnePerSource 报告该 kind 是否同一来源只能记一笔；清单与迁移里 ledger_transactions_source_once_key 一致，改一边要同步另一边。
func (k Kind) OnePerSource() bool {
	switch k {
	case KindBalanceTopup, KindBalanceHold, KindBalanceRelease, KindOrderPaid,
		KindLatePaymentSuspense, KindPlanChangeRefund, KindCommissionSettled,
		KindCommissionPaid, KindGiftCardBalance, KindCommissionVoided:
		return true
	default:
		return false
	}
}

// Direction 是分录方向。
type Direction string

const (
	Debit  Direction = "debit"
	Credit Direction = "credit"
)

// AccountRef 指定一个科目：用户类给 UserID（Ref 留空），平台类给 Ref（UserID 留零值）。
type AccountRef struct {
	Type   AccountType
	UserID uuid.UUID
	Ref    string
}

// UserAccount 构造用户类科目引用。
func UserAccount(t AccountType, userID uuid.UUID) AccountRef {
	return AccountRef{Type: t, UserID: userID, Ref: ""}
}

// PlatformAccount 构造平台类科目引用。
func PlatformAccount(t AccountType, ref string) AccountRef {
	return AccountRef{Type: t, UserID: uuid.Nil, Ref: ref}
}

// Leg 是一条待记分录：金额恒为正，方向由 Direction 表达。
type Leg struct {
	Account   AccountRef
	Direction Direction
	Amount    decimal.Decimal
}

// Posting 是一笔待记的交易。SourceType 与 SourceID 要么都给、要么都不给。
type Posting struct {
	Kind       Kind
	SourceType string
	SourceID   uuid.UUID
	Memo       string
	Legs       []Leg
}

// 常用来源类型。
const (
	SourceOrder        = "order"
	SourceUser         = "user"
	SourcePayment      = "payment"
	SourceGiftCardCode = "gift_card_code"
)

// 冻结状态。
const (
	HoldHeld     = "held"
	HoldCaptured = "captured"
	HoldReleased = "released"
)

// ErrInsufficientBalance 是用户类科目余额不够扣。
var ErrInsufficientBalance = errs.Newf(errs.Conflict, "余额不足")

// ErrBalanceConstraint 是写余额撞上库里的不透支约束（预检漏掉的兜底）：语句已失败、调用方事务已作废，必须回滚。
// 它总是和 ErrInsufficientBalance 一起出现在包装链里；调用方要先判它，判到就不能再在同一事务里写。
var ErrBalanceConstraint = errors.New("ledger: 写余额撞上库里的不透支约束，事务已作废")

// ErrDuplicatePosting 是同一来源的同类交易已经记过（如一张订单的第二笔 order_paid）。
var ErrDuplicatePosting = errs.Newf(errs.Conflict, "这笔账已经记过了")

// ErrHoldExists 是这张订单已经冻结过余额。
var ErrHoldExists = errs.Newf(errs.Conflict, "这张订单已经冻结过余额")

// ErrHoldCaptured 是冻结已经扣款，不能再释放。
var ErrHoldCaptured = errs.Newf(errs.Conflict, "这张订单的余额已经扣款，不能释放")

// ErrHoldReleased 是这张订单的余额冻结已经释放，不能再扣款。
var ErrHoldReleased = errs.Newf(errs.Conflict, "这张订单的余额冻结已经释放，不能再扣款")

// ErrUserNotFound 是科目归属的用户不存在。
var ErrUserNotFound = errs.New(errs.NotFound)

// ErrInvalidPosting 表示调用方传入的记账参数不合法（程序错误，对外是 500）。
var ErrInvalidPosting = errors.New("ledger: 记账参数不合法")

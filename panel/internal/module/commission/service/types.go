package service

import (
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 佣金条目状态：pending 冻结中，available 已解冻入可提现。
const (
	EntryPending   = "pending"
	EntryAvailable = "available"
)

// 提现状态：requested 申请中，approved 已批准待打款，rejected 已驳回，paid 已打款。
const (
	WithdrawalRequested = "requested"
	WithdrawalApproved  = "approved"
	WithdrawalRejected  = "rejected"
	WithdrawalPaid      = "paid"
)

// entryTransitions 是佣金条目的转换表。
var entryTransitions = map[string][]string{
	EntryPending: {EntryAvailable},
}

// withdrawalTransitions 是提现的转换表：审批只在申请中做一次，打款只对已批准的做。
var withdrawalTransitions = map[string][]string{
	WithdrawalRequested: {WithdrawalApproved, WithdrawalRejected},
	WithdrawalApproved:  {WithdrawalPaid},
}

// CanEntryTransition 查佣金条目状态转换表。
func CanEntryTransition(from, to string) bool { return allowed(entryTransitions, from, to) }

// CanWithdrawalTransition 查提现状态转换表。
func CanWithdrawalTransition(from, to string) bool { return allowed(withdrawalTransitions, from, to) }

func allowed(table map[string][]string, from, to string) bool {
	for _, t := range table[from] {
		if t == to {
			return true
		}
	}
	return false
}

// RateBP 把整数百分比换成万分比。
func RateBP(percent int) int64 { return int64(percent) * 100 }

// Amount 是佣金公式：trunc(基数 × rate_bp / 10000)，向零截断到分（R3）。库里有同样的 CHECK。
func Amount(base decimal.Decimal, rateBP int64) decimal.Decimal { return money.ApplyBP(base, rateBP) }

// 审计动作与资源类型（沿用旧动作名）。
const (
	ActionWithdrawalRequested = "withdrawal.requested"
	ActionWithdrawalApprove   = "withdrawal.approve"
	ActionWithdrawalReject    = "withdrawal.reject"
	ActionWithdrawalPaid      = "withdrawal.paid"
	ActionTransferredToBal    = "commission.transferred_to_balance"

	ResourceWithdrawal        = "withdrawal"
	ResourceLedgerTransaction = "ledger_transaction"
)

// 账本来源类型。
const (
	SourceCommissionEntry    = "commission_entry"
	SourceWithdrawal         = "withdrawal"
	SourceCommissionTransfer = "commission_transfer"
)

// 列表条数上限（与旧接口一致）。
const (
	MyEntriesLimit        = 100
	MyWithdrawalsLimit    = 50
	MyTransfersLimit      = 50
	AdminWithdrawalsLimit = 200
	// SettleBatchDefault 是解冻任务一轮最多处理的条数。
	SettleBatchDefault = 500
)

// 文本长度上限。
const (
	MaxPayoutDetailRunes    = 500
	MaxRejectReasonRunes    = 500
	MaxPayoutReferenceRunes = 128
)

// 对外错误（文案沿用旧代码）。
var (
	ErrNoCommission         = errs.Newf(errs.ValidationFailed, "没有可提现的佣金")
	ErrWithdrawTooSmall     = errs.Newf(errs.ValidationFailed, "提现金额低于最低限额")
	ErrWithdrawTooMuch      = errs.Newf(errs.Conflict, "提现金额超过可提现余额")
	ErrWithdrawPending      = errs.Newf(errs.Conflict, "还有正在处理的提现申请")
	ErrTransferInsufficient = errs.Newf(errs.Conflict, "可提现佣金不足。冻结期内的佣金要等解冻后才能转出，提现处理中的金额也不能再转")
	ErrWithdrawalHandled    = errs.Newf(errs.Conflict, "该提现申请已被处理过")
	ErrWithdrawalNotPayable = errs.Newf(errs.Conflict, "只有已批准的提现才能标记为已打款")
	ErrWithdrawalNotFound   = errs.Newf(errs.NotFound, "提现申请不存在")
	// ErrWithdrawCurrencyAmbiguous 是可用佣金分在多个币种：请求里没有币种，不合并、不猜。
	ErrWithdrawCurrencyAmbiguous = errs.Newf(errs.Conflict, "可提现佣金包含多个币种，请联系管理员分币种处理")
	// ErrTransferNotSiteCurrency 是可用佣金只有非站点币种：钱包只显示站点币种，这部分请走提现。
	ErrTransferNotSiteCurrency = errs.Newf(errs.Conflict, "这部分佣金不是站点币种，不能转入余额，请走提现")
)

// Summary 是门户「我的佣金」的汇总，金额都是 Currency 这一个币种（取法见 summaryCurrency）。
type Summary struct {
	Currency     money.Currency
	Pending      decimal.Decimal // 冻结中
	Available    decimal.Decimal // 可提现（已解冻 − 未驳回的提现 − 已转余额，不小于 0）
	Withdrawing  decimal.Decimal // 提现处理中（申请中、已批准）
	Settled      decimal.Decimal // 已打款
	TotalEarned  decimal.Decimal // 累计佣金（含冻结中）
	Invitees     int64           // 邀请人数
	Orders       int64           // 产生佣金的订单数
	PaidInvitees int64           // 产生过佣金的被邀请人数
	RatePercent  int
	MinWithdraw  decimal.Decimal
	Scope        string
}

// EntryView 是门户佣金明细的一行；From 是打码后的被邀请人邮箱。
type EntryView struct {
	Amount      decimal.Decimal
	Base        decimal.Decimal
	RateBP      int64
	Currency    string
	Status      string
	FrozenUntil time.Time
	CreatedAt   time.Time
	OrderNo     string
	From        string
}

// WithdrawalView 是门户提现记录的一行。
type WithdrawalView struct {
	ID           uuid.UUID
	Amount       decimal.Decimal
	Currency     string
	Status       string
	RejectReason string
	RequestedAt  time.Time
	CompletedAt  *time.Time
}

// TransferView 是门户「佣金转入余额」的一行。
type TransferView struct {
	LedgerTxnID uuid.UUID
	Amount      decimal.Decimal
	Currency    string
	CreatedAt   time.Time
}

// MyCommission 是门户 GET /me/commission 的全部内容。
type MyCommission struct {
	Summary     Summary
	Entries     []EntryView
	Withdrawals []WithdrawalView
	Transfers   []TransferView
}

// CurrencyOverview 是后台分销概览里某一币种的汇总。
type CurrencyOverview struct {
	Currency           money.Currency
	Pending            decimal.Decimal
	Available          decimal.Decimal
	PaidOut            decimal.Decimal
	ThisMonth          decimal.Decimal
	TotalEarned        decimal.Decimal
	Entries            int64
	NeedReview         int64
	WaitingWithdrawals int64
}

// Overview 是后台分销概览：金额字段是站点币种（Currency）那一组，笔数字段是全部币种合计；
// ByCurrency 按币种列出每一组（只列有数据的币种，站点币种总在里面）。
type Overview struct {
	Currency           money.Currency
	Pending            decimal.Decimal
	Available          decimal.Decimal
	PaidOut            decimal.Decimal
	ThisMonth          decimal.Decimal
	TotalEarned        decimal.Decimal
	Entries            int64
	NeedReview         int64
	WaitingWithdrawals int64
	InvitedUsers       int64
	ByCurrency         []CurrencyOverview
	RatePercent        int
	FreezeDays         int
	MinWithdraw        decimal.Decimal
	Scope              string
}

// AdminWithdrawal 是后台提现列表的一行；PayoutDetail 是解封后的收款信息，EarnedTotal 是该用户同币种的累计佣金。
type AdminWithdrawal struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	Email        string
	Amount       decimal.Decimal
	Currency     string
	Status       string
	PayoutDetail string
	RejectReason string
	RequestedAt  time.Time
	CompletedAt  *time.Time
	EarnedTotal  decimal.Decimal
}

// ConfigUpdate 是后台改分销参数的部分更新：nil 字段保持原值。MinWithdraw 是元。
type ConfigUpdate struct {
	RatePercent *int
	FreezeDays  *int
	MinWithdraw *decimal.Decimal
	Scope       *string
}

// SettleStats 是一轮解冻的统计。
type SettleStats struct {
	Scanned     int
	Settled     int
	Skipped     int
	Quarantined int // 确定性失败（待结科目余额不足）转人工复核的条数
	Failed      int
}

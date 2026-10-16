package service

import (
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// 佣金条目状态：pending 冻结中，available 已解冻入可提现，voided 复核作废（终态，计提已冲回）。
const (
	EntryPending   = "pending"
	EntryAvailable = "available"
	EntryVoided    = "voided"
)

// 提现状态：requested 申请中，approved 已批准待打款，rejected 已驳回，paid 已打款。
const (
	WithdrawalRequested = "requested"
	WithdrawalApproved  = "approved"
	WithdrawalRejected  = "rejected"
	WithdrawalPaid      = "paid"
)

// entryTransitions 是佣金条目的转换表。
// 复核通过不换状态（只清 review_required），所以不在表里；作废只对待复核的 pending 条目做，另由 WHERE 条件把关。
var entryTransitions = map[string][]string{
	EntryPending: {EntryAvailable, EntryVoided},
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

// 审计动作与资源类型。
const (
	ActionWithdrawalRequested = "withdrawal.requested"
	ActionWithdrawalApprove   = "withdrawal.approve"
	ActionWithdrawalReject    = "withdrawal.reject"
	ActionWithdrawalPaid      = "withdrawal.paid"
	ActionTransferredToBal    = "commission.transferred_to_balance"
	ActionAutoTransferred     = "commission.auto_transferred_to_balance"
	ActionEntryApproved       = "commission.entry_approved"
	ActionEntryVoided         = "commission.entry_voided"

	ResourceWithdrawal        = "withdrawal"
	ResourceLedgerTransaction = "ledger_transaction"
	ResourceCommissionEntry   = "commission_entry"
)

// 账本来源类型。
const (
	SourceCommissionEntry    = "commission_entry"
	SourceWithdrawal         = "withdrawal"
	SourceCommissionTransfer = "commission_transfer"
	// SourceCommissionForfeiture 是注销时作废可提现佣金的冲回交易来源（commission_forfeitures 的一行）。
	SourceCommissionForfeiture = "commission_forfeiture"
)

// ReasonAccountDeleted 是账号注销时作废佣金、驳回提现记的原因（条目 void_reason、提现 reject_reason、
// commission_forfeitures.reason 同一个值；与 order 取消待付单的原因同名）。
const ReasonAccountDeleted = "account_deleted"

// reviewDeletedReason 是解冻时发现受益人已注销、转人工复核的原因（注销提交前一刻才计提、没赶上注销作废的条目），
// 后台只能作废、不能通过。
const reviewDeletedReason = "受益人账号已注销，请作废这条佣金"

// 列表条数上限。
const (
	MyEntriesLimit        = 100
	MyWithdrawalsLimit    = 50
	MyTransfersLimit      = 50
	AdminWithdrawalsLimit = 200
	// ReviewEntriesDefault、ReviewEntriesMax 是后台待复核列表的默认与最大每页条数。
	ReviewEntriesDefault = 50
	ReviewEntriesMax     = 100
	// SettleBatchDefault 是解冻任务一轮最多处理的条数。
	SettleBatchDefault = 500
)

// 文本长度上限。
const (
	MaxPayoutDetailRunes    = 500
	MaxRejectReasonRunes    = 500
	MaxPayoutReferenceRunes = 128
	MaxEntryReviewRunes     = 500
	// MinVoidReasonRunes 是作废原因的最少字数（库里有同样的 CHECK）。
	MinVoidReasonRunes = 5
)

// 对外错误。
var (
	ErrNoCommission         = errs.Newf(errs.ValidationFailed, "没有可提现的佣金")
	ErrWithdrawTooSmall     = errs.Newf(errs.ValidationFailed, "提现金额低于最低限额")
	ErrWithdrawTooMuch      = errs.Newf(errs.Conflict, "提现金额超过可提现余额")
	ErrWithdrawPending      = errs.Newf(errs.Conflict, "还有正在处理的提现申请")
	ErrTransferInsufficient = errs.Newf(errs.Conflict, "可提现佣金不足。冻结期内的佣金要等解冻后才能转出，提现处理中的金额也不能再转")
	ErrWithdrawalHandled    = errs.Newf(errs.Conflict, "该提现申请已被处理过")
	ErrWithdrawalNotPayable = errs.Newf(errs.Conflict, "只有已批准的提现才能标记为已打款")
	ErrWithdrawalNotFound   = errs.Newf(errs.NotFound, "提现申请不存在")
	ErrEntryNotFound        = errs.Newf(errs.NotFound, "佣金条目不存在")
	// ErrAccountDeleted 是佣金的受益人或提现的申请人已注销：注销时名下未打款的佣金与在途提现都已作废，不再出钱。
	ErrAccountDeleted = errs.Newf(errs.Conflict, "账号已注销")
	// ErrApprovedWithdrawalPending 是注销时他还有已批准、没标记打款的提现（用户 10-14 定：这种情况不许注销，
	// 免得线下已打款的钱被驳回成平台收入、之后标记打款又对不上账）。
	ErrApprovedWithdrawalPending = errs.Newf(errs.Conflict, "该用户有已批准、还没标记打款的提现，先在佣金页把它标记为已打款，再注销")
	// ErrEntryNotReviewable 是条目不在待复核状态（已复核过、已解冻或已作废）：重放与并发的第二次都落在这里。
	ErrEntryNotReviewable = errs.Newf(errs.Conflict, "这条佣金不在待复核状态，可能已被处理，请刷新")
	// ErrVoidUnderfunded 是推荐人的待结佣金科目不够冲回这一条（账本与条目对不上，需要人工核对账本）。
	ErrVoidUnderfunded = errs.Newf(errs.Conflict, "待结佣金科目余额不足，无法冲回这条佣金，请先核对账本")
	// ErrWithdrawDisabled 是提现已关闭：佣金解冻后自动转入余额，不能申请提现。
	ErrWithdrawDisabled = errs.Newf(errs.Conflict, "提现已关闭：佣金解冻后将自动转入余额，不能申请提现")
)

// Summary 是门户「我的佣金」的汇总。
type Summary struct {
	Pending         decimal.Decimal // 冻结中
	Available       decimal.Decimal // 可提现（已解冻 − 未驳回的提现 − 已转余额 − 注销作废，不小于 0）
	Withdrawing     decimal.Decimal // 提现处理中（申请中、已批准）
	Settled         decimal.Decimal // 已打款
	TotalEarned     decimal.Decimal // 累计佣金（含冻结中）
	Invitees        int64           // 邀请人数
	Orders          int64           // 佣金条目数（含第 2、3 级）
	PaidInvitees    int64           // 产生过佣金的直接邀请人数（只算第 1 级）
	RatePercent     int
	MinWithdraw     decimal.Decimal
	Scope           string
	DirectRateBP    int64    // 直接邀请人实得的万分比，横幅用
	WithdrawEnabled bool     // 是否开放提现
	WithdrawMethods []string // 可选的提现方式，按目录顺序
}

// EntryView 是门户佣金明细的一行；From 是打码后的被邀请人邮箱，只有第 1 级（直接邀请）有，第 2、3 级为空。
type EntryView struct {
	Amount       decimal.Decimal
	Base         decimal.Decimal
	RateBP       int64
	Status       string
	FrozenUntil  time.Time
	CreatedAt    time.Time
	OrderNo      string
	From         string
	Level        int
	SharePercent int
}

// WithdrawalView 是门户提现记录的一行。
type WithdrawalView struct {
	ID           uuid.UUID
	Amount       decimal.Decimal
	Status       string
	RejectReason string
	RequestedAt  time.Time
	CompletedAt  *time.Time
	Method       string
}

// TransferView 是门户「佣金转入余额」的一行。
type TransferView struct {
	LedgerTxnID uuid.UUID
	Amount      decimal.Decimal
	CreatedAt   time.Time
}

// MyCommission 是门户 GET /me/commission 的全部内容。
type MyCommission struct {
	Summary     Summary
	Entries     []EntryView
	Withdrawals []WithdrawalView
	Transfers   []TransferView
}

// Overview 是后台分销概览：金额为全站合计。
type Overview struct {
	Pending             decimal.Decimal
	Available           decimal.Decimal
	PaidOut             decimal.Decimal
	ThisMonth           decimal.Decimal
	TotalEarned         decimal.Decimal
	Entries             int64
	NeedReview          int64
	WaitingWithdrawals  int64
	InvitedUsers        int64
	RatePercent         int
	FreezeDays          int
	MinWithdraw         decimal.Decimal
	Scope               string
	DistributionEnabled bool
	DistributionLevels  [3]int
	WithdrawEnabled     bool
	WithdrawMethods     []string
}

// AdminWithdrawal 是后台提现列表的一行；PayoutDetail 是解封后的收款信息，EarnedTotal 是该用户的累计佣金。
type AdminWithdrawal struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	Email           string
	Amount          decimal.Decimal
	Status          string
	Method          string
	PayoutDetail    string
	PayoutReference string // 没有为空串
	PaidUSDTAmount  *decimal.Decimal
	RejectReason    string
	RequestedAt     time.Time
	CompletedAt     *time.Time
	EarnedTotal     decimal.Decimal
}

// ConfigUpdate 是后台改分销参数的部分更新：nil 字段保持原值；分销与提现两组写进 settings 的各自一组键。MinWithdraw 是元。
type ConfigUpdate struct {
	RatePercent         *int
	FreezeDays          *int
	MinWithdraw         *decimal.Decimal
	Scope               *string
	DistributionEnabled *bool
	DistributionL1      *int
	DistributionL2      *int
	DistributionL3      *int
	WithdrawEnabled     *bool
	WithdrawMethods     *[]string
}

// SettleStats 是一轮解冻的统计。
type SettleStats struct {
	Scanned         int
	Settled         int
	Skipped         int
	Quarantined     int // 确定性失败（待结科目余额不足）转人工复核的条数
	Failed          int
	AutoTransferred int // 提现关闭时解冻、随后在单独事务里自动转入余额的条数（含上一轮转失败、这一轮补转的）
}

// 待复核的来源（由 review_reason 归类，给后台区分提示）：同源嫌疑、同源检查失败或未装配、解冻时待结科目余额不足、
// 受益人已注销（只能作废，通过会回 409）、其它。
const (
	ReviewSourceSameSource     = "same_source"
	ReviewSourceSourceCheck    = "source_check"
	ReviewSourceUnderfunded    = "underfunded"
	ReviewSourceAccountDeleted = "account_deleted"
	ReviewSourceOther          = "other"
)

// ReviewEntryRow 是后台待复核列表的一行；邮箱不打码（管理员核对用）。
type ReviewEntryRow struct {
	ID            uuid.UUID
	OrderNo       string
	ReferrerEmail string
	RefereeEmail  string
	Amount        decimal.Decimal
	ReviewReason  string
	ReviewSource  string // ReviewSource* 之一
	CreatedAt     time.Time
	FrozenUntil   time.Time
}

// ReviewEntryPage 是后台待复核列表的一页；NextCursor 为空表示没有下一页。
type ReviewEntryPage struct {
	Entries    []ReviewEntryRow
	NextCursor string
}

// VoidResult 是账号注销时作废佣金的结果（只有人民币）。
type VoidResult struct {
	VoidedEntries       int             // 作废的冻结中、待复核条目数
	VoidedPending       decimal.Decimal // 上面这些条目的金额合计（冲回待结佣金）
	Forfeited           decimal.Decimal // 作废的可提现佣金（冲回可提现佣金）
	RejectedWithdrawals int             // 驳回的申请中提现笔数
	RejectedAmount      decimal.Decimal // 驳回的提现金额合计（驳回后退回可提现、随即一并作废，已含在 Forfeited 里）
}

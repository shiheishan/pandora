package service

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
)

// 卡型。
const (
	TypeGeneral = "general"
	TypePlan    = "plan"
	TypeMystery = "mystery"
)

// 模板状态。
const (
	TemplateActive   = "active"
	TemplatePaused   = "paused"
	TemplateArchived = "archived"
)

// 卡密状态。
const (
	CodeUnused   = "unused"
	CodeUsed     = "used"
	CodeDisabled = "disabled"
	CodeExpired  = "expired"
)

// 套餐卡的落地方式（写进兑换流水的 plan_mode）。
const (
	PlanGrantNew     = "new"
	PlanGrantRenewed = "renewed"
	PlanGrantChanged = "changed"
)

// Prize 是盲盒奖池里的一项；Weight 是相对权重（70/20/10 与 7/2/1 等价）。零值表示不送。
type Prize struct {
	Label        string
	Weight       int
	Balance      decimal.Decimal
	TrafficBytes int64
	ExpireDays   int
}

// Rewards 是一张卡送什么，按卡型解释：general 用前四项，plan 用 PlanID / PriceID（两者都必填），mystery 用 Pool。
// 余额零值表示不送。
type Rewards struct {
	Balance      decimal.Decimal
	TrafficBytes int64
	ExpireDays   int
	ResetQuota   bool
	PlanID       *uuid.UUID
	PriceID      *uuid.UUID
	Pool         []Prize
}

// Conditions 是领取条件。
type Conditions struct {
	NewUserOnly    bool
	PaidUserOnly   bool
	AllowedPlanIDs []uuid.UUID
	RequireInvite  bool
}

// Limits 是使用限制：0 表示不限。
type Limits struct {
	MaxUsePerUser int
	CooldownHours int
}

// Template 是一个礼品卡模板（后台视图）。
type Template struct {
	ID          uuid.UUID
	Name        string
	Description string
	Type        string
	Status      string
	Rewards     Rewards
	Conditions  Conditions
	Limits      Limits
	ThemeColor  string
	// PriceArchived 是套餐卡指向的价格档已下架（归档或不存在）：后台标「价格已下架」，兑换会被拒，编辑时必须重选在售价
	PriceArchived bool
	// CodeTotal、CodeUsed 是这个模板下的码数与已兑换数
	CodeTotal int64
	CodeUsed  int64
	CreatedAt time.Time
}

// TemplateInput 是新建或修改模板的输入；ID 为 nil 是新建。卡型建好后不能改。
type TemplateInput struct {
	ID          *uuid.UUID
	Name        string
	Description string
	Type        string
	// Status 空串按 active
	Status     string
	Rewards    Rewards
	Conditions Conditions
	Limits     Limits
	ThemeColor string
}

// Batch 是一个生码批次的后台视图。
type Batch struct {
	ID              uuid.UUID
	TemplateID      uuid.UUID
	TemplateName    string
	Prefix          string
	Count           int
	Used            int
	Disabled        int
	ExpiresAt       *time.Time
	CreatedByEmail  *string
	CreatedAt       time.Time
	ExportedAt      *time.Time
	ExportedByEmail *string
}

// GenerateInput 是批量生码的输入。Reply 由 handler 提供：把结果做成幂等可重放的响应（不含明文样例），
// service 在生码事务里调用它并完成幂等认领；ctx 里没有认领时不调用。
type GenerateInput struct {
	TemplateID uuid.UUID
	Count      int
	Prefix     string
	ExpiresAt  *time.Time
	Reply      func(Generated) (idemsvc.Response, error)
}

// Generated 是生码结果。Sample 是前几张明文（只在这次响应里出现），完整明文只能走一次性导出。
type Generated struct {
	BatchID uuid.UUID
	Count   int
	Sample  []string
	Batch   Batch
}

// BatchFilter 是批次列表的筛选与分页。
type BatchFilter struct {
	TemplateID *uuid.UUID
	Limit      int
	Offset     int
}

// ExportRow 是一次性导出文件的一行，只在 ExportBatch 的返回值里出现明文。
type ExportRow struct {
	Code         string
	Status       string
	ExpiresAt    *time.Time
	TemplateName string
}

// BatchExport 是一次性导出的结果。
type BatchExport struct {
	Batch Batch
	Rows  []ExportRow
}

// CodeFilter 是卡密列表与掩码报表共用的筛选：两处口径一致，运营在列表里看到多少行，导出的就是那些行。
// Status 为空不筛；否则必须是四种状态之一。
type CodeFilter struct {
	TemplateID *uuid.UUID
	BatchID    *uuid.UUID
	Status     string
}

// Code 是卡密列表的一行，只有掩码。
type Code struct {
	ID         uuid.UUID
	CodeMasked string
	Status     string
	BatchID    uuid.UUID
	ExpiresAt  *time.Time
	UsedEmail  string
	UsedAt     *time.Time
	CreatedAt  time.Time
	TemplateID uuid.UUID
}

// CodeReportRow 是掩码报表的一行。
type CodeReportRow struct {
	CodeMasked   string
	Status       string
	TemplateName string
	BatchID      uuid.UUID
	ExpiresAt    *time.Time
	CreatedAt    time.Time
	UsedEmail    string
	UsedAt       *time.Time
}

// Stats 是后台礼品卡统计。
type Stats struct {
	Templates   int64
	CodesTotal  int64
	CodesUsed   int64
	CodesUnused int64
	// BalanceOut 已兑换发出的余额合计
	BalanceOut decimal.Decimal
	// TrafficOut 已兑换发出的流量合计（字节）
	TrafficOut int64
	// BalanceIssued 已发行通用卡的面额合计：每张码按所属模板当前的余额奖励计，不论是否兑换、停用或过期
	BalanceIssued decimal.Decimal
}

// Granted 是一次兑换实际发出去的东西。
type Granted struct {
	Balance      decimal.Decimal
	TrafficBytes int64
	ExpireDays   int
	QuotaReset   bool
}

// Usage 是后台使用明细的一行。
type Usage struct {
	TemplateName string
	CodeMasked   string
	UserEmail    string
	Granted      Granted
	PrizeLabel   string
	RedeemedAt   time.Time
}

// Choice 是用户在预览给出的选项里选定的用法；Kind 为空表示没选。
type Choice struct {
	Kind           string
	SubscriptionID *uuid.UUID
}

// RedeemInput 是兑换的输入。Reply 同 GenerateInput.Reply：service 在兑换事务里调用它并完成幂等认领。
type RedeemInput struct {
	Code   string
	Choice *Choice
	Reply  func(RedeemResult) (idemsvc.Response, error)
}

// RedeemResult 是兑换回执。
type RedeemResult struct {
	TemplateName string
	Type         string
	PrizeLabel   string
	Balance      decimal.Decimal
	TrafficBytes int64
	ExpireDays   int
	QuotaReset   bool
	PlanGranted  string
	// Summary 是给用户看的人话，永不为空
	Summary []string
}

// CardPreview 是门户兑换前看到的卡面：模板里用户该看的部分（不含发行量）。盲盒只回奖品名，不回权重与奖励。
type CardPreview struct {
	ID          uuid.UUID
	Name        string
	Description string
	Type        string
	Status      string
	Rewards     Rewards
	Conditions  Conditions
	Limits      Limits
	ThemeColor  string
	CreatedAt   time.Time
	// PlanName、PlanKind、Interval、IntervalCount、ValidityDays 只给套餐卡
	// PlanKind 是套餐类型（catalog 的 "subscription" / "one_time"）；ValidityDays 只给一次性套餐的价格：有效天数，nil 为永久
	PlanName      string
	PlanKind      string
	Interval      string
	IntervalCount int
	ValidityDays  *int
	// Placement 是这张卡在当前用户名下怎么用；纯余额卡为 nil
	Placement *CardPlacement
}

// CardPlacement 是兑换卡的落点问题。DefaultKey 为空表示不预选。
type CardPlacement struct {
	Question   string
	Options    []Placement
	DefaultKey string
}

// MyRedemption 是门户「我的兑换记录」的一行。CodeHint 是卡密掩码。
type MyRedemption struct {
	TemplateName string
	Type         string
	CodeHint     string
	PrizeLabel   string
	Balance      decimal.Decimal
	TrafficBytes int64
	ExpireDays   int
	RedeemedAt   time.Time
}

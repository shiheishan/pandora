// Package model 是 giftcard 模块的 GORM 结构体：只有字段和标签，表结构见迁移 00015_giftcard.sql。
package model

import (
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// Template 是 gift_card_templates 表的一行。
type Template struct {
	ID            uuid.UUID        `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Name          string           `gorm:"column:name"`
	Description   string           `gorm:"column:description"`
	Type          string           `gorm:"column:type"`
	Status        string           `gorm:"column:status"`
	BalanceAmount *decimal.Decimal `gorm:"column:balance_amount"`
	TrafficBytes  *int64           `gorm:"column:traffic_bytes"`
	ExpireDays    *int             `gorm:"column:expire_days"`
	ResetQuota    bool             `gorm:"column:reset_quota"`
	PlanID        *uuid.UUID       `gorm:"column:plan_id;type:uuid"`
	PriceID       *uuid.UUID       `gorm:"column:price_id;type:uuid"`
	NewUserOnly   bool             `gorm:"column:new_user_only"`
	PaidUserOnly  bool             `gorm:"column:paid_user_only"`
	RequireInvite bool             `gorm:"column:require_invite"`
	MaxUsePerUser int              `gorm:"column:max_use_per_user"`
	CooldownHours int              `gorm:"column:cooldown_hours"`
	ThemeColor    string           `gorm:"column:theme_color"`
	CreatedBy     *uuid.UUID       `gorm:"column:created_by;type:uuid"`
	CreatedAt     time.Time        `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt     time.Time        `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Template) TableName() string { return "gift_card_templates" }

// Prize 是 gift_card_prizes 表的一行：盲盒奖池里的一个奖品。
type Prize struct {
	TemplateID    uuid.UUID        `gorm:"column:template_id;type:uuid;primaryKey"`
	Position      int              `gorm:"column:position;primaryKey"`
	Label         string           `gorm:"column:label"`
	Weight        int              `gorm:"column:weight"`
	BalanceAmount *decimal.Decimal `gorm:"column:balance_amount"`
	TrafficBytes  *int64           `gorm:"column:traffic_bytes"`
	ExpireDays    *int             `gorm:"column:expire_days"`
}

// TableName 固定表名。
func (Prize) TableName() string { return "gift_card_prizes" }

// TemplatePlan 是 gift_card_template_plans 表的一行：模板限定的一个套餐。
type TemplatePlan struct {
	TemplateID uuid.UUID `gorm:"column:template_id;type:uuid;primaryKey"`
	PlanID     uuid.UUID `gorm:"column:plan_id;type:uuid;primaryKey"`
}

// TableName 固定表名。
func (TemplatePlan) TableName() string { return "gift_card_template_plans" }

// Batch 是 gift_card_batches 表的一行。
type Batch struct {
	ID          uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	TemplateID  uuid.UUID  `gorm:"column:template_id;type:uuid"`
	Prefix      string     `gorm:"column:prefix"`
	Count       int        `gorm:"column:count"`
	ExpiresAt   *time.Time `gorm:"column:expires_at"`
	CodesSealed []byte     `gorm:"column:codes_sealed"`
	CreatedBy   *uuid.UUID `gorm:"column:created_by;type:uuid"`
	CreatedAt   time.Time  `gorm:"column:created_at;autoCreateTime"`
	ExportedAt  *time.Time `gorm:"column:exported_at"`
	ExportedBy  *uuid.UUID `gorm:"column:exported_by;type:uuid"`
}

// TableName 固定表名。
func (Batch) TableName() string { return "gift_card_batches" }

// Code 是 gift_card_codes 表的一行。
type Code struct {
	ID         uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	TemplateID uuid.UUID  `gorm:"column:template_id;type:uuid"`
	BatchID    uuid.UUID  `gorm:"column:batch_id;type:uuid"`
	CodeHash   []byte     `gorm:"column:code_hash"`
	CodeMask   string     `gorm:"column:code_mask"`
	Status     string     `gorm:"column:status"`
	ExpiresAt  *time.Time `gorm:"column:expires_at"`
	UsedBy     *uuid.UUID `gorm:"column:used_by;type:uuid"`
	UsedAt     *time.Time `gorm:"column:used_at"`
	CreatedAt  time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt  time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Code) TableName() string { return "gift_card_codes" }

// Redemption 是 gift_card_redemptions 表的一行（只追加）。
type Redemption struct {
	ID             uuid.UUID        `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	CodeID         uuid.UUID        `gorm:"column:code_id;type:uuid"`
	TemplateID     uuid.UUID        `gorm:"column:template_id;type:uuid"`
	UserID         uuid.UUID        `gorm:"column:user_id;type:uuid"`
	PrizeLabel     *string          `gorm:"column:prize_label"`
	BalanceAmount  *decimal.Decimal `gorm:"column:balance_amount"`
	LedgerTxnID    *uuid.UUID       `gorm:"column:ledger_txn_id;type:uuid"`
	TrafficBytes   *int64           `gorm:"column:traffic_bytes"`
	ExpireDays     *int             `gorm:"column:expire_days"`
	QuotaReset     bool             `gorm:"column:quota_reset"`
	PlanID         *uuid.UUID       `gorm:"column:plan_id;type:uuid"`
	PlanMode       *string          `gorm:"column:plan_mode"`
	SubscriptionID *uuid.UUID       `gorm:"column:subscription_id;type:uuid"`
	RefundAmount   *decimal.Decimal `gorm:"column:refund_amount"`
	RedeemedAt     time.Time        `gorm:"column:redeemed_at;autoCreateTime"`
}

// TableName 固定表名。
func (Redemption) TableName() string { return "gift_card_redemptions" }

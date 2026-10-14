package repo

import (
	"time"

	"github.com/google/uuid"
)

// CredentialRow 是凭据的读形状（不含密文），带引用它的证书数。
type CredentialRow struct {
	ID               uuid.UUID  `gorm:"column:id"`
	Name             string     `gorm:"column:name"`
	Provider         string     `gorm:"column:provider"`
	Zone             string     `gorm:"column:zone"`
	SecretHint       string     `gorm:"column:secret_hint"`
	VerifyStatus     string     `gorm:"column:verify_status"`
	VerifiedAt       *time.Time `gorm:"column:verified_at"`
	VerifyError      *string    `gorm:"column:verify_error"`
	VisibleZones     *int       `gorm:"column:visible_zones"`
	RowVersion       int64      `gorm:"column:row_version"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
	UpdatedAt        time.Time  `gorm:"column:updated_at"`
	CertificateCount int        `gorm:"column:certificate_count"`
}

// CredentialUpdate 是改凭据要写的列。SecretSealed 为 nil 表示不换密钥。Reverify 为真时
// 校验结论回到 unverified（清 verified_at、verify_error、visible_zones）。
type CredentialUpdate struct {
	Name         string
	Zone         string
	SecretSealed []byte
	SecretHint   string
	Reverify     bool
}

// CredentialVerdict 是一次校验（或签发预检）的结论。Status 是 ok / error；VisibleZones 为 nil 时保留原值。
type CredentialVerdict struct {
	Status       string
	VerifyError  *string
	VisibleZones *int
}

// CertificateRow 是证书的读形状：证书列、凭据名与提供方、当前版本号与 CA、进行中的订单。
type CertificateRow struct {
	ID                  uuid.UUID  `gorm:"column:id"`
	Name                string     `gorm:"column:name"`
	Identifiers         string     `gorm:"column:identifiers"`
	Challenge           string     `gorm:"column:challenge"`
	DNSCredentialID     uuid.UUID  `gorm:"column:dns_credential_id"`
	DNSCredentialName   string     `gorm:"column:dns_credential_name"`
	DNSProvider         string     `gorm:"column:dns_provider"`
	KeyType             string     `gorm:"column:key_type"`
	Status              string     `gorm:"column:status"`
	PausedReason        *string    `gorm:"column:paused_reason"`
	CurrentVersion      *int       `gorm:"column:current_version"`
	CurrentCA           *string    `gorm:"column:current_ca"`
	NotBefore           *time.Time `gorm:"column:not_before"`
	NotAfter            *time.Time `gorm:"column:not_after"`
	RenewAfter          *time.Time `gorm:"column:renew_after"`
	ARIWindowStart      *time.Time `gorm:"column:ari_window_start"`
	ARIWindowEnd        *time.Time `gorm:"column:ari_window_end"`
	NextAttemptAt       *time.Time `gorm:"column:next_attempt_at"`
	ConsecutiveFailures int        `gorm:"column:consecutive_failures"`
	LastErrorCode       *string    `gorm:"column:last_error_code"`
	LastError           *string    `gorm:"column:last_error"`
	OrderID             *uuid.UUID `gorm:"column:order_id"`
	OrderState          *string    `gorm:"column:order_state"`
	OrderReason         *string    `gorm:"column:order_reason"`
	OrderCreatedAt      *time.Time `gorm:"column:order_created_at"`
	RowVersion          int64      `gorm:"column:row_version"`
	CreatedAt           time.Time  `gorm:"column:created_at"`
	UpdatedAt           time.Time  `gorm:"column:updated_at"`
}

// CertificateSpec 是改证书要写的列。CredentialChanged 为真且证书停在 blocked_credential 时恢复
// （没签出过回 pending，签出过回 active）；CredentialChanged 或 Reissue 为真时清掉退避（next_attempt_at）。
type CertificateSpec struct {
	Name              string
	Identifiers       string // JSON 数组
	DNSCredentialID   uuid.UUID
	KeyType           string
	CredentialChanged bool
	Reissue           bool
}

// VersionRow 是版本的读形状（不含私钥密文）。Current 表示它是证书的当前版本。
type VersionRow struct {
	ID              uuid.UUID `gorm:"column:id"`
	Version         int       `gorm:"column:version"`
	CA              string    `gorm:"column:ca"`
	Identifiers     string    `gorm:"column:identifiers"`
	IsRenewal       bool      `gorm:"column:is_renewal"`
	Serial          string    `gorm:"column:serial"`
	NotBefore       time.Time `gorm:"column:not_before"`
	NotAfter        time.Time `gorm:"column:not_after"`
	ChainSHA256     []byte    `gorm:"column:chain_sha256"`
	PublicKeySHA256 []byte    `gorm:"column:public_key_sha256"`
	Current         bool      `gorm:"column:current"`
	CreatedAt       time.Time `gorm:"column:created_at"`
}

// OrderRow 是订单的读形状；Version 是签出的版本号（没签出为 nil）。
type OrderRow struct {
	ID          uuid.UUID  `gorm:"column:id"`
	Reason      string     `gorm:"column:reason"`
	State       string     `gorm:"column:state"`
	CA          *string    `gorm:"column:ca"`
	Replaces    bool       `gorm:"column:replaces"`
	Attempt     int        `gorm:"column:attempt"`
	ErrorCode   *string    `gorm:"column:error_code"`
	ErrorDetail *string    `gorm:"column:error_detail"`
	Version     *int       `gorm:"column:version"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	StartedAt   *time.Time `gorm:"column:started_at"`
	FinishedAt  *time.Time `gorm:"column:finished_at"`
}

// OrderBriefRow 是进行中的订单。
type OrderBriefRow struct {
	ID        uuid.UUID `gorm:"column:id"`
	State     string    `gorm:"column:state"`
	Reason    string    `gorm:"column:reason"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

// ClaimedOrder 是认领到的订单；Attempt 是这次认领的栅栏令牌。
type ClaimedOrder struct {
	ID            uuid.UUID `gorm:"column:id"`
	CertificateID uuid.UUID `gorm:"column:certificate_id"`
	Reason        string    `gorm:"column:reason"`
	ReplacesARIID *string   `gorm:"column:replaces_ari_id"`
	Attempt       int       `gorm:"column:attempt"`
}

// OrderWorkRow 是签发一张订单要读的证书侧信息：证书本身与当前版本（没有版本时 Current* 为 nil）。
type OrderWorkRow struct {
	Name               string     `gorm:"column:name"`
	Identifiers        string     `gorm:"column:identifiers"`
	KeyType            string     `gorm:"column:key_type"`
	Status             string     `gorm:"column:status"`
	DNSCredentialID    uuid.UUID  `gorm:"column:dns_credential_id"`
	CurrentIdentifiers *string    `gorm:"column:current_identifiers"`
	CurrentCA          *string    `gorm:"column:current_ca"`
	CurrentAccountID   *uuid.UUID `gorm:"column:current_account_id"`
	NotAfter           *time.Time `gorm:"column:not_after"`
}

// FailureUpdate 是一次失败要写到证书上的列：连续失败数、退避多久、错误、是否转暂停（paused_reason = failures）。
type FailureUpdate struct {
	Failures int
	RetryIn  time.Duration
	Code     string
	Detail   string
	Pause    bool
}

// SuccessUpdate 是签出新版本后要写到证书上的列。
type SuccessUpdate struct {
	VersionID  uuid.UUID
	NotBefore  time.Time
	NotAfter   time.Time
	RenewAfter time.Time
}

// ARIDueRow 是该刷新 ARI 窗口的在用证书及其当前版本。
type ARIDueRow struct {
	CertificateID uuid.UUID `gorm:"column:certificate_id"`
	VersionID     uuid.UUID `gorm:"column:version_id"`
	CA            string    `gorm:"column:ca"`
	ChainPEM      string    `gorm:"column:chain_pem"`
}

// AlertRow 是到期等级可能上升的证书。
type AlertRow struct {
	ID           uuid.UUID `gorm:"column:id"`
	Name         string    `gorm:"column:name"`
	NotBefore    time.Time `gorm:"column:not_before"`
	NotAfter     time.Time `gorm:"column:not_after"`
	AlertedLevel int       `gorm:"column:alerted_level"`
}

// IssuanceRow 是签发流水里过去一段时间的一行。
type IssuanceRow struct {
	CA          string    `gorm:"column:ca"`
	Identifiers string    `gorm:"column:identifiers"`
	IsRenewal   bool      `gorm:"column:is_renewal"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

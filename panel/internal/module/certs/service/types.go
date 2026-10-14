package service

import (
	"time"

	"github.com/google/uuid"
)

// 证书状态（certificates.status）。
const (
	StatusPending           = "pending"
	StatusActive            = "active"
	StatusPaused            = "paused"
	StatusBlockedCredential = "blocked_credential"
)

// 暂停原因（certificates.paused_reason）。
const (
	PausedManual   = "manual"
	PausedFailures = "failures"
)

// 订单状态（certificate_orders.state）。
const (
	OrderQueued    = "queued"
	OrderRunning   = "running"
	OrderSucceeded = "succeeded"
	OrderFailed    = "failed"
	OrderCancelled = "cancelled"
)

// 订单原因（certificate_orders.reason）。
const (
	ReasonInitial = "initial"
	ReasonRenewal = "renewal"
	ReasonARI     = "ari"
	ReasonManual  = "manual"
)

// 密钥类型（certificates.key_type）。
const (
	KeyECDSAP256 = "ecdsa-p256"
	KeyRSA2048   = "rsa-2048"
)

// 凭据校验状态（dns_credentials.verify_status）。
const (
	VerifyUnverified = "unverified"
	VerifyOK         = "ok"
	VerifyError      = "error"
)

// Certificate 是证书的读形状（列表与详情共用）。没有任何密文字段。
type Certificate struct {
	ID                  uuid.UUID
	Name                string
	Identifiers         []string
	Wildcard            bool
	Challenge           string
	DNSCredentialID     uuid.UUID
	DNSCredentialName   string
	DNSProvider         string
	KeyType             string
	Status              string
	PausedReason        *string
	CurrentVersion      *int
	CurrentCA           *string
	NotBefore           *time.Time
	NotAfter            *time.Time
	RenewAfter          *time.Time
	ARIWindowStart      *time.Time
	ARIWindowEnd        *time.Time
	NextAttemptAt       *time.Time
	ConsecutiveFailures int
	LastErrorCode       *string
	LastError           *string
	ExpiryLevel         ExpiryLevel
	// ActiveOrder 是进行中（排队或签发中）的订单；没有为 nil
	ActiveOrder *OrderBrief
	RowVersion  int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// OrderBrief 是进行中的订单。
type OrderBrief struct {
	ID        uuid.UUID
	State     string
	Reason    string
	CreatedAt time.Time
}

// CertificateSummary 是后台横幅的计数（到期与失败只在后台显示）。
type CertificateSummary struct {
	Total    int
	Warning  int
	Critical int
	Expired  int
	// Failing 是最近一次签发失败、还没成功过的证书数（含凭据失效）
	Failing int
	Paused  int
	Blocked int
}

// CertificateList 是证书列表与横幅计数。
type CertificateList struct {
	Items   []Certificate
	Summary CertificateSummary
}

// CertificateInput 是新建证书。KeyType 空串取 ecdsa-p256。
type CertificateInput struct {
	Name            string
	Identifiers     []string
	DNSCredentialID string
	KeyType         string
}

// CertificatePatch 是修改证书：nil 的字段不改。改了标识或密钥类型就排一张新订单。
type CertificatePatch struct {
	Name            *string
	Identifiers     []string // nil 表示不改
	DNSCredentialID *string
	KeyType         *string
}

// Version 是证书版本的读形状：链与指纹，没有私钥。
type Version struct {
	ID              uuid.UUID
	Version         int
	CA              string
	Identifiers     []string
	IsRenewal       bool
	Serial          string
	NotBefore       time.Time
	NotAfter        time.Time
	ChainSHA256     string // 十六进制
	PublicKeySHA256 string // 十六进制
	Current         bool
	CreatedAt       time.Time
}

// Order 是签发订单的读形状（详情页的订单历史）。
type Order struct {
	ID          uuid.UUID
	Reason      string
	State       string
	CA          *string
	Replaces    bool
	Attempt     int
	ErrorCode   *string
	ErrorDetail *string
	Version     *int
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
}

// CertificateDetail 是一张证书、全部版本（新的在前）与最近 50 张订单。
type CertificateDetail struct {
	Certificate Certificate
	Versions    []Version
	Orders      []Order
}

// DNSCredential 是凭据的读形状：只有末四位提示，没有任何密文字段。
type DNSCredential struct {
	ID           uuid.UUID
	Name         string
	Provider     string
	Zone         string
	SecretHint   string
	VerifyStatus string
	VerifiedAt   *time.Time
	VerifyError  *string
	VisibleZones *int
	// CertificateCount 是引用它的证书数；大于 0 时不能删
	CertificateCount int
	RowVersion       int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// DNSCredentialInput 是新建凭据；Secret 的字段按提供方（providerFields）。
type DNSCredentialInput struct {
	Name     string
	Provider string
	Zone     string
	Secret   map[string]string
}

// DNSCredentialPatch 是修改凭据：nil 的字段（含 Secret 里缺席的键）保留原值；提供方不能改。
type DNSCredentialPatch struct {
	Name   *string
	Zone   *string
	Secret map[string]string
}

// VerifyResult 是一次校验的结论，连同校验后的凭据一起回。
type VerifyResult struct {
	Credential DNSCredential
	OK         bool
	// Message 是失败原因（成功时为空）
	Message  string
	Warnings []string
	// Resumed 是因为这次校验通过而恢复自动签发的证书数
	Resumed int
}

// ACMESettings 是后台「ACME 设置」的读形状，没有任何秘密字段。
type ACMESettings struct {
	ContactEmail   string
	UseStaging     bool
	ZeroSSLEnabled bool
	ZeroSSLEABKID  string
	// ZeroSSLEABHMACSet 只说 HMAC 配没配
	ZeroSSLEABHMACSet bool
}

// ACMESettingsInput 是整体保存。ZeroSSLEABHMAC：nil 保留原值，"" 清空，其余替换。
type ACMESettingsInput struct {
	ContactEmail   string
	UseStaging     bool
	ZeroSSLEnabled bool
	ZeroSSLEABKID  string
	ZeroSSLEABHMAC *string
}

// PatrolResult 是一轮巡检做了什么（给日志）。
type PatrolResult struct {
	Queued   int // 新排的到期订单
	Enqueued int // 入队的签发任务
	Expired  int // 租约过期、按中断记失败的订单
	Raised   int // 到期等级上升的证书
}

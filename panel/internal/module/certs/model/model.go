// Package model 是 certs 模块的 GORM 结构体（迁移 00025_certs.sql），只有字段和标签。
//
// identifiers 是 jsonb 字符串数组，用 string 承载 JSON 文本，由 service 编解码。
package model

import (
	"time"

	"github.com/google/uuid"
)

// DNSCredential 是 dns_credentials 表的一行。SecretSealed 是封存密文，只在 repo 与 service 之间流转。
type DNSCredential struct {
	ID           uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Name         string     `gorm:"column:name"`
	Provider     string     `gorm:"column:provider"`
	Zone         string     `gorm:"column:zone"`
	SecretSealed []byte     `gorm:"column:secret_sealed"`
	SecretHint   string     `gorm:"column:secret_hint"`
	VerifyStatus string     `gorm:"column:verify_status;default:unverified"`
	VerifiedAt   *time.Time `gorm:"column:verified_at"`
	VerifyError  *string    `gorm:"column:verify_error"`
	VisibleZones *int       `gorm:"column:visible_zones"`
	RowVersion   int64      `gorm:"column:row_version;default:1"`
	CreatedAt    time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt    time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (DNSCredential) TableName() string { return "dns_credentials" }

// ACMEAccount 是 acme_accounts 表的一行。
type ACMEAccount struct {
	ID               uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	CA               string    `gorm:"column:ca"`
	DirectoryURL     string    `gorm:"column:directory_url"`
	ContactEmail     string    `gorm:"column:contact_email"`
	AccountKeySealed []byte    `gorm:"column:account_key_sealed"`
	AccountURL       string    `gorm:"column:account_url"`
	EABKID           *string   `gorm:"column:eab_kid"`
	Status           string    `gorm:"column:status;default:valid"`
	CreatedAt        time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt        time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (ACMEAccount) TableName() string { return "acme_accounts" }

// Certificate 是 certificates 表的一行。
type Certificate struct {
	ID                  uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Name                string     `gorm:"column:name"`
	Identifiers         string     `gorm:"column:identifiers;type:jsonb"`
	Challenge           string     `gorm:"column:challenge;default:dns-01"`
	DNSCredentialID     uuid.UUID  `gorm:"column:dns_credential_id;type:uuid"`
	KeyType             string     `gorm:"column:key_type"`
	Status              string     `gorm:"column:status;default:pending"`
	PausedReason        *string    `gorm:"column:paused_reason"`
	CurrentVersionID    *uuid.UUID `gorm:"column:current_version_id;type:uuid"`
	NotBefore           *time.Time `gorm:"column:not_before"`
	NotAfter            *time.Time `gorm:"column:not_after"`
	RenewAfter          *time.Time `gorm:"column:renew_after"`
	ARIWindowStart      *time.Time `gorm:"column:ari_window_start"`
	ARIWindowEnd        *time.Time `gorm:"column:ari_window_end"`
	ARICheckedAt        *time.Time `gorm:"column:ari_checked_at"`
	NextAttemptAt       *time.Time `gorm:"column:next_attempt_at"`
	ConsecutiveFailures int        `gorm:"column:consecutive_failures"`
	LastErrorCode       *string    `gorm:"column:last_error_code"`
	LastError           *string    `gorm:"column:last_error"`
	AlertedLevel        int16      `gorm:"column:alerted_level"`
	RowVersion          int64      `gorm:"column:row_version;default:1"`
	CreatedBy           *uuid.UUID `gorm:"column:created_by;type:uuid"`
	CreatedAt           time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt           time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Certificate) TableName() string { return "certificates" }

// CertificateVersion 是 certificate_versions 表的一行（只插不改）。
type CertificateVersion struct {
	ID               uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	CertificateID    uuid.UUID  `gorm:"column:certificate_id;type:uuid"`
	Version          int        `gorm:"column:version"`
	ACMEAccountID    uuid.UUID  `gorm:"column:acme_account_id;type:uuid"`
	CA               string     `gorm:"column:ca"`
	Identifiers      string     `gorm:"column:identifiers;type:jsonb"`
	IsRenewal        bool       `gorm:"column:is_renewal"`
	Serial           string     `gorm:"column:serial"`
	ChainPEM         string     `gorm:"column:chain_pem"`
	PrivateKeySealed []byte     `gorm:"column:private_key_sealed"`
	PublicKeySHA256  []byte     `gorm:"column:public_key_sha256"`
	ChainSHA256      []byte     `gorm:"column:chain_sha256"`
	NotBefore        time.Time  `gorm:"column:not_before"`
	NotAfter         time.Time  `gorm:"column:not_after"`
	ARICertID        *string    `gorm:"column:ari_cert_id"`
	CertURL          *string    `gorm:"column:cert_url"`
	OrderID          *uuid.UUID `gorm:"column:order_id;type:uuid"`
	CreatedAt        time.Time  `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (CertificateVersion) TableName() string { return "certificate_versions" }

// CertificateIssuance 是 certificate_issuances 表的一行（只追加）。
type CertificateIssuance struct {
	ID            uuid.UUID `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	CertificateID uuid.UUID `gorm:"column:certificate_id;type:uuid"`
	CA            string    `gorm:"column:ca"`
	Identifiers   string    `gorm:"column:identifiers;type:jsonb"`
	IsRenewal     bool      `gorm:"column:is_renewal"`
	ARIReplaces   bool      `gorm:"column:ari_replaces"`
	Serial        string    `gorm:"column:serial"`
	CreatedAt     time.Time `gorm:"column:created_at;autoCreateTime"`
}

// TableName 固定表名。
func (CertificateIssuance) TableName() string { return "certificate_issuances" }

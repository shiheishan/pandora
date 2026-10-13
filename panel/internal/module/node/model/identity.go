package model

// 节点通道身份相关表（迁移 00020_node_identity.sql）。

import (
	"time"

	"github.com/google/uuid"
)

// NodeCA 是 node_ca 表的唯一一行（ID 恒为 1）。
type NodeCA struct {
	ID      int16  `gorm:"column:id;primaryKey"`
	CertPEM string `gorm:"column:cert_pem"`
	// KeySealed 是封存后的 PKCS#8 私钥
	KeySealed  []byte    `gorm:"column:key_sealed"`
	SPKISHA256 string    `gorm:"column:spki_sha256"`
	CreatedAt  time.Time `gorm:"column:created_at"`
}

// TableName 固定表名。
func (NodeCA) TableName() string { return "node_ca" }

// BootstrapToken 是 bootstrap_tokens 表的一行（只存令牌哈希）。
type BootstrapToken struct {
	ID            uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	ServerID      uuid.UUID  `gorm:"column:server_id;type:uuid"`
	TokenSHA256   string     `gorm:"column:token_sha256"`
	ExpiresAt     time.Time  `gorm:"column:expires_at"`
	UsedAt        *time.Time `gorm:"column:used_at"`
	UsedRequestID *string    `gorm:"column:used_request_id"`
	IssuedBy      *uuid.UUID `gorm:"column:issued_by;type:uuid"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
}

// TableName 固定表名。
func (BootstrapToken) TableName() string { return "bootstrap_tokens" }

// ServerIdentity 是 server_identities 表的一行：一张签发给服务器的客户端证书。
type ServerIdentity struct {
	ID              uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	ServerID        uuid.UUID  `gorm:"column:server_id;type:uuid"`
	Seq             int32      `gorm:"column:seq"`
	CertSerial      string     `gorm:"column:cert_serial"`
	SPKISHA256      string     `gorm:"column:spki_sha256"`
	CertPEM         string     `gorm:"column:cert_pem"`
	CSRSHA256       string     `gorm:"column:csr_sha256"`
	Source          string     `gorm:"column:source"`
	EnrollTokenID   *uuid.UUID `gorm:"column:enroll_token_id;type:uuid"`
	EnrollRequestID *string    `gorm:"column:enroll_request_id"`
	Status          string     `gorm:"column:status"`
	IssuedAt        time.Time  `gorm:"column:issued_at"`
	ExpiresAt       time.Time  `gorm:"column:expires_at"`
	SupersededAt    *time.Time `gorm:"column:superseded_at"`
	RevokedAt       *time.Time `gorm:"column:revoked_at"`
	RevokedReason   *string    `gorm:"column:revoked_reason"`
	CreatedAt       time.Time  `gorm:"column:created_at;default:now()"`
}

// TableName 固定表名。
func (ServerIdentity) TableName() string { return "server_identities" }

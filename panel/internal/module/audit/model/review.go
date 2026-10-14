package model

import (
	"time"

	"github.com/google/uuid"
)

// IPClusterReview 是 ip_cluster_reviews 的一行（迁移 00033_identity_ops.sql）：一个同源 IP 聚类当前的复核结论，
// 每个来源 IP 哈希只留一行。normal 有有效期（到期重新提示），disabled 没有。
type IPClusterReview struct {
	ID        uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	IPHash    []byte     `gorm:"column:ip_hash"`
	Decision  string     `gorm:"column:decision"`
	Note      string     `gorm:"column:note"`
	DecidedBy uuid.UUID  `gorm:"column:decided_by;type:uuid"`
	DecidedAt time.Time  `gorm:"column:decided_at"`
	ExpiresAt *time.Time `gorm:"column:expires_at"`
}

// TableName 固定表名。
func (IPClusterReview) TableName() string { return "ip_cluster_reviews" }

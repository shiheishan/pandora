package model

import (
	"time"

	"github.com/google/uuid"
)

// UserGenerationJob 是 user_generation_jobs 表的一行（迁移 00033_identity_ops.sql）：后台批量生成账号的任务。
// ResultEncrypted 是已生成账号的邮箱与初始口令（JSON 数组）的封存密文，AAD 绑定任务 id；只留 24 小时。
type UserGenerationJob struct {
	ID              uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	ActorID         uuid.UUID  `gorm:"column:actor_id;type:uuid"`
	Status          string     `gorm:"column:status;default:queued"`
	Total           int        `gorm:"column:total"`
	Completed       int        `gorm:"column:completed;default:0"`
	Failed          int        `gorm:"column:failed;default:0"`
	EmailPrefix     string     `gorm:"column:email_prefix"`
	EmailDomain     string     `gorm:"column:email_domain"`
	GroupID         *uuid.UUID `gorm:"column:group_id;type:uuid"`
	Reason          string     `gorm:"column:reason"`
	ResultEncrypted []byte     `gorm:"column:result_encrypted"`
	ResultExpiresAt *time.Time `gorm:"column:result_expires_at"`
	ResultPurgedAt  *time.Time `gorm:"column:result_purged_at"`
	Error           *string    `gorm:"column:error"`
	Attempts        int        `gorm:"column:attempts;default:0"`
	LeaseUntil      *time.Time `gorm:"column:lease_until"`
	CreatedAt       time.Time  `gorm:"column:created_at;autoCreateTime"`
	StartedAt       *time.Time `gorm:"column:started_at"`
	FinishedAt      *time.Time `gorm:"column:finished_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (UserGenerationJob) TableName() string { return "user_generation_jobs" }

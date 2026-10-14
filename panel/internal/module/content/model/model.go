// Package model 是 content 模块的 GORM 结构体，只有字段与标签。
package model

import (
	"time"

	"github.com/google/uuid"
)

// Page 是 content_pages 的一行（一个版本）。
// TargetPlatforms、TargetPlanIDs 是排好序、去重的 JSON 字符串数组原文（如 `["android","web"]`）。
type Page struct {
	ID               uuid.UUID  `gorm:"column:id;type:uuid;primaryKey;default:uuidv7()"`
	Slug             string     `gorm:"column:slug"`
	Kind             string     `gorm:"column:kind"`
	Category         string     `gorm:"column:category"`
	Version          int        `gorm:"column:version"`
	Title            string     `gorm:"column:title"`
	Summary          string     `gorm:"column:summary"`
	Body             string     `gorm:"column:body"`
	Locale           string     `gorm:"column:locale"`
	SanitizerVersion string     `gorm:"column:sanitizer_version;default:plain-v1"`
	TargetPlatforms  string     `gorm:"column:target_platforms;type:jsonb"`
	MinClientVersion string     `gorm:"column:min_client_version"`
	MaxClientVersion string     `gorm:"column:max_client_version"`
	TargetPlanIDs    string     `gorm:"column:target_plan_ids;type:jsonb"`
	Visibility       string     `gorm:"column:visibility"`
	Status           string     `gorm:"column:status"`
	ReviewDueAt      *time.Time `gorm:"column:review_due_at"`
	PublishedAt      *time.Time `gorm:"column:published_at"`
	CreatedBy        *uuid.UUID `gorm:"column:created_by;type:uuid"`
	CreatedAt        time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt        time.Time  `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Page) TableName() string { return "content_pages" }

// Feedback 是 content_page_feedback 的一行。
type Feedback struct {
	PageSlug    string    `gorm:"column:page_slug;primaryKey"`
	PageVersion int       `gorm:"column:page_version;primaryKey"`
	UserID      uuid.UUID `gorm:"column:user_id;type:uuid;primaryKey"`
	Helpful     bool      `gorm:"column:helpful"`
	CreatedAt   time.Time `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt   time.Time `gorm:"column:updated_at;autoUpdateTime"`
}

// TableName 固定表名。
func (Feedback) TableName() string { return "content_page_feedback" }

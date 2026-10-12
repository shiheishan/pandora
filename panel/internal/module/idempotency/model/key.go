package model

import (
	"time"

	"github.com/google/uuid"
)

// IdempotencyKey 对应表 idempotency_keys（GORM 默认命名即为该表名）。
type IdempotencyKey struct {
	ID                      uuid.UUID `gorm:"type:uuid;primaryKey;default:uuidv7()"`
	Scope                   string
	ActorID                 uuid.UUID `gorm:"type:uuid"`
	Key                     string    `gorm:"column:idempotency_key"`
	RequestHash             []byte
	Status                  string
	Generation              int64
	LockedUntil             *time.Time
	ResponseCode            *int
	Replayable              bool
	ResponseBody            []byte
	ResponseContentType     *string
	ResponseLocation        *string
	ResponseETag            *string `gorm:"column:response_etag"`
	ResponseCacheControl    *string
	ResponseContentLanguage *string
	ResourceType            *string
	ResourceID              *uuid.UUID `gorm:"type:uuid"`
	CreatedAt               time.Time
	CompletedAt             *time.Time
}

const (
	// StatusInFlight 表示认领后尚未完成。
	StatusInFlight = "in_flight"
	// StatusSucceeded 表示已成功完成且可查询结果。
	StatusSucceeded = "succeeded"
	// StatusFailed 表示已释放，可由同一请求接管重试。
	StatusFailed = "failed"
)

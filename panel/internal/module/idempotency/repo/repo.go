// Package repo 是 idempotency 模块唯一使用 GORM 的数据访问层。
package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/aegispanel/aegis/internal/module/idempotency/model"
	"github.com/aegispanel/aegis/internal/pkg/db"
)

// leaseSQL 写入认领/接管时的 locked_until 表达式，须与 service.LeaseDuration（5 分钟）一致。
const leaseSQL = "now() + interval '5 minutes'"

// Repo 访问 idempotency_keys 表。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// Finish 是完成或释放时写入的结果。
type Finish struct {
	Status                                                     string // model.StatusSucceeded 或 model.StatusFailed
	Code                                                       int
	Replayable                                                 bool
	Body                                                       []byte // Replayable 时非 nil（nil 归一成 []byte{}）；否则写 NULL
	ContentType, Location, ETag, CacheControl, ContentLanguage *string
	ResourceType                                               *string
	ResourceID                                                 *uuid.UUID
}

// Insert 尝试插入新认领行；返回是否插入成功。
func (r *Repo) Insert(ctx context.Context, scope string, actorID uuid.UUID, key string, hash []byte) (model.IdempotencyKey, bool, error) {
	conn := r.d.Conn(ctx)
	var row model.IdempotencyKey
	sql := "INSERT INTO idempotency_keys (scope, actor_id, idempotency_key, request_hash, locked_until) VALUES (?, ?, ?, ?, " + leaseSQL + ") ON CONFLICT (scope, actor_id, idempotency_key) DO NOTHING RETURNING *"
	res := conn.Raw(sql, scope, actorID, key, hash).Scan(&row)
	if res.Error != nil {
		return model.IdempotencyKey{}, false, res.Error
	}
	return row, res.RowsAffected == 1, nil
}

// LockForUpdate 在事务内锁定已有行。
func (r *Repo) LockForUpdate(ctx context.Context, scope string, actorID uuid.UUID, key string) (model.IdempotencyKey, error) {
	conn := r.d.Conn(ctx)
	var row model.IdempotencyKey
	err := conn.Clauses(clause.Locking{Strength: "UPDATE"}).Where("scope = ? AND actor_id = ? AND idempotency_key = ?", scope, actorID, key).Take(&row).Error
	return row, err
}

// Takeover 在 failed 状态下由同一请求接管执行权。
func (r *Repo) Takeover(ctx context.Context, id uuid.UUID, generation int64) (model.IdempotencyKey, bool, error) {
	conn := r.d.Conn(ctx)
	var row model.IdempotencyKey
	sql := "UPDATE idempotency_keys SET status = 'in_flight', generation = generation + 1, locked_until = " + leaseSQL + ", response_code = NULL, completed_at = NULL WHERE id = ? AND generation = ? AND status = 'failed' RETURNING *"
	res := conn.Raw(sql, id, generation).Scan(&row)
	if res.Error != nil {
		return model.IdempotencyKey{}, false, res.Error
	}
	return row, res.RowsAffected == 1, nil
}

// Finish 按 CAS 写入完成或释放结果。
func (r *Repo) Finish(ctx context.Context, id, actorID uuid.UUID, generation int64, f Finish) (bool, error) {
	conn := r.d.Conn(ctx)
	var body any
	if f.Replayable {
		if f.Body == nil {
			body = []byte{}
		} else {
			body = f.Body
		}
	} else {
		body = nil
	}
	updates := map[string]any{
		"status":                    f.Status,
		"response_code":             f.Code,
		"replayable":                f.Replayable,
		"response_body":             body,
		"response_content_type":     ptrStringAny(f.ContentType),
		"response_location":         ptrStringAny(f.Location),
		"response_etag":             ptrStringAny(f.ETag),
		"response_cache_control":    ptrStringAny(f.CacheControl),
		"response_content_language": ptrStringAny(f.ContentLanguage),
		"resource_type":             ptrStringAny(f.ResourceType),
		"resource_id":               ptrUUIDAny(f.ResourceID),
		"locked_until":              nil,
		"completed_at":              gorm.Expr("now()"),
	}
	res := conn.Model(&model.IdempotencyKey{}).Where(
		"id = ? AND actor_id = ? AND generation = ? AND status = ?",
		id, actorID, generation, model.StatusInFlight,
	).Updates(updates)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// Purge 按 created_at 分批删除过期行。
func (r *Repo) Purge(ctx context.Context, before time.Time, limit int) (int64, error) {
	conn := r.d.Conn(ctx)
	res := conn.Exec(
		"DELETE FROM idempotency_keys WHERE id IN (SELECT id FROM idempotency_keys WHERE created_at < ? ORDER BY created_at LIMIT ?)",
		before, limit,
	)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

func ptrStringAny(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func ptrUUIDAny(p *uuid.UUID) any {
	if p == nil {
		return nil
	}
	return *p
}

package repo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrDuplicate 表示唯一约束冲突（Telegram 绑定、绑定码）。
var ErrDuplicate = errors.New("notify: 唯一约束冲突")

func mapDuplicateErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return fmt.Errorf("%w: %v", ErrDuplicate, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %v", ErrDuplicate, err)
	}
	return err
}

// Binding 读本人的 Telegram 绑定；没有 ok 为 false。
func (r *Repo) Binding(ctx context.Context, userID uuid.UUID) (model.TelegramBinding, bool, error) {
	var b model.TelegramBinding
	res := r.d.Conn(ctx).Where("user_id = ?", userID).Limit(1).Find(&b)
	if res.Error != nil {
		return model.TelegramBinding{}, false, res.Error
	}
	return b, res.RowsAffected == 1, nil
}

// BindingsForUsers 批量读一批用户的绑定（一条 SQL）。
func (r *Repo) BindingsForUsers(ctx context.Context, userIDs []uuid.UUID) ([]model.TelegramBinding, error) {
	if len(userIDs) == 0 {
		return []model.TelegramBinding{}, nil
	}
	out := []model.TelegramBinding{}
	if err := r.d.Conn(ctx).Where("user_id IN ?", userIDs).Find(&out).Error; err != nil {
		return nil, err
	}
	if out == nil {
		return []model.TelegramBinding{}, nil
	}
	return out, nil
}

// InsertBinding 写一条绑定；用户或 chat_id 已绑定返回 repo.ErrDuplicate。
func (r *Repo) InsertBinding(ctx context.Context, b *model.TelegramBinding) error {
	return mapDuplicateErr(r.d.Conn(ctx).Create(b).Error)
}

// DeleteBinding 删本人的绑定；没有返回 false。
func (r *Repo) DeleteBinding(ctx context.Context, userID uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Where("user_id = ?", userID).Delete(&model.TelegramBinding{})
	return res.RowsAffected == 1, res.Error
}

// DeleteUnusedBindCodes 删本人全部未用的绑定码。
func (r *Repo) DeleteUnusedBindCodes(ctx context.Context, userID uuid.UUID) error {
	return r.d.Conn(ctx).Exec(
		`DELETE FROM telegram_bind_codes WHERE user_id = ? AND used_at IS NULL`, userID,
	).Error
}

// InsertBindCode 写一个绑定码；哈希撞了返回 repo.ErrDuplicate。
func (r *Repo) InsertBindCode(ctx context.Context, c *model.TelegramBindCode) error {
	return mapDuplicateErr(r.d.Conn(ctx).Create(c).Error)
}

// LockBindCode 按哈希取未用、未过期（expires_at > now）的绑定码并加 FOR UPDATE；没有 ok 为 false。
func (r *Repo) LockBindCode(ctx context.Context, hash []byte, now time.Time) (model.TelegramBindCode, bool, error) {
	var c model.TelegramBindCode
	res := r.d.Conn(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("code_hash = ? AND used_at IS NULL AND expires_at > ?", hash, now).
		Limit(1).Find(&c)
	if res.Error != nil {
		return model.TelegramBindCode{}, false, res.Error
	}
	return c, res.RowsAffected == 1, nil
}

// MarkBindCodeUsed 把绑定码标为已用；已用过返回 false。
func (r *Repo) MarkBindCodeUsed(ctx context.Context, id uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE telegram_bind_codes SET used_at = now() WHERE id = ? AND used_at IS NULL`, id,
	)
	return res.RowsAffected == 1, res.Error
}

// PurgeBindCodes 删 expires_at < before 的绑定码，每次至多 batch 行。
func (r *Repo) PurgeBindCodes(ctx context.Context, before time.Time, batch int) (int, error) {
	res := r.d.Conn(ctx).Exec(
		`DELETE FROM telegram_bind_codes WHERE id IN (
		   SELECT id FROM telegram_bind_codes WHERE expires_at < ? ORDER BY expires_at LIMIT ?)`,
		before, batch)
	return int(res.RowsAffected), res.Error
}

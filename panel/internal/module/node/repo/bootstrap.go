// 一次性接入令牌（bootstrap_tokens，只存 SHA-256）。
package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"

	"github.com/aegispanel/aegis/internal/module/node/model"
)

// InsertBootstrapToken 写入一张接入令牌（调用方填好 ServerID、TokenSHA256、ExpiresAt、IssuedBy、CreatedAt）；写回 ID。
func (r *Repo) InsertBootstrapToken(ctx context.Context, t *model.BootstrapToken) error {
	return r.d.Conn(ctx).Create(t).Error
}

// BootstrapTokenByHashForUpdate 按令牌哈希取一行并加行锁（同一令牌的并发接入在这里串行）；
// 作废的令牌当作不存在；没有返回 nil, nil。
func (r *Repo) BootstrapTokenByHashForUpdate(ctx context.Context, tokenSHA256 string) (*model.BootstrapToken, error) {
	var rows []model.BootstrapToken
	err := r.d.Conn(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("token_sha256 = ? AND voided_at IS NULL", tokenSHA256).Limit(1).Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

// MarkBootstrapTokenUsed 把令牌标成已用（只从未用变成已用一次）；返回是否真的改了一行。
func (r *Repo) MarkBootstrapTokenUsed(ctx context.Context, id uuid.UUID, requestID string, at time.Time) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE bootstrap_tokens SET used_at = ?, used_request_id = ? WHERE id = ? AND used_at IS NULL AND voided_at IS NULL`,
		at, requestID, id)
	return res.RowsAffected == 1, res.Error
}

// CountPendingBootstrapTokens 数这台服务器未用、未作废、未过期的令牌。
func (r *Repo) CountPendingBootstrapTokens(ctx context.Context, serverID uuid.UUID, now time.Time) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Model(&model.BootstrapToken{}).
		Where("server_id = ? AND used_at IS NULL AND voided_at IS NULL AND expires_at > ?", serverID, now).Count(&n).Error
	return n, err
}

// VoidBootstrapTokens 作废这台服务器所有未用、未过期、未作废的令牌；返回作废了几张。
func (r *Repo) VoidBootstrapTokens(ctx context.Context, serverID uuid.UUID, at time.Time) (int64, error) {
	res := r.d.Conn(ctx).Exec(
		`UPDATE bootstrap_tokens SET voided_at = ? WHERE server_id = ? AND used_at IS NULL AND voided_at IS NULL AND expires_at > ?`,
		at, serverID, at)
	return res.RowsAffected, res.Error
}

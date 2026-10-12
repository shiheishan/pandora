package repo

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/module/identity/model"
)

// Credential 是按邮箱或 id 查到的账号与口令哈希。
type Credential struct {
	model.User
	PHC *string `gorm:"column:phc"` // 没设过口令时为 nil
}

// PasswordHash 读取用户当前口令 PHC。
func (r *Repo) PasswordHash(ctx context.Context, userID uuid.UUID) (string, bool, error) {
	var phc string
	err := r.d.Conn(ctx).Raw(
		"SELECT phc FROM user_passwords WHERE user_id = ?", userID,
	).Scan(&phc).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	if phc == "" {
		return "", false, nil
	}
	return phc, true, nil
}

// LockPasswordHash 读取并锁定用户口令行。
func (r *Repo) LockPasswordHash(ctx context.Context, userID uuid.UUID) (string, bool, error) {
	var phc string
	err := r.d.Conn(ctx).Raw(
		"SELECT phc FROM user_passwords WHERE user_id = ? FOR UPDATE", userID,
	).Scan(&phc).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	if phc == "" {
		return "", false, nil
	}
	return phc, true, nil
}

// CredentialByEmail 按邮箱查账号与口令。
func (r *Repo) CredentialByEmail(ctx context.Context, email string) (Credential, bool, error) {
	return r.credential(ctx, "u.email = ?", email)
}

// CredentialByID 按用户 id 查账号与口令。
func (r *Repo) CredentialByID(ctx context.Context, id uuid.UUID) (Credential, bool, error) {
	return r.credential(ctx, "u.id = ?", id)
}

func (r *Repo) credential(ctx context.Context, where string, arg any) (Credential, bool, error) {
	var c Credential
	sql := "SELECT u.*, p.phc FROM users u LEFT JOIN user_passwords p ON p.user_id = u.id WHERE " + where
	err := r.d.Conn(ctx).Raw(sql, arg).Scan(&c).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Credential{}, false, nil
		}
		return Credential{}, false, err
	}
	if c.ID == uuid.Nil {
		return Credential{}, false, nil
	}
	return c, true, nil
}

// UpsertPassword 写入或更新口令哈希。
func (r *Repo) UpsertPassword(ctx context.Context, userID uuid.UUID, phc string) error {
	err := r.d.Conn(ctx).Exec(
		"INSERT INTO user_passwords (user_id, phc) VALUES (?, ?) ON CONFLICT (user_id) DO UPDATE SET phc = EXCLUDED.phc, rotated_at = now(), updated_at = now()",
		userID, phc,
	).Error
	return mapErr(err)
}

// ReplacePassword 在旧哈希匹配时替换为新哈希。
func (r *Repo) ReplacePassword(ctx context.Context, userID uuid.UUID, oldPHC, newPHC string) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		"UPDATE user_passwords SET phc = ?, rotated_at = now(), updated_at = now() WHERE user_id = ? AND phc = ?",
		newPHC, userID, oldPHC,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// RehashPassword 登录时静默升级哈希参数：只换 phc 与 updated_at，不动 rotated_at（口令本身没变）；旧哈希不匹配时不改。
func (r *Repo) RehashPassword(ctx context.Context, userID uuid.UUID, oldPHC, newPHC string) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		"UPDATE user_passwords SET phc = ?, updated_at = now() WHERE user_id = ? AND phc = ?",
		newPHC, userID, oldPHC,
	)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

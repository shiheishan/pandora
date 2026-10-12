package repo

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/identity/model"
)

// InsertRegistration 插入注册会话。
func (r *Repo) InsertRegistration(ctx context.Context, s *model.RegistrationSession) error {
	return mapErr(r.d.Conn(ctx).Create(s).Error)
}

// LockRegistrationByToken 按令牌哈希锁定未过期的注册会话。
func (r *Repo) LockRegistrationByToken(ctx context.Context, tokenHash []byte) (model.RegistrationSession, bool, error) {
	var s model.RegistrationSession
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM registration_sessions WHERE token_hash = ? AND expires_at > now() FOR UPDATE",
		tokenHash,
	).Scan(&s).Error
	if err != nil {
		return model.RegistrationSession{}, false, err
	}
	if s.ID == uuid.Nil {
		return model.RegistrationSession{}, false, nil
	}
	return s, true, nil
}

// IncRegistrationAttempts 注册会话验证码尝试次数加一。
func (r *Repo) IncRegistrationAttempts(ctx context.Context, id uuid.UUID) error {
	return r.d.Conn(ctx).Exec(
		"UPDATE registration_sessions SET attempts = attempts + 1 WHERE id = ?", id,
	).Error
}

// ConsumeRegistration 将注册会话标记为已消费。
func (r *Repo) ConsumeRegistration(ctx context.Context, id uuid.UUID) error {
	return r.d.Conn(ctx).Exec(
		"UPDATE registration_sessions SET status = 'consumed', consumed_at = now() WHERE id = ? AND status = 'pending'",
		id,
	).Error
}

// InsertCode 插入验证码行。
func (r *Repo) InsertCode(ctx context.Context, c *model.VerificationCode) error {
	return mapErr(r.d.Conn(ctx).Create(c).Error)
}

// LockCodeByHash 按用途与哈希锁定一条未消费的验证码。
func (r *Repo) LockCodeByHash(ctx context.Context, purpose string, targetHash, codeHash []byte) (model.VerificationCode, bool, error) {
	var c model.VerificationCode
	err := r.d.Conn(ctx).Raw(
		`SELECT * FROM verification_codes WHERE purpose = ? AND target_hash = ? AND code_hash = ? AND consumed_at IS NULL AND expires_at > now()
		 ORDER BY created_at DESC LIMIT 1 FOR UPDATE`,
		purpose, targetHash, codeHash,
	).Scan(&c).Error
	if err != nil {
		return model.VerificationCode{}, false, err
	}
	if c.ID == uuid.Nil {
		return model.VerificationCode{}, false, nil
	}
	return c, true, nil
}

// LockLatestCode 锁定目标上最新一条未过期验证码。
func (r *Repo) LockLatestCode(ctx context.Context, purpose string, targetHash []byte) (model.VerificationCode, bool, error) {
	var c model.VerificationCode
	err := r.d.Conn(ctx).Raw(
		`SELECT * FROM verification_codes WHERE purpose = ? AND target_hash = ? AND consumed_at IS NULL AND expires_at > now()
		 ORDER BY created_at DESC LIMIT 1 FOR UPDATE`,
		purpose, targetHash,
	).Scan(&c).Error
	if err != nil {
		return model.VerificationCode{}, false, err
	}
	if c.ID == uuid.Nil {
		return model.VerificationCode{}, false, nil
	}
	return c, true, nil
}

// IncCodeAttempts 验证码尝试次数加一。
func (r *Repo) IncCodeAttempts(ctx context.Context, id uuid.UUID) error {
	return r.d.Conn(ctx).Exec(
		"UPDATE verification_codes SET attempts = attempts + 1 WHERE id = ?", id,
	).Error
}

// ConsumeCode 标记验证码已消费。
func (r *Repo) ConsumeCode(ctx context.Context, id uuid.UUID) error {
	return r.d.Conn(ctx).Exec(
		"UPDATE verification_codes SET consumed_at = now() WHERE id = ? AND consumed_at IS NULL", id,
	).Error
}

// ExpireCodes 立即过期目标上所有未消费验证码。
func (r *Repo) ExpireCodes(ctx context.Context, purpose string, targetHash []byte) error {
	return r.d.Conn(ctx).Exec(
		"UPDATE verification_codes SET expires_at = now() WHERE purpose = ? AND target_hash = ? AND consumed_at IS NULL AND expires_at > now()",
		purpose, targetHash,
	).Error
}

// PurgeRegistrations 分批删除早于 before 的注册会话。
func (r *Repo) PurgeRegistrations(ctx context.Context, before time.Time, batch int) (int, error) {
	res := r.d.Conn(ctx).Exec(
		"DELETE FROM registration_sessions WHERE id IN (SELECT id FROM registration_sessions WHERE created_at < ? ORDER BY created_at LIMIT ?)",
		before, batch,
	)
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

// PurgeCodes 分批删除早于 before 的验证码。
func (r *Repo) PurgeCodes(ctx context.Context, before time.Time, batch int) (int, error) {
	res := r.d.Conn(ctx).Exec(
		"DELETE FROM verification_codes WHERE id IN (SELECT id FROM verification_codes WHERE created_at < ? ORDER BY created_at LIMIT ?)",
		before, batch,
	)
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

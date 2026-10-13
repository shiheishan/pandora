package repo

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/module/identity/model"
)

// InsertGenerationJob 插入批量生成任务行。
func (r *Repo) InsertGenerationJob(ctx context.Context, j *model.UserGenerationJob) error {
	return mapErr(r.d.Conn(ctx).Create(j).Error)
}

// GenerationJobByID 按 id 读取任务。
func (r *Repo) GenerationJobByID(ctx context.Context, id uuid.UUID) (model.UserGenerationJob, bool, error) {
	var j model.UserGenerationJob
	err := r.d.Conn(ctx).Where("id = ?", id).Take(&j).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return model.UserGenerationJob{}, false, nil
		}
		return model.UserGenerationJob{}, false, err
	}
	return j, true, nil
}

// RecentGenerationJobs 列出最近的批量生成任务。
func (r *Repo) RecentGenerationJobs(ctx context.Context, limit int) ([]model.UserGenerationJob, error) {
	var rows []model.UserGenerationJob
	err := r.d.Conn(ctx).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.UserGenerationJob{}, nil
	}
	return rows, nil
}

// GroupExists 判断用户组是否存在。
func (r *Repo) GroupExists(ctx context.Context, id uuid.UUID) (bool, error) {
	var exists bool
	err := r.d.Conn(ctx).Raw(
		"SELECT EXISTS(SELECT 1 FROM user_groups WHERE id = ?)", id,
	).Scan(&exists).Error
	return exists, err
}

// ClaimGenerationJob 认领任务：queued 或租约已过期的 running。
func (r *Repo) ClaimGenerationJob(ctx context.Context, id uuid.UUID, lease time.Duration) (model.UserGenerationJob, bool, error) {
	var j model.UserGenerationJob
	secs := lease.Seconds()
	err := r.d.Conn(ctx).Raw(`
		UPDATE user_generation_jobs
		   SET status = 'running', attempts = attempts + 1,
		       lease_until = now() + make_interval(secs => ?),
		       started_at = coalesce(started_at, now()), updated_at = now()
		 WHERE id = ?
		   AND (status = 'queued' OR (status = 'running' AND lease_until < now()))
		 RETURNING *`, secs, id).Scan(&j).Error
	if err != nil {
		return model.UserGenerationJob{}, false, err
	}
	if j.ID == uuid.Nil {
		return model.UserGenerationJob{}, false, nil
	}
	return j, true, nil
}

// LockGenerationJob 锁定任务行（FOR UPDATE）。
func (r *Repo) LockGenerationJob(ctx context.Context, id uuid.UUID) (model.UserGenerationJob, bool, error) {
	var j model.UserGenerationJob
	err := r.d.Conn(ctx).Raw(
		"SELECT * FROM user_generation_jobs WHERE id = ? FOR UPDATE", id,
	).Scan(&j).Error
	if err != nil {
		return model.UserGenerationJob{}, false, err
	}
	if j.ID == uuid.Nil {
		return model.UserGenerationJob{}, false, nil
	}
	return j, true, nil
}

// InsertGeneratedUsers 批量插入用户；撞邮箱的不在返回 map 里。
func (r *Repo) InsertGeneratedUsers(ctx context.Context, emails []string, groupID *uuid.UUID) (map[string]uuid.UUID, error) {
	if len(emails) == 0 {
		return map[string]uuid.UUID{}, nil
	}
	var rows []struct {
		ID    uuid.UUID `gorm:"column:id"`
		Email string    `gorm:"column:email"`
	}
	var group any
	if groupID != nil {
		group = *groupID
	}
	err := r.d.Conn(ctx).Raw(`
		INSERT INTO users (email, status, user_group_id, email_verified_at)
		SELECT e, 'active', CAST(? AS uuid), now()
		  FROM unnest(CAST(? AS text[])) AS e
		ON CONFLICT (email) DO NOTHING
		RETURNING id, email::text AS email`, group, textArrayLiteral(emails)).Scan(&rows).Error
	if err != nil {
		return nil, mapErr(err)
	}
	out := make(map[string]uuid.UUID, len(rows))
	for _, row := range rows {
		out[row.Email] = row.ID
	}
	return out, nil
}

// InsertPasswords 批量写入口令哈希。
func (r *Repo) InsertPasswords(ctx context.Context, userIDs []uuid.UUID, phcs []string) error {
	if len(userIDs) == 0 {
		return nil
	}
	if len(userIDs) != len(phcs) {
		return errors.New("identity: userIDs 与 phcs 长度不一致")
	}
	return mapErr(r.d.Conn(ctx).Exec(
		`INSERT INTO user_passwords (user_id, phc)
		 SELECT * FROM unnest(CAST(? AS uuid[]), CAST(? AS text[]))`,
		uuidArray(userIDs), textArrayLiteral(phcs),
	).Error)
}

// SaveGenerationProgress 更新进度、结果密文并续租。
func (r *Repo) SaveGenerationProgress(ctx context.Context, id uuid.UUID, completed int, sealed []byte, lease time.Duration) error {
	secs := lease.Seconds()
	return r.d.Conn(ctx).Exec(`
		UPDATE user_generation_jobs
		   SET completed = ?, result_encrypted = ?, error = NULL,
		       lease_until = now() + make_interval(secs => ?), updated_at = now()
		 WHERE id = ?`, completed, sealed, secs, id).Error
}

// SucceedGenerationJob 在进度已满时标记成功并设置结果到期时间。
func (r *Repo) SucceedGenerationJob(ctx context.Context, id uuid.UUID, attempts int, ttl time.Duration) (bool, error) {
	secs := ttl.Seconds()
	res := r.d.Conn(ctx).Exec(`
		UPDATE user_generation_jobs
		   SET status = 'succeeded', finished_at = now(), lease_until = NULL,
		       result_expires_at = now() + make_interval(secs => ?), updated_at = now()
		 WHERE id = ? AND status = 'running' AND attempts = ? AND completed = total`,
		secs, id, attempts)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// FailGenerationJob 标记任务失败并保留已有结果的到期时间。
func (r *Repo) FailGenerationJob(ctx context.Context, id uuid.UUID, attempts int, reason string, ttl time.Duration) (bool, error) {
	secs := ttl.Seconds()
	res := r.d.Conn(ctx).Exec(`
		UPDATE user_generation_jobs
		   SET status = 'failed', failed = total - completed, error = ?,
		       finished_at = now(), lease_until = NULL,
		       result_expires_at = CASE WHEN result_encrypted IS NULL THEN NULL
		                                ELSE now() + make_interval(secs => ?) END,
		       updated_at = now()
		 WHERE id = ? AND status = 'running' AND attempts = ?`,
		reason, secs, id, attempts)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// RetryGenerationLater 缩短租约以便稍后重新认领。
func (r *Repo) RetryGenerationLater(ctx context.Context, id uuid.UUID, attempts int, msg string) error {
	return r.d.Conn(ctx).Exec(`
		UPDATE user_generation_jobs
		   SET error = ?, lease_until = now(), updated_at = now()
		 WHERE id = ? AND status = 'running' AND attempts = ?`,
		msg, id, attempts).Error
}

// StaleGenerationJobs 列出排队过久或租约已过期的任务。
func (r *Repo) StaleGenerationJobs(ctx context.Context, queuedBefore time.Time, limit int) ([]model.UserGenerationJob, error) {
	var rows []model.UserGenerationJob
	err := r.d.Conn(ctx).Raw(`
		SELECT * FROM user_generation_jobs
		 WHERE (status = 'queued' AND created_at < ?)
		    OR (status = 'running' AND lease_until < now())
		 ORDER BY created_at, id
		 LIMIT ?`, queuedBefore, limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []model.UserGenerationJob{}, nil
	}
	return rows, nil
}

// PurgeGenerationResults 清掉已过期的结果密文。
func (r *Repo) PurgeGenerationResults(ctx context.Context, limit int) (int64, error) {
	res := r.d.Conn(ctx).Exec(`
		UPDATE user_generation_jobs
		   SET result_encrypted = NULL, result_purged_at = now(), updated_at = now()
		 WHERE id IN (
		       SELECT id FROM user_generation_jobs
		        WHERE result_encrypted IS NOT NULL AND result_expires_at <= now()
		        ORDER BY result_expires_at
		        LIMIT ?
		        FOR UPDATE SKIP LOCKED)`, limit)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// PurgeGenerationJobs 删除已结束且结果已清空的旧任务行。
func (r *Repo) PurgeGenerationJobs(ctx context.Context, before time.Time, batch int) (int, error) {
	res := r.d.Conn(ctx).Exec(`
		DELETE FROM user_generation_jobs
		 WHERE id IN (
		       SELECT id FROM user_generation_jobs
		        WHERE finished_at < ? AND result_encrypted IS NULL
		        ORDER BY finished_at
		        LIMIT ?)`, before, batch)
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

// textArrayLiteral 把字符串切片编成 PostgreSQL text 数组字面量（元素双引号转义）；uuid 数组见 ops_users.go 的 uuidArray。
func textArrayLiteral(ss []string) string {
	if len(ss) == 0 {
		return "{}"
	}
	var b strings.Builder
	b.WriteByte('{')
	for i, s := range ss {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, r := range s {
			if r == '\\' || r == '"' {
				b.WriteByte('\\')
			}
			b.WriteRune(r)
		}
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

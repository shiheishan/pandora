package adminops

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 批量生成账号的后台任务（用户 2026-10-07 定，方案 A；迁移 00130）。
//
// 请求只登记任务（SubmitGenerateUsers），aegis-admin 的 worker（user_generation_worker.go）
// 逐个生成，一次只占 1 个 Argon2 名额，不挡登录。界面轮询进度（GetUserGenerationJob），
// 完成后下载结果（UserGenerationResult）。结果里有明文初始口令：库里只存信封密文，
// AAD 绑定任务 id（UserGenerationResultAAD），明文由 handler 解开写成 CSV；只留 24 小时，
// 到期由 worker 清掉；只有提交任务的管理员能下载，每次下载都写审计。

// userGenerationResultTTL 是结果密文的保留期：任务结束后 24 小时清掉。
const userGenerationResultTTL = 24 * time.Hour

// UserGenerationResultAAD 是结果密文的附加数据：密文搬到别的任务行上解不开。
func UserGenerationResultAAD(jobID string) []byte {
	return []byte("user_generation_job:" + jobID)
}

// UserGenerationJob 是任务的读模型（不含结果）。json tag 即后台接口的响应形状。
type UserGenerationJob struct {
	ID              string     `json:"id"`
	ActorID         string     `json:"actor_id"`
	Status          string     `json:"status"`
	Total           int        `json:"total"`
	Completed       int        `json:"completed"`
	Failed          int        `json:"failed"`
	EmailPrefix     string     `json:"email_prefix"`
	EmailDomain     string     `json:"email_domain"`
	GroupID         *string    `json:"group_id"`
	Reason          string     `json:"reason"`
	Error           *string    `json:"error"`
	ResultAvailable bool       `json:"result_available"`
	ResultExpiresAt *time.Time `json:"result_expires_at"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at"`
	FinishedAt      *time.Time `json:"finished_at"`
}

const userGenerationJobSelect = `
	SELECT id::text, actor_id::text, status, total, completed, failed,
	       email_prefix, email_domain, group_id::text, reason, error,
	       result_encrypted IS NOT NULL, result_expires_at,
	       created_at, started_at, finished_at
	  FROM user_generation_jobs`

func scanUserGenerationJob(row pgx.Row) (*UserGenerationJob, error) {
	var j UserGenerationJob
	err := row.Scan(&j.ID, &j.ActorID, &j.Status, &j.Total, &j.Completed, &j.Failed,
		&j.EmailPrefix, &j.EmailDomain, &j.GroupID, &j.Reason, &j.Error,
		&j.ResultAvailable, &j.ResultExpiresAt, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// SubmitGenerateUsers 登记一个批量生成任务，立即返回（handler 回 202）。
//
// 幂等由路由上的 Idempotency 中间件在响应后完成（与改成任务之前一样）：同一个键重放
// 回放第一次的响应，不会登记第二个任务。登记与审计在同一个事务里。
func (s *Service) SubmitGenerateUsers(ctx context.Context, tenantID string, in GenerateUsersInput) (*UserGenerationJob, error) {

	in, err := normalizeGenerateUsers(in)
	if err != nil {
		return nil, err
	}
	if _, err := uuid.Parse(in.ActorID); err != nil {
		return nil, httpx.New(httpx.CodeUnauthorized, "缺少操作人")
	}
	var job *UserGenerationJob
	actor := in.ActorID
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor}, func(tx pgx.Tx) error {
		if in.GroupID != "" {
			var exists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS(SELECT 1 FROM user_groups WHERE tenant_id = $1 AND id = $2::uuid)`,
				tenantID, in.GroupID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return httpx.Invalid(map[string]string{"group_id": "分组不存在"})
			}
		}
		var err error
		job, err = scanUserGenerationJob(tx.QueryRow(ctx, `
			WITH j AS (
				INSERT INTO user_generation_jobs
					(tenant_id, actor_id, total, email_prefix, email_domain, group_id, reason)
				VALUES ($1, $2::uuid, $3, $4, $5, NULLIF($6, '')::uuid, $7)
				RETURNING *)
			SELECT id::text, actor_id::text, status, total, completed, failed,
			       email_prefix, email_domain, group_id::text, reason, error,
			       result_encrypted IS NOT NULL, result_expires_at,
			       created_at, started_at, finished_at
			  FROM j`,
			tenantID, actor, in.Count, in.EmailPrefix, in.EmailDomain, in.GroupID, in.Reason))
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actor,
			Action: "user.bulk_generate_requested", ResourceType: "user_generation_job",
			ResourceID: &job.ID,
			AfterDigest: map[string]any{
				"count": in.Count, "prefix": in.EmailPrefix, "domain": in.EmailDomain,
				"group_id": in.GroupID, "reason": in.Reason,
			},
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		return nil, err
	}
	// 提交之后再叫醒：worker 一定看得到这个任务
	s.wakeUserGeneration()
	return job, nil
}

// GetUserGenerationJob 读一个任务的进度（不含结果）。
func (s *Service) GetUserGenerationJob(ctx context.Context, tenantID, jobID string) (*UserGenerationJob, error) {
	if _, err := uuid.Parse(jobID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	var job *UserGenerationJob
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var err error
		job, err = scanUserGenerationJob(tx.QueryRow(ctx,
			userGenerationJobSelect+` WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, jobID))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		return err
	})
	return job, err
}

// ListUserGenerationJobs 列最近的任务（最新在前，最多 20 个）。
func (s *Service) ListUserGenerationJobs(ctx context.Context, tenantID string) ([]UserGenerationJob, error) {
	out := []UserGenerationJob{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, userGenerationJobSelect+`
			 WHERE tenant_id = $1 ORDER BY created_at DESC, id DESC LIMIT 20`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			j, err := scanUserGenerationJob(rows)
			if err != nil {
				return err
			}
			out = append(out, *j)
		}
		return rows.Err()
	})
	return out, err
}

// UserGenerationResult 取一个任务的结果密文，并在同一事务里写导出审计。
//
// 只有提交任务的那位管理员能取：结果里是能直接登录的初始口令，别的管理员要另起任务。
// 别人的任务与不存在的任务回同一个 404。结果已过 24 小时被清掉、或任务还没生成出任何
// 账号时回 409。密文由 handler 按 UserGenerationResultAAD 解开。
func (s *Service) UserGenerationResult(ctx context.Context, tenantID, jobID, actorID string) ([]byte, *UserGenerationJob, error) {
	if _, err := uuid.Parse(jobID); err != nil {
		return nil, nil, httpx.NotFoundOrForbidden()
	}
	var sealed []byte
	var job *UserGenerationJob
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var err error
		job, err = scanUserGenerationJob(tx.QueryRow(ctx,
			userGenerationJobSelect+` WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, jobID))
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		if err != nil {
			return err
		}
		if job.ActorID != actorID {
			return httpx.NotFoundOrForbidden()
		}
		if err := tx.QueryRow(ctx, `
			SELECT result_encrypted FROM user_generation_jobs
			 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, jobID).Scan(&sealed); err != nil {
			return err
		}
		if len(sealed) == 0 {
			return httpx.New(httpx.CodeConflict, "结果不可下载：任务还没生成出账号，或结果已超过 24 小时被清除")
		}
		actor := actorID
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actor,
			Action: "user.bulk_generate_exported", ResourceType: "user_generation_job",
			ResourceID: &job.ID,
			AfterDigest: map[string]any{
				"completed": job.Completed, "status": job.Status, "contains_initial_passwords": true,
			},
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return sealed, job, nil
}

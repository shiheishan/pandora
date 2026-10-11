package adminops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 批量生成账号的后台 worker（方案 A：一次只占 1 个 Argon2 名额，不挡登录）。
//
// 由 aegis-admin 起一个 goroutine 调 RunOnce：登记任务时被进程内唤醒（user_generation_wake.go），
// 另有 15 秒一次的兜底轮询；每次认领一个任务做完；
// 顺带把超过 24 小时的结果密文清掉。认领带租约（user_generation_jobs.lease_until），
// 每批写完续租；进程中途退出，租约一过别的实例或重启后的自己从 completed 处接着做。
//
// 每一批：先在事务外逐个生成口令、经全局名额算 Argon2（一次一个、算完即还）；再开一个
// 短事务写这批用户与口令哈希，并把进度与结果密文（已生成的全部邮箱 + 口令）一起更新。
// 用户与结果同生共死：接着做时 completed 就是结果里已有的条数，不会重复建号。

const (
	// userGenerationBatch 是一批生成的账号数：一批的哈希约半秒，事务很短，进度每批前进一次
	userGenerationBatch = 10
	// userGenerationLease 是认领与每批续租的租约；必须远长于一批的耗时
	userGenerationLease = 2 * time.Minute
	// userGenerationMaxAttempts 是一个任务最多被认领的次数：反复中断就判失败，不无限重试
	userGenerationMaxAttempts = 5
	// userGenerationPurgeEvery 是清过期结果的间隔
	userGenerationPurgeEvery = time.Minute
)

// ResultSealer 加解密任务结果；*crypto.Envelope 满足它。
type ResultSealer interface {
	Seal(plaintext, aad []byte) ([]byte, error)
	Open(sealed, aad []byte) ([]byte, error)
}

// UserGenerationWorker 逐个生成批量账号。
type UserGenerationWorker struct {
	s      *Service
	sealer ResultSealer
	log    *slog.Logger
	// newCredential 生成一份口令与哈希（测试可替换以免真跑 Argon2）
	newCredential func(ctx context.Context) (generatedCredential, error)
	// newSuffix 生成邮箱后缀（测试可注入）
	newSuffix func() (string, error)
	lastPurge time.Time
}

// NewUserGenerationWorker 用信封加密器与日志装配 worker。
func (s *Service) NewUserGenerationWorker(sealer ResultSealer, log *slog.Logger) *UserGenerationWorker {
	if log == nil {
		log = slog.Default()
	}
	return &UserGenerationWorker{
		s: s, sealer: sealer, log: log,
		newCredential: generateCredentialWithSlot,
		newSuffix:     func() (string, error) { return randomSlug(8) },
	}
}

// RunOnce 清一次过期结果（按间隔），再认领并做完至多一个任务。返回是否认领到了任务。
func (w *UserGenerationWorker) RunOnce(ctx context.Context, tenantID string) (bool, error) {
	if time.Since(w.lastPurge) >= userGenerationPurgeEvery {
		n, err := w.s.PurgeExpiredUserGenerationResults(ctx, tenantID)
		if err != nil {
			return false, fmt.Errorf("清过期结果: %w", err)
		}
		w.lastPurge = time.Now()
		if n > 0 {
			w.log.Info("批量生成账号的过期结果已清除", "jobs", n)
		}
	}
	job, err := w.claim(ctx, tenantID)
	if err != nil || job == nil {
		return false, err
	}
	return true, w.process(ctx, tenantID, job)
}

// claimedJob 是认领到的任务；attempts 是这次认领的代数，写进度时用它确认租约还是自己的。
type claimedJob struct {
	id, actorID, prefix, domain, reason string
	groupID                             *string
	total, completed, attempts          int
	sealed                              []byte
}

func (w *UserGenerationWorker) claim(ctx context.Context, tenantID string) (*claimedJob, error) {
	var job *claimedJob
	err := w.s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var j claimedJob
		err := tx.QueryRow(ctx, `
			UPDATE user_generation_jobs j
			   SET status = 'running', attempts = j.attempts + 1,
			       lease_until = now() + make_interval(secs => $2),
			       started_at = coalesce(j.started_at, now()), updated_at = now()
			 WHERE j.id = (
			       SELECT id FROM user_generation_jobs
			        WHERE tenant_id = $1 AND status IN ('queued', 'running')
			          AND (status = 'queued' OR lease_until IS NULL OR lease_until < now())
			        ORDER BY created_at, id
			        LIMIT 1
			        FOR UPDATE SKIP LOCKED)
			RETURNING j.id::text, j.actor_id::text, j.email_prefix, j.email_domain, j.reason,
			          j.group_id::text, j.total, j.completed, j.attempts, j.result_encrypted`,
			tenantID, userGenerationLease.Seconds()).Scan(
			&j.id, &j.actorID, &j.prefix, &j.domain, &j.reason,
			&j.groupID, &j.total, &j.completed, &j.attempts, &j.sealed)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		job = &j
		return nil
	})
	return job, err
}

func (w *UserGenerationWorker) process(ctx context.Context, tenantID string, job *claimedJob) error {
	if job.attempts > userGenerationMaxAttempts {
		return w.fail(ctx, tenantID, job, "任务多次中断，已停止；已生成的账号见结果")
	}
	results, err := w.openResults(job)
	if err != nil {
		return w.fail(ctx, tenantID, job, "已生成部分的结果无法解开，已停止")
	}
	for job.completed < job.total {
		if err := ctx.Err(); err != nil {
			return nil // 停机：租约一过接着做
		}
		n := min(userGenerationBatch, job.total-job.completed)
		creds := make([]generatedCredential, 0, n)
		for len(creds) < n {
			c, err := w.newCredential(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return w.retryLater(ctx, tenantID, job, err)
			}
			creds = append(creds, c)
		}
		next, err := w.writeBatch(ctx, tenantID, job, results, creds)
		if errors.Is(err, errUserGenerationGroupGone) {
			return w.fail(ctx, tenantID, job, "所选分组已被删除，任务停止；已生成的账号见结果")
		}
		if errors.Is(err, errUserGenerationLeaseLost) {
			return nil // 别的实例接手了
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return w.retryLater(ctx, tenantID, job, err)
		}
		results = next
		job.completed = len(results)
	}
	return w.succeed(ctx, tenantID, job)
}

var (
	errUserGenerationGroupGone = errors.New("user generation group deleted")
	errUserGenerationLeaseLost = errors.New("user generation lease lost")
)

// writeBatch 在一个短事务里写这批用户与口令哈希，并把进度与全部结果的密文一起更新。
func (w *UserGenerationWorker) writeBatch(ctx context.Context, tenantID string, job *claimedJob,
	results []GeneratedUser, creds []generatedCredential) ([]GeneratedUser, error) {

	var next []GeneratedUser
	err := w.s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: job.actorID}, func(tx pgx.Tx) error {
		// 先锁任务行并确认租约还是这一代的：不是就说明别的实例已经接手，这批不写
		var mine bool
		if err := tx.QueryRow(ctx, `
			SELECT status = 'running' AND attempts = $3 AND completed = $4
			  FROM user_generation_jobs
			 WHERE tenant_id = $1 AND id = $2::uuid
			 FOR UPDATE`, tenantID, job.id, job.attempts, job.completed).Scan(&mine); err != nil {
			return err
		}
		if !mine {
			return errUserGenerationLeaseLost
		}
		var group any
		if job.groupID != nil {
			var exists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS(SELECT 1 FROM user_groups WHERE tenant_id = $1 AND id = $2::uuid)`,
				tenantID, *job.groupID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return errUserGenerationGroupGone
			}
			group = *job.groupID
		}
		users, err := insertGeneratedUsers(ctx, tx, tenantID, group, job.prefix, job.domain, creds, w.newSuffix)
		if err != nil {
			return err
		}
		next = append(append(make([]GeneratedUser, 0, len(results)+len(users)), results...), users...)
		sealed, err := w.sealResults(job.id, next)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE user_generation_jobs
			   SET completed = $3, result_encrypted = $4, error = NULL,
			       lease_until = now() + make_interval(secs => $5), updated_at = now()
			 WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, job.id, len(next), sealed, userGenerationLease.Seconds())
		return err
	})
	if err != nil {
		return nil, err
	}
	job.sealed = nil
	return next, nil
}

// succeed 收尾：状态、结果到期时间与生成审计（主体是提交任务的管理员）一起写。
func (w *UserGenerationWorker) succeed(ctx context.Context, tenantID string, job *claimedJob) error {
	return w.s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: job.actorID}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE user_generation_jobs
			   SET status = 'succeeded', finished_at = now(), lease_until = NULL,
			       result_expires_at = now() + make_interval(secs => $4), updated_at = now()
			 WHERE tenant_id = $1 AND id = $2::uuid AND status = 'running' AND attempts = $3
			   AND completed = total`,
			tenantID, job.id, job.attempts, userGenerationResultTTL.Seconds())
		if err != nil || tag.RowsAffected() != 1 {
			return err
		}
		return w.finishAudit(ctx, tx, tenantID, job, "success", "")
	})
}

// fail 把任务判为失败：没生成的记进 failed，已生成部分的结果照样保留 24 小时可下载。
func (w *UserGenerationWorker) fail(ctx context.Context, tenantID string, job *claimedJob, reason string) error {
	return w.s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: job.actorID}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE user_generation_jobs
			   SET status = 'failed', failed = total - completed, error = $4,
			       finished_at = now(), lease_until = NULL,
			       result_expires_at = CASE WHEN result_encrypted IS NULL THEN NULL
			                                ELSE now() + make_interval(secs => $5) END,
			       updated_at = now()
			 WHERE tenant_id = $1 AND id = $2::uuid AND status = 'running' AND attempts = $3`,
			tenantID, job.id, job.attempts, reason, userGenerationResultTTL.Seconds())
		if err != nil || tag.RowsAffected() != 1 {
			return err
		}
		return w.finishAudit(ctx, tx, tenantID, job, "partial", reason)
	})
}

// retryLater 记下错误并把租约缩短到马上过期，下一轮重新认领（attempts 封顶）。
func (w *UserGenerationWorker) retryLater(ctx context.Context, tenantID string, job *claimedJob, cause error) error {
	err := w.s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE user_generation_jobs
			   SET error = $4, lease_until = now(), updated_at = now()
			 WHERE tenant_id = $1 AND id = $2::uuid AND status = 'running' AND attempts = $3`,
			tenantID, job.id, job.attempts, "生成中断，稍后自动重试")
		return err
	})
	return errors.Join(cause, err)
}

func (w *UserGenerationWorker) finishAudit(ctx context.Context, tx pgx.Tx, tenantID string,
	job *claimedJob, outcome, reason string) error {
	actor := job.actorID
	digest := map[string]any{
		"job_id": job.id, "count": job.total, "completed": job.completed,
		"prefix": job.prefix, "domain": job.domain, "reason": job.reason,
	}
	if job.groupID != nil {
		digest["group_id"] = *job.groupID
	}
	if reason != "" {
		digest["error"] = reason
	}
	return audit.Write(ctx, tx, tenantID, audit.Entry{
		ActorKind: "admin", ActorID: &actor,
		Action: "user.bulk_generated", ResourceType: "user_generation_job", ResourceID: &job.id,
		AfterDigest: digest, APIDomain: "admin", Outcome: outcome,
	})
}

func (w *UserGenerationWorker) openResults(job *claimedJob) ([]GeneratedUser, error) {
	if len(job.sealed) == 0 {
		if job.completed != 0 {
			return nil, errors.New("progress without a result")
		}
		return nil, nil
	}
	plain, err := w.sealer.Open(job.sealed, UserGenerationResultAAD(job.id))
	if err != nil {
		return nil, err
	}
	var out []GeneratedUser
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, err
	}
	if len(out) != job.completed {
		return nil, fmt.Errorf("result holds %d accounts, progress says %d", len(out), job.completed)
	}
	return out, nil
}

func (w *UserGenerationWorker) sealResults(jobID string, results []GeneratedUser) ([]byte, error) {
	plain, err := json.Marshal(results)
	if err != nil {
		return nil, err
	}
	return w.sealer.Seal(plain, UserGenerationResultAAD(jobID))
}

// PurgeExpiredUserGenerationResults 清掉超过保留期的结果密文（含明文初始口令），任务行留着。
// 每次最多清 100 个任务。
func (s *Service) PurgeExpiredUserGenerationResults(ctx context.Context, tenantID string) (int64, error) {
	var n int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE user_generation_jobs
			   SET result_encrypted = NULL, result_purged_at = now(), updated_at = now()
			 WHERE id IN (
			       SELECT id FROM user_generation_jobs
			        WHERE tenant_id = $1 AND result_encrypted IS NOT NULL
			          AND result_expires_at <= now()
			        ORDER BY result_expires_at
			        LIMIT 100
			        FOR UPDATE SKIP LOCKED)`, tenantID)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

// generateCredentialWithSlot 生成一份随机口令并经全局名额算 Argon2：一次只占 1 个名额、
// 算完即还，不把登录挤出去。名额排不上（登录正忙）就稍等再排，不让任务失败。
func generateCredentialWithSlot(ctx context.Context) (generatedCredential, error) {
	password, err := randomPassword()
	if err != nil {
		return generatedCredential{}, err
	}
	for {
		slot, err := crypto.AcquirePasswordSlot(ctx)
		if errors.Is(err, crypto.ErrPasswordHashBusy) && ctx.Err() == nil {
			select {
			case <-ctx.Done():
				return generatedCredential{}, ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			continue
		}
		if err != nil {
			return generatedCredential{}, httpx.New(httpx.CodeUnavailable, "当前请求较多，请稍后重试").WithInternal(err)
		}
		phc, err := slot.Hash(password, crypto.DefaultArgon2Params())
		slot.Release()
		if err != nil {
			return generatedCredential{}, err
		}
		return generatedCredential{password: password, phc: phc}, nil
	}
}

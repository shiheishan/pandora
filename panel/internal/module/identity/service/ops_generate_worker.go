package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/identity/model"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	generationBatch       = 10
	generationLease       = 2 * time.Minute
	generationMaxAttempts = 5
	reconcileQueuedAfter  = time.Minute
	reconcileBatch        = 20
	purgeResultsBatch     = 100
)

var (
	errGenerationLeaseLost     = errors.New("identity: generation lease lost")
	errGenerationGroupGone     = errors.New("identity: generation group gone")
	errGenerationGroupDisabled = errors.New("identity: generation group disabled")
)

// RunGenerationJob 执行一次批量生成任务（asynq 处理函数调用）。
func (s *Service) RunGenerationJob(ctx context.Context, id uuid.UUID) error {
	job, ok, err := s.repo.ClaimGenerationJob(ctx, id, generationLease)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	if job.Attempts > generationMaxAttempts {
		return s.failGeneration(ctx, job, "任务多次中断，已停止；已生成的账号见结果", job.Completed)
	}
	results, err := s.openGenerationResults(job)
	if err != nil {
		return s.failGeneration(ctx, job, "已生成部分的结果无法解开，已停止", job.Completed)
	}
	completed := job.Completed
	for completed < job.Total {
		if err := ctx.Err(); err != nil {
			return nil
		}
		n := min(generationBatch, job.Total-completed)
		creds := make([]generationCredential, 0, n)
		for len(creds) < n {
			c, err := s.generateCredential(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return s.retryGenerationLater(ctx, job, err)
			}
			creds = append(creds, c)
		}
		next, err := s.writeGenerationBatch(ctx, job, completed, results, creds)
		if errors.Is(err, errGenerationGroupGone) {
			return s.failGeneration(ctx, job, "所选分组已被删除，任务停止；已生成的账号见结果", completed)
		}
		if errors.Is(err, errGenerationGroupDisabled) {
			return s.failGeneration(ctx, job, "所选分组已停用，任务停止；已生成的账号见结果", completed)
		}
		if errors.Is(err, errGenerationLeaseLost) {
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return s.retryGenerationLater(ctx, job, err)
		}
		results = next
		completed = len(results)
	}
	job.Completed = completed
	return s.succeedGeneration(ctx, job)
}

// ReconcileGeneration 清过期结果并补排卡住的任务。
func (s *Service) ReconcileGeneration(ctx context.Context) (requeued int, purged int64, err error) {
	purged, err = s.repo.PurgeGenerationResults(ctx, purgeResultsBatch)
	if err != nil {
		return 0, 0, err
	}
	stale, err := s.repo.StaleGenerationJobs(ctx, time.Now().Add(-reconcileQueuedAfter), reconcileBatch)
	if err != nil {
		return 0, purged, err
	}
	for _, j := range stale {
		if err := s.enqueueGeneration(ctx, j.ID, j.Attempts); err != nil {
			plog.From(ctx).Warn("批量生成补排入队失败",
				zap.String("job", j.ID.String()), zap.Error(err))
			continue
		}
		requeued++
	}
	return requeued, purged, nil
}

// PurgeGenerationJobs 删除已结束且结果已清空的旧任务行。
func (s *Service) PurgeGenerationJobs(ctx context.Context, before time.Time, batch int) (int, error) {
	if batch < 1 || batch > 10000 {
		return 0, fmt.Errorf("identity: purge batch 须在 1 到 10000 之间")
	}
	return s.repo.PurgeGenerationJobs(ctx, before, batch)
}

func (s *Service) generateCredential(ctx context.Context) (generationCredential, error) {
	password, err := randomPassword()
	if err != nil {
		return generationCredential{}, err
	}
	for {
		release, err := s.hasher.acquire(ctx, authn.AudienceAdmin)
		if errors.Is(err, ErrPasswordHashBusy) && ctx.Err() == nil {
			select {
			case <-ctx.Done():
				return generationCredential{}, ctx.Err()
			case <-time.After(200 * time.Millisecond):
				continue
			}
		}
		if err != nil {
			return generationCredential{}, err
		}
		phc, err := hashPHC(password)
		release()
		if err != nil {
			return generationCredential{}, err
		}
		return generationCredential{password: password, phc: phc}, nil
	}
}

func (s *Service) writeGenerationBatch(ctx context.Context, job model.UserGenerationJob, completed int,
	results []GeneratedUser, creds []generationCredential) ([]GeneratedUser, error) {

	var next []GeneratedUser
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		locked, ok, err := s.repo.LockGenerationJob(ctx, job.ID)
		if err != nil {
			return err
		}
		if !ok || locked.Status != JobRunning || locked.Attempts != job.Attempts || locked.Completed != completed {
			return errGenerationLeaseLost
		}
		if job.GroupID != nil {
			// 执行时复查：提交后组被删或被停用都停止任务
			st, ok, err := s.repo.GroupStatus(ctx, *job.GroupID)
			if err != nil {
				return err
			}
			if !ok {
				return errGenerationGroupGone
			}
			if st == GroupDisabled {
				return errGenerationGroupDisabled
			}
		}
		tried := map[string]bool{}
		for _, u := range results {
			tried[u.Email] = true
		}
		users, err := s.insertGenerationUsers(ctx, job.EmailPrefix, job.EmailDomain, job.GroupID, creds, tried)
		if err != nil {
			return err
		}
		next = append(append(make([]GeneratedUser, 0, len(results)+len(users)), results...), users...)
		sealed, err := s.sealGenerationResults(job.ID, next)
		if err != nil {
			return err
		}
		return s.repo.SaveGenerationProgress(ctx, job.ID, len(next), sealed, generationLease)
	})
	if err != nil {
		return nil, err
	}
	return next, nil
}

func (s *Service) succeedGeneration(ctx context.Context, job model.UserGenerationJob) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		ok, err := s.repo.SucceedGenerationJob(ctx, job.ID, job.Attempts, generationResultTTL)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		return s.writeGenerationFinishAudit(ctx, job, auditsvc.OutcomeSuccess, "")
	})
}

func (s *Service) failGeneration(ctx context.Context, job model.UserGenerationJob, reason string, completed int) error {
	job.Completed = completed
	return s.tx.Do(ctx, func(ctx context.Context) error {
		ok, err := s.repo.FailGenerationJob(ctx, job.ID, job.Attempts, reason, generationResultTTL)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		outcome := auditsvc.OutcomePartial
		if completed == 0 {
			outcome = auditsvc.OutcomeFailure
		}
		return s.writeGenerationFinishAudit(ctx, job, outcome, reason)
	})
}

func (s *Service) retryGenerationLater(ctx context.Context, job model.UserGenerationJob, cause error) error {
	msg := "生成中断，稍后自动重试"
	if err := s.repo.RetryGenerationLater(ctx, job.ID, job.Attempts, msg); err != nil {
		return errors.Join(cause, err)
	}
	return errors.Join(cause, fmt.Errorf("%s", msg))
}

func (s *Service) writeGenerationFinishAudit(ctx context.Context, job model.UserGenerationJob, outcome, reason string) error {
	data := map[string]any{
		"job_id": job.ID.String(), "count": job.Total, "completed": job.Completed,
		"prefix": job.EmailPrefix, "domain": job.EmailDomain, "reason": job.Reason,
	}
	if job.GroupID != nil {
		data["group_id"] = job.GroupID.String()
	}
	if reason != "" {
		data["error"] = reason
	}
	actor := job.ActorID
	return s.writeAudit(ctx, auditsvc.Event{
		Action:       "user.bulk_generated",
		ResourceType: "user_generation_job",
		ResourceID:   &job.ID,
		ActorID:      &actor,
		Audience:     authn.AudienceAdmin,
		Outcome:      outcome,
		Data:         data,
	})
}

func (s *Service) openGenerationResults(job model.UserGenerationJob) ([]GeneratedUser, error) {
	if len(job.ResultEncrypted) == 0 {
		if job.Completed != 0 {
			return nil, errors.New("progress without a result")
		}
		return nil, nil
	}
	sealer, err := s.keys.Sealer()
	if err != nil {
		return nil, err
	}
	plain, err := sealer.Open(job.ResultEncrypted, generationResultAAD(job.ID))
	if err != nil {
		return nil, err
	}
	var out []GeneratedUser
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, err
	}
	if len(out) != job.Completed {
		return nil, fmt.Errorf("result holds %d accounts, progress says %d", len(out), job.Completed)
	}
	return out, nil
}

func (s *Service) sealGenerationResults(id uuid.UUID, results []GeneratedUser) ([]byte, error) {
	plain, err := json.Marshal(results)
	if err != nil {
		return nil, err
	}
	sealer, err := s.keys.Sealer()
	if err != nil {
		return nil, err
	}
	return sealer.Seal(plain, generationResultAAD(id))
}

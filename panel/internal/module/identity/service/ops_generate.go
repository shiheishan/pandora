package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/identity/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// TaskGenerateUsers 是批量生成账号的一次性 asynq 任务类型。
const TaskGenerateUsers = "identity:generate_users"

// TaskReconcileGeneration 是每分钟补排与清过期结果的定时任务类型。
const TaskReconcileGeneration = "reconcile:user_generation"

// generationResultTTL 是任务结果密文的保留期。
const generationResultTTL = 24 * time.Hour

// GenerationRetentionDays 是已结束且结果已清空任务行的保留天数。
const GenerationRetentionDays = 30

// generationTaskTimeout 是单次生成任务的 asynq 超时。
const generationTaskTimeout = 10 * time.Minute

// generationResultAAD 是结果密文的附加数据。
func generationResultAAD(id uuid.UUID) []byte {
	return []byte("user_generation_job:" + id.String())
}

// newGenerationSuffix 生成邮箱随机后缀，测试可替换。
var newGenerationSuffix = func() (string, error) { return randomSlug(8) }

// SubmitGeneration 登记批量生成任务并入队。
func (s *Service) SubmitGeneration(ctx context.Context, in GenerateInput) (GenerationJob, error) {
	p, err := principal(ctx)
	if err != nil {
		return GenerationJob{}, err
	}
	in, err = normalizeGenerateInput(in)
	if err != nil {
		return GenerationJob{}, err
	}
	var job model.UserGenerationJob
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if in.GroupID != nil {
			ok, err := s.repo.GroupExists(ctx, *in.GroupID)
			if err != nil {
				return err
			}
			if !ok {
				return errs.Invalid(map[string]string{"group_id": "分组不存在"})
			}
		}
		job = model.UserGenerationJob{
			ActorID:     p.UserID,
			Status:      JobQueued,
			Total:       in.Count,
			EmailPrefix: in.EmailPrefix,
			EmailDomain: in.EmailDomain,
			GroupID:     in.GroupID,
			Reason:      in.Reason,
		}
		if err := s.repo.InsertGenerationJob(ctx, &job); err != nil {
			return err
		}
		groupID := ""
		if in.GroupID != nil {
			groupID = in.GroupID.String()
		}
		if err := s.writeAudit(ctx, auditsvc.Event{
			Action:       "user.bulk_generate_requested",
			ResourceType: "user_generation_job",
			ResourceID:   &job.ID,
			Data: map[string]any{
				"count": in.Count, "prefix": in.EmailPrefix, "domain": in.EmailDomain,
				"group_id": groupID, "reason": in.Reason,
			},
		}); err != nil {
			return err
		}
		s.kickGeneration(ctx, job.ID, 0)
		return nil
	})
	if err != nil {
		return GenerationJob{}, err
	}
	return jobFromModel(job), nil
}

// ListGenerationJobs 返回最近 20 个批量生成任务。
func (s *Service) ListGenerationJobs(ctx context.Context) ([]GenerationJob, error) {
	rows, err := s.repo.RecentGenerationJobs(ctx, 20)
	if err != nil {
		return nil, err
	}
	out := make([]GenerationJob, len(rows))
	for i, m := range rows {
		out[i] = jobFromModel(m)
	}
	return out, nil
}

// GetGenerationJob 读取单个任务进度。
func (s *Service) GetGenerationJob(ctx context.Context, id uuid.UUID) (GenerationJob, error) {
	m, ok, err := s.repo.GenerationJobByID(ctx, id)
	if err != nil {
		return GenerationJob{}, err
	}
	if !ok {
		return GenerationJob{}, errs.New(errs.NotFound)
	}
	return jobFromModel(m), nil
}

// GenerationResult 解开任务结果并写导出审计。
func (s *Service) GenerationResult(ctx context.Context, id uuid.UUID) ([]GeneratedUser, error) {
	p, err := principal(ctx)
	if err != nil {
		return nil, err
	}
	var users []GeneratedUser
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		m, ok, err := s.repo.GenerationJobByID(ctx, id)
		if err != nil {
			return err
		}
		if !ok || m.ActorID != p.UserID {
			return errs.New(errs.NotFound)
		}
		if len(m.ResultEncrypted) == 0 || (m.ResultExpiresAt != nil && !s.now().Before(*m.ResultExpiresAt)) {
			return errs.Newf(errs.Conflict, "结果不可下载：任务还没生成出账号，或结果已超过 24 小时被清除")
		}
		sealer, err := s.keys.Sealer()
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		plain, err := sealer.Open(m.ResultEncrypted, generationResultAAD(id))
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		if err := json.Unmarshal(plain, &users); err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		return s.writeAudit(ctx, auditsvc.Event{
			Action:       "user.bulk_generate_exported",
			ResourceType: "user_generation_job",
			ResourceID:   &id,
			Data:         map[string]any{"rows": len(users)},
		})
	})
	if err != nil {
		return nil, err
	}
	return users, nil
}

// kickGeneration 在事务提交后入队生成任务；attempts 写入 TaskID 以免 asynq 归档占号。
func (s *Service) kickGeneration(ctx context.Context, id uuid.UUID, attempts int) {
	tx.AfterCommit(ctx, TaskGenerateUsers+":"+id.String(), func(ctx context.Context) {
		if err := s.enqueueGeneration(ctx, id, attempts); err != nil {
			plog.From(ctx).Warn("批量生成任务入队失败，等 reconcile 补上",
				zap.String("job", id.String()), zap.Error(err))
		}
	})
}

// enqueueGeneration 入队 identity:generate_users；TaskID 带 attempts 代数。
func (s *Service) enqueueGeneration(ctx context.Context, id uuid.UUID, attempts int) error {
	if s.queue == nil {
		return nil
	}
	task, err := jobs.NewTask(ctx, TaskGenerateUsers, GenerationPayload{JobID: id})
	if err != nil {
		return err
	}
	_, err = s.queue.EnqueueContext(ctx, task,
		asynq.TaskID(fmt.Sprintf("gen_users:%s:%d", id, attempts)),
		asynq.Queue("default"),
		asynq.MaxRetry(0),
		asynq.Timeout(generationTaskTimeout),
	)
	if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
		return nil
	}
	return err
}

func normalizeGenerateInput(in GenerateInput) (GenerateInput, error) {
	in.EmailPrefix = strings.ToLower(strings.TrimSpace(in.EmailPrefix))
	in.EmailDomain = strings.ToLower(strings.TrimSpace(in.EmailDomain))
	reason, ok := cleanText(in.Reason)
	if !ok {
		return in, errs.Invalid(map[string]string{"reason": "请写清生成原因，5 到 500 个字"})
	}
	in.Reason = reason

	if in.Count < 1 || in.Count > 500 {
		return in, errs.Invalid(map[string]string{
			"count": "一次生成 1 到 500 个。更多请分批提交"})
	}
	if !isSafeSlug(in.EmailPrefix) {
		return in, errs.Invalid(map[string]string{
			"email_prefix": "前缀只能用小写字母、数字和短横线，1 到 20 位"})
	}
	if !isSafeDomain(in.EmailDomain) {
		return in, errs.Invalid(map[string]string{
			"email_domain": "域名格式不正确"})
	}
	if n := utf8.RuneCountInString(in.Reason); n < 5 || n > 500 {
		return in, errs.Invalid(map[string]string{
			"reason": "请写清生成原因，5 到 500 个字"})
	}
	return in, nil
}

func jobFromModel(m model.UserGenerationJob) GenerationJob {
	return GenerationJob{
		ID:              m.ID,
		ActorID:         m.ActorID,
		Status:          m.Status,
		Total:           m.Total,
		Completed:       m.Completed,
		Failed:          m.Failed,
		EmailPrefix:     m.EmailPrefix,
		EmailDomain:     m.EmailDomain,
		GroupID:         m.GroupID,
		Reason:          m.Reason,
		Error:           m.Error,
		ResultAvailable: len(m.ResultEncrypted) > 0,
		ResultExpiresAt: m.ResultExpiresAt,
		CreatedAt:       m.CreatedAt,
		StartedAt:       m.StartedAt,
		FinishedAt:      m.FinishedAt,
	}
}

func isSafeSlug(s string) bool {
	if len(s) < 1 || len(s) > 20 || s[0] == '-' {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

func isSafeDomain(s string) bool {
	if len(s) < 4 || len(s) > 63 || !strings.Contains(s, ".") {
		return false
	}
	if strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") || strings.Contains(s, "..") {
		return false
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

func randomSlug(n int) (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out), nil
}

func randomPassword() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789@#%+="
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 16)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	out[0] = 'A' + byte(int(b[0])%26)
	out[1] = 'a' + byte(int(b[1])%26)
	out[2] = '2' + byte(int(b[2])%8)
	return string(out), nil
}

// generationCredential 是事务外生成的一份口令与 PHC。
type generationCredential struct {
	password string
	phc      string
}

const generationEmailRounds = 20

// insertGenerationUsers 在事务内按轮插入用户并写口令，返回本批 GeneratedUser。
func (s *Service) insertGenerationUsers(ctx context.Context, prefix, domain string, groupID *uuid.UUID,
	creds []generationCredential, tried map[string]bool) ([]GeneratedUser, error) {

	n := len(creds)
	userIDs := make([]uuid.UUID, n)
	emails := make([]string, n)
	pending := make([]int, n)
	for i := range creds {
		pending[i] = i
	}
	if tried == nil {
		tried = map[string]bool{}
	}
	for round := 0; len(pending) > 0; round++ {
		if round >= generationEmailRounds {
			return nil, fmt.Errorf("批量生成账号：邮箱后缀反复冲突")
		}
		batchEmails := make([]string, 0, len(pending))
		byEmail := make(map[string]int, len(pending))
		for _, i := range pending {
			var email string
			for {
				sfx, err := newGenerationSuffix()
				if err != nil {
					return nil, err
				}
				email = prefix + "-" + sfx + "@" + domain
				if !tried[email] {
					break
				}
			}
			tried[email] = true
			emails[i] = email
			byEmail[email] = i
			batchEmails = append(batchEmails, email)
		}
		inserted, err := s.repo.InsertGeneratedUsers(ctx, batchEmails, groupID)
		if err != nil {
			return nil, err
		}
		for email, id := range inserted {
			if i, ok := byEmail[email]; ok {
				userIDs[i] = id
			}
		}
		next := pending[:0]
		for _, i := range pending {
			if userIDs[i] == uuid.Nil {
				next = append(next, i)
			}
		}
		pending = next
	}
	phcs := make([]string, n)
	out := make([]GeneratedUser, n)
	for i, c := range creds {
		phcs[i] = c.phc
		out[i] = GeneratedUser{Email: emails[i], Password: c.password}
	}
	if err := s.repo.InsertPasswords(ctx, userIDs, phcs); err != nil {
		return nil, err
	}
	return out, nil
}

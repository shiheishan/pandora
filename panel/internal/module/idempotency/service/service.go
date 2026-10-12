// Package service 是幂等键的认领、完成、释放与清理。
//
// 分工：
//   - 中间件（handler.Middleware）读 Idempotency-Key 头，调 Claim 认领；认领成功后把 *Claim 放进请求 ctx，
//     业务处理完再按响应调 Complete / CompleteUnreplayable / Release。
//   - 需要和业务写入同事务完成的接口（下单、续费、换套餐、充值等），业务 service 用 ClaimFrom(ctx) 取认领，
//     在自己的事务里调 Complete 或 CompleteBound（ctx 带事务即加入）。事务提交后 Claim.Completed() 为真，
//     中间件看到就不再完成；事务回滚则认领仍是 in_flight，由中间件按最终响应释放或完成。
//   - 过期的 in_flight 不会被接管：租约到期不能证明上一次执行已经停下，只作对账信号。
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/aegispanel/aegis/internal/module/idempotency/model"
	"github.com/aegispanel/aegis/internal/module/idempotency/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// ErrClaimLost 表示认领已不归本次执行所有（已完成、已被接管或已清理），完成写入未生效。
var ErrClaimLost = errors.New("idempotency: claim lost")

// Resource 是业务事务内完成时绑定的业务资源。
type Resource struct {
	Type string
	ID   uuid.UUID
}

// Claim 是一次已认领的执行权；只能用指针传递。
type Claim struct {
	ID          uuid.UUID
	Scope       string
	ActorID     uuid.UUID
	Key         string
	Fingerprint Fingerprint
	Generation  int64
	LockedUntil time.Time
	completed   atomic.Bool
}

// Completed 报告本次认领是否已在库里完成（所在事务已提交）。
func (c *Claim) Completed() bool { return c.completed.Load() }

type claimKey struct{}

// WithClaim 把认领放进 ctx。
func WithClaim(ctx context.Context, c *Claim) context.Context {
	return context.WithValue(ctx, claimKey{}, c)
}

// ClaimFrom 取 ctx 里的认领。
func ClaimFrom(ctx context.Context) (*Claim, bool) {
	c, ok := ctx.Value(claimKey{}).(*Claim)
	return c, ok && c != nil
}

// Service 是 idempotency 模块的业务入口。
type Service struct {
	repo *repo.Repo
	tx   *tx.Manager
}

// New 构造 Service。
func New(r *repo.Repo, m *tx.Manager) *Service { return &Service{repo: r, tx: m} }

// Claim 认领 (scope, 当前操作者, key)。三选一返回：新认领、可重放的响应、错误
// （400 键非法、401 未登录、409 idempotency_key_reuse 指纹不同、409 conflict 处理中或需人工核对）。
// ctx 里已有事务时加入该事务，否则在短事务里完成。
func (s *Service) Claim(ctx context.Context, scope, key string, fp Fingerprint) (*Claim, *Response, error) {
	if !ValidScope(scope) {
		return nil, nil, fmt.Errorf("idempotency: invalid scope %q", scope)
	}
	if !ValidKey(key) {
		return nil, nil, errs.Newf(errs.BadRequest, "此接口要求有效的 Idempotency-Key 请求头")
	}
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return nil, nil, errs.Newf(errs.Unauthorized, "该接口要求有效的已认证账号")
	}

	var (
		claim  *Claim
		replay *Response
	)
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		row, inserted, err := s.repo.Insert(ctx, scope, p.UserID, key, fp[:])
		if err != nil {
			return err
		}
		if inserted {
			claim = newClaim(row, fp)
			return nil
		}
		row, err = s.repo.LockForUpdate(ctx, scope, p.UserID, key)
		if err != nil {
			return err
		}
		if !bytes.Equal(row.RequestHash, fp[:]) {
			plog.From(ctx).Warn("idempotency key reused for a different request", zap.String("scope", scope))
			return errs.Newf(errs.IdempotencyReuse, "该 Idempotency-Key 已用于内容不同的请求")
		}
		switch row.Status {
		case model.StatusInFlight:
			return busy(ctx, scope)
		case model.StatusSucceeded:
			if !row.Replayable {
				return busy(ctx, scope)
			}
			r, err := replayOf(row)
			if err != nil {
				return err
			}
			replay = r
			return nil
		case model.StatusFailed:
			taken, ok, err := s.repo.Takeover(ctx, row.ID, row.Generation)
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("idempotency: takeover of a failed claim matched no row")
			}
			claim = newClaim(taken, fp)
			return nil
		default:
			return fmt.Errorf("idempotency: unsupported status %q", row.Status)
		}
	})
	if err != nil {
		return nil, nil, err
	}
	return claim, replay, nil
}

// Complete 记下成功且可重放的响应。ctx 里有事务时随该事务提交（业务事务内完成），否则立即提交。
func (s *Service) Complete(ctx context.Context, c *Claim, r Response) error {
	f, err := successFinish(c, r)
	if err != nil {
		return err
	}
	return s.finish(ctx, c, f)
}

// CompleteBound 同 Complete，并把认领绑定到业务资源（如订单）；须在创建该资源的同一事务里调用。
func (s *Service) CompleteBound(ctx context.Context, c *Claim, r Response, res Resource) error {
	if err := validateResourceType(res.Type); err != nil {
		return err
	}
	if res.ID == uuid.Nil {
		return errors.New("idempotency: resource id is empty")
	}
	f, err := successFinish(c, r)
	if err != nil {
		return err
	}
	f.ResourceType = &res.Type
	f.ResourceID = &res.ID
	return s.finish(ctx, c, f)
}

// CompleteUnreplayable 记下成功但响应没能完整录下：之后同一请求回 409 conflict，不再执行。
func (s *Service) CompleteUnreplayable(ctx context.Context, c *Claim, status int) error {
	if c == nil {
		return errors.New("idempotency: nil claim")
	}
	if !is2xx(status) {
		return fmt.Errorf("idempotency: unreplayable completion needs a 2xx status, got %d", status)
	}
	return s.finish(ctx, c, repo.Finish{Status: model.StatusSucceeded, Code: status})
}

// Release 记下失败（非 2xx）并释放认领：之后同一请求可接管重试。
func (s *Service) Release(ctx context.Context, c *Claim, status int) error {
	if c == nil {
		return errors.New("idempotency: nil claim")
	}
	if status < 100 || status > 999 || is2xx(status) {
		return fmt.Errorf("idempotency: release needs a non-2xx status, got %d", status)
	}
	return s.finish(ctx, c, repo.Finish{Status: model.StatusFailed, Code: status})
}

// Purge 删除一批 created_at 早于 before 的记录，返回删除行数；调用方循环直到返回值小于 batch。
func (s *Service) Purge(ctx context.Context, before time.Time, batch int) (int64, error) {
	if before.IsZero() || batch <= 0 {
		return 0, errors.New("idempotency: purge needs a cutoff and a positive batch")
	}
	return s.repo.Purge(ctx, before, batch)
}

// finish 按 (id, actor, generation, in_flight) 做 CAS；成功后登记提交后标记已完成。
// 不自己开事务：ctx 里有事务就在调用方事务里写。
func (s *Service) finish(ctx context.Context, c *Claim, f repo.Finish) error {
	ok, err := s.repo.Finish(ctx, c.ID, c.ActorID, c.Generation, f)
	if err != nil {
		return err
	}
	if !ok {
		return ErrClaimLost
	}
	tx.AfterCommit(ctx, "", func(context.Context) { c.completed.Store(true) })
	return nil
}

func successFinish(c *Claim, r Response) (repo.Finish, error) {
	if c == nil {
		return repo.Finish{}, errors.New("idempotency: nil claim")
	}
	if !is2xx(r.Status) {
		return repo.Finish{}, fmt.Errorf("idempotency: completion needs a 2xx status, got %d", r.Status)
	}
	if len(r.Body) > MaxBodyBytes {
		return repo.Finish{}, errors.New("idempotency: response exceeds the replay limit")
	}
	if err := r.Header.Validate(); err != nil {
		return repo.Finish{}, err
	}
	body := r.Body
	if body == nil {
		body = []byte{}
	}
	return repo.Finish{
		Status:          model.StatusSucceeded,
		Code:            r.Status,
		Replayable:      true,
		Body:            body,
		ContentType:     optional(r.Header.ContentType),
		Location:        optional(r.Header.Location),
		ETag:            optional(r.Header.ETag),
		CacheControl:    optional(r.Header.CacheControl),
		ContentLanguage: optional(r.Header.ContentLanguage),
	}, nil
}

func busy(ctx context.Context, scope string) error {
	plog.From(ctx).Warn("idempotency claim busy or awaiting reconciliation", zap.String("scope", scope))
	return errs.Newf(errs.Conflict, "同一请求正在处理或需要人工核对，请稍后重试")
}

func newClaim(row model.IdempotencyKey, fp Fingerprint) *Claim {
	c := &Claim{
		ID:          row.ID,
		Scope:       row.Scope,
		ActorID:     row.ActorID,
		Key:         row.Key,
		Fingerprint: fp,
		Generation:  row.Generation,
	}
	if row.LockedUntil != nil {
		c.LockedUntil = *row.LockedUntil
	}
	return c
}

func replayOf(row model.IdempotencyKey) (*Response, error) {
	if row.ResponseCode == nil {
		return nil, errors.New("idempotency: succeeded record has no response code")
	}
	body := row.ResponseBody
	if body == nil {
		body = []byte{}
	}
	return &Response{
		Status: *row.ResponseCode,
		Body:   body,
		Header: Header{
			ContentType:     deref(row.ResponseContentType),
			Location:        deref(row.ResponseLocation),
			ETag:            deref(row.ResponseETag),
			CacheControl:    deref(row.ResponseCacheControl),
			ContentLanguage: deref(row.ResponseContentLanguage),
		},
	}, nil
}

func is2xx(status int) bool { return status >= http.StatusOK && status < http.StatusMultipleChoices }

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

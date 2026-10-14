package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// Emails 批量取用户邮箱（一条 SQL），给审计显示操作者用；实现 audit 的 ActorDirectory。
func (s *Service) Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	return s.repo.Emails(ctx, ids)
}

// IsDeleted 报告用户是否已注销；用户不存在返回 false（由调用方按自己的口径处理不存在）。
// 别的模块对用户的写入口在自己的事务里先调它（排在锁自己的行之前）：读时给用户行加共享锁并持有到提交，
// 与注销（先锁用户行，再取消待付订单、停订阅）互斥，注销提交前进来的写入要等，提交后读到 deleted 被拒。
func (s *Service) IsDeleted(ctx context.Context, userID uuid.UUID) (bool, error) {
	st, ok, err := s.repo.UserStatusShared(ctx, userID)
	if err != nil || !ok {
		return false, err
	}
	return st == StatusDeleted, nil
}

// AccountDeleted 报告用户是否已注销，无锁读（用户不存在返回 false）。给不能等用户行锁的读面用：commission 在订单结算事务里
// 判断邀请人是否已注销，那时已持有库存、券与平台收入等行锁，再去等邀请人的用户行会与注销（锁用户行后释放他的待付单）互等。
// 竞态的代价只是注销提交前那一刻的一单仍记给他。写入口要互斥时用 IsDeleted。
func (s *Service) AccountDeleted(ctx context.Context, userID uuid.UUID) (bool, error) {
	u, ok, err := s.repo.UserByID(ctx, userID)
	if err != nil || !ok {
		return false, err
	}
	return u.Status == StatusDeleted, nil
}

// UserGroupID 取用户所在的用户组，没有分组时返回 nil；用户不存在回 errs.NotFound（catalog、promo 按组过滤用）。
func (s *Service) UserGroupID(ctx context.Context, userID uuid.UUID) (*uuid.UUID, error) {
	u, ok, err := s.repo.UserByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errs.New(errs.NotFound)
	}
	return u.UserGroupID, nil
}

// HasReferrer 判断用户是不是经邀请注册的（giftcard「仅受邀用户」条件用）。
func (s *Service) HasReferrer(ctx context.Context, userID uuid.UUID) (bool, error) {
	_, ok, err := s.repo.Referrer(ctx, userID)
	return ok, err
}

// IDsByEmail 返回邮箱包含 q 的用户 id（一条 SQL，最多 limit 个，limit 超过 1000 按 1000）；实现 audit 的 ActorDirectory。
func (s *Service) IDsByEmail(ctx context.Context, q string, limit int) ([]uuid.UUID, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	if limit < 1 {
		limit = 50
	}
	if limit > directoryMaxLimit {
		limit = directoryMaxLimit
	}
	return s.repo.IDsByEmailLike(ctx, q, limit)
}

// UserLabels 是审计里资源类型 "user" 的可读名（邮箱）；签名与 audit 的 ResourceLabeler 一致。
func (s *Service) UserLabels(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	return s.Emails(ctx, ids)
}

// PurgeRegistrations 删 before 之前创建的注册会话，最多 batch 行，返回删除行数（保留期任务用）。
func (s *Service) PurgeRegistrations(ctx context.Context, before time.Time, batch int) (int, error) {
	if batch < 1 || batch > 10000 {
		return 0, fmt.Errorf("identity: purge batch 须在 1 到 10000 之间")
	}
	return s.repo.PurgeRegistrations(ctx, before, batch)
}

// PurgeCodes 删 before 之前创建的验证码，最多 batch 行，返回删除行数（保留期任务用）。
func (s *Service) PurgeCodes(ctx context.Context, before time.Time, batch int) (int, error) {
	if batch < 1 || batch > 10000 {
		return 0, fmt.Errorf("identity: purge batch 须在 1 到 10000 之间")
	}
	return s.repo.PurgeCodes(ctx, before, batch)
}

var _ auditsvc.ActorDirectory = (*Service)(nil)

func _() {
	var s *Service
	var _ auditsvc.ResourceLabeler = s.UserLabels
}

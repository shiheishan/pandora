package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/google/uuid"
)

// Emails 批量取用户邮箱（一条 SQL），给审计显示操作者用；实现 audit 的 ActorDirectory。
func (s *Service) Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	return s.repo.Emails(ctx, ids)
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

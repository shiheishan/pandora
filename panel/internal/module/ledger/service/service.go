package service

import (
	"context"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/ledger/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
)

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// auditWriter 是 audit service 的写入面。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// AccountStates 是 identity 的账号状态读取面（*identitysvc.Service 直接满足）：IsDeleted 读时给用户行加共享锁并持有到提交。
type AccountStates interface {
	IsDeleted(ctx context.Context, userID uuid.UUID) (bool, error)
}

// Service 是 ledger 模块对外的唯一入口。
type Service struct {
	repo     *repo.Repo
	tx       txRunner
	audit    auditWriter
	accounts AccountStates // 后台调账前拒已注销账号，装配时注入（UseAccountStates）
}

// New 构造 ledger service。
func New(r *repo.Repo, txm *tx.Manager, audit *auditsvc.Service) *Service {
	return &Service{
		repo:  r,
		tx:    txm,
		audit: audit,
	}
}

// UseAccountStates 注入 identity 的账号状态读取面；只在启动装配时调用（不并发安全）。
func (s *Service) UseAccountStates(a AccountStates) { s.accounts = a }

// MissingPorts 返回还没注入的必需端口名（装配自检用）。
func (s *Service) MissingPorts() []string {
	if s.accounts == nil {
		return []string{"AccountStates"}
	}
	return nil
}

// principal 取 ctx 里的登录主体；没有主体或 UserID 为零时回 401。
func principal(ctx context.Context) (authn.Principal, error) {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return authn.Principal{}, errs.New(errs.Unauthorized)
	}
	return p, nil
}

// actorOf 返回记账交易上的操作者种类与 ID。
func actorOf(ctx context.Context) (kind string, id *uuid.UUID) {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return "system", nil
	}
	uid := p.UserID
	if p.Audience == authn.AudienceAdmin {
		return "admin", &uid
	}
	return "user", &uid
}

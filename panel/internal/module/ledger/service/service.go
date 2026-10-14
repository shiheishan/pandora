package service

import (
	"context"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/ledger/repo"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
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

// siteReader 是 ledger 用到的站点设置读取面。
type siteReader interface {
	Site(ctx context.Context) (settingssvc.Site, error)
}

// Service 是 ledger 模块对外的唯一入口。
type Service struct {
	repo     *repo.Repo
	tx       txRunner
	settings siteReader
	audit    auditWriter
}

// New 构造 ledger service。
func New(r *repo.Repo, txm *tx.Manager, settings *settingssvc.Service, audit *auditsvc.Service) *Service {
	return &Service{
		repo:     r,
		tx:       txm,
		settings: settings,
		audit:    audit,
	}
}

// DefaultCurrency 返回站点币种（settings 的 site.currency），解析失败报错。
func (s *Service) DefaultCurrency(ctx context.Context) (money.Currency, error) {
	site, err := s.settings.Site(ctx)
	if err != nil {
		return "", err
	}
	return money.ParseCurrency(site.Currency)
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

// Package service 是 promo 模块的业务层：优惠券的定义、批量生成、试算、占用、释放与确认，以及后台管理。别的模块只调本包。
package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/promo/repo"
	settingsvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// auditWriter 是 audit service 的写入面。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// SiteSettings 是 settings service 的只读面：建券没给币种时取站点币种。*settingsvc.Service 直接满足。
type SiteSettings interface {
	Site(ctx context.Context) (settingsvc.Site, error)
}

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// Service 是 promo 模块对外的业务入口。
type Service struct {
	repo    *repo.Repo
	tx      txRunner
	audit   auditWriter
	site    SiteSettings
	catalog CatalogReader // UseCatalog 注入
	users   UserDirectory // UseUsers 注入
	now     func() time.Time
}

// New 构造 promo service；site 传 settings service。
func New(r *repo.Repo, txm *tx.Manager, audit *auditsvc.Service, site SiteSettings) *Service {
	return &Service{
		repo:  r,
		tx:    txm,
		audit: audit,
		site:  site,
		now:   time.Now,
	}
}

// UseCatalog 只在启动装配时调用（不并发安全）。
func (s *Service) UseCatalog(c CatalogReader) { s.catalog = c }

// UseUsers 只在启动装配时调用（不并发安全）。
func (s *Service) UseUsers(u UserDirectory) { s.users = u }

var errNotWired = errors.New("promo: 依赖未注入")

func (s *Service) needCatalog() (CatalogReader, error) {
	if s.catalog == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.catalog, nil
}

func (s *Service) needUsers() (UserDirectory, error) {
	if s.users == nil {
		return nil, errs.Wrap(errs.Internal, errNotWired)
	}
	return s.users, nil
}

// withSiteCurrency 在规格没给币种时填站点币种；站点币种不是券支持的币种时由 validateSpec 报 422。
func (s *Service) withSiteCurrency(ctx context.Context, spec Spec) (Spec, error) {
	if strings.TrimSpace(spec.Currency) != "" {
		return spec, nil
	}
	site, err := s.site.Site(ctx)
	if err != nil {
		return Spec{}, err
	}
	spec.Currency = site.Currency
	return spec, nil
}

// actorID 取当前登录用户 ID，用于 coupons.created_by。
func actorID(ctx context.Context) *uuid.UUID {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return nil
	}
	id := p.UserID
	return &id
}

// MissingPorts 返回还没注入的必需端口名（装配完成后由 internal/app 统一检查）。
func (s *Service) MissingPorts() []string {
	var out []string
	for _, p := range []struct {
		name string
		ok   bool
	}{
		{"CatalogReader", s.catalog != nil},
		{"UserDirectory", s.users != nil},
	} {
		if !p.ok {
			out = append(out, p.name)
		}
	}
	return out
}

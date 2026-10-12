// Package service 是 subscription 模块的业务层：订阅、配额、流量包的开通、续期、延期、换套餐、
// 加流量包与转移、手动重置、改设备数、备注名，以及状态口径与按用户批量读。
//
// 给别的模块用的写方法（Provision、Renew、Extend、ChangePlan、GrantTrafficPack、TransferPacks、ResetTraffic）
// 都经 s.tx.Do 执行：ctx 里已有事务就加入调用方的事务，钱与订阅在同一个事务里落库。
// 这些方法自己锁订阅行（FOR UPDATE）；调用方先锁订单、再调这里（锁序：订单 → 订阅 → 配额 → 流量包）。
//
// 状态口径（生效、可救回、已停用）只在 state.go 定义；别的模块调 StateAt 或 SummariesByUsers，不再自己写这类 SQL。
// 名单变化（会影响节点下发的事实）在提交后经可选的 RosterNotifier 通知（P5 由 node 注册）。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
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

// Catalog 是 subscription 要的目录读取面；*catalogsvc.Service 直接满足。
// 只读套餐、版本（权益与配额定义）与价格的展示信息，不在这里判断能不能买（那是 order 经 catalog.Offer 做的）。
type Catalog interface {
	PlansByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Plan, error)
	VersionsByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Version, error)
	PricesByIDs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]catalogsvc.Price, error)
}

// Users 是 subscription 要的用户读取面；*identitysvc.Service 直接满足。
type Users interface {
	// Emails 批量取用户邮箱（一条 SQL）。
	Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
	// UserGroupID 取用户所在的用户组；没有分组返回 nil。
	UserGroupID(ctx context.Context, userID uuid.UUID) (*uuid.UUID, error)
}

// SiteSettings 是站点设置的读取面（配置名里的站点名）；*settingssvc.Service 直接满足。
type SiteSettings interface {
	Site(ctx context.Context) (settingssvc.Site, error)
}

// Sealer 封存订阅链接令牌的原文（AAD 绑定订阅 id）；*crypto.Sealer 直接满足。
type Sealer interface {
	Seal(plain, aad []byte) ([]byte, error)
}

// RosterNotifier 由 node 模块实现（P5）：一份订阅里影响节点下发名单的事实变了
// （开通、续期、延期、换套餐、加流量包、流量包转移、重置流量、改设备数），在事务提交后调用。
// 实现必须很快返回（只记下要重算的订阅、排任务），不能回调本模块的写方法。没注册时什么都不做。
type RosterNotifier interface {
	SubscriptionsChanged(ctx context.Context, subscriptionIDs []uuid.UUID)
}

// Service 是 subscription 模块对外的唯一入口。
type Service struct {
	repo     *repo.Repo
	tx       txRunner
	audit    auditWriter
	settings SiteSettings
	catalog  Catalog
	users    Users
	sealer   Sealer
	roster   RosterNotifier
	now      func() time.Time
}

// New 构造 subscription service。catalog 一般是 catalog service，users 是 identity service，sealer 是 crypto.Keys.Sealer()。
func New(r *repo.Repo, txm *tx.Manager, audit *auditsvc.Service, settings SiteSettings, catalog Catalog, users Users, sealer Sealer) *Service {
	return &Service{
		repo:     r,
		tx:       txm,
		audit:    audit,
		settings: settings,
		catalog:  catalog,
		users:    users,
		sealer:   sealer,
		now:      time.Now,
	}
}

// UseRosterNotifier 只在启动装配时调用（不并发安全）。
func (s *Service) UseRosterNotifier(n RosterNotifier) { s.roster = n }

// clock 返回当前时刻（UTC、截到微秒，与库里的精度一致）。
func (s *Service) clock() time.Time {
	return s.now().UTC().Truncate(time.Microsecond)
}

// notifyRoster 在提交后通知名单变化；同一事务里同一份订阅只通知一次。
func (s *Service) notifyRoster(ctx context.Context, subIDs ...uuid.UUID) {
	for _, id := range subIDs {
		if id == uuid.Nil {
			continue
		}
		subID := id
		tx.AfterCommit(ctx, "subscription:roster:"+subID.String(), func(ctx context.Context) {
			if s.roster != nil {
				s.roster.SubscriptionsChanged(ctx, []uuid.UUID{subID})
			}
		})
	}
}

// writeEvent 追加一条订阅事件；payload 不放钱。
func (s *Service) writeEvent(ctx context.Context, subID uuid.UUID, eventType string, from *string, to string, actor Actor, orderID *uuid.UUID, payload map[string]any) error {
	if payload == nil {
		payload = map[string]any{}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return subErr(s.repo.InsertEvent(ctx, &model.Event{
		SubscriptionID: subID,
		EventType:      eventType,
		FromStatus:     from,
		ToStatus:       to,
		ActorKind:      actor.Kind,
		ActorID:        actor.ID,
		OrderID:        orderID,
		Payload:        string(raw),
	}))
}

// writeAudit 在 ctx 的事务里写一条审计（操作者由 audit 从登录主体取）。
func (s *Service) writeAudit(ctx context.Context, action string, subID uuid.UUID, data map[string]any) error {
	id := subID
	return s.audit.Write(ctx, auditsvc.Event{
		Action:       action,
		ResourceType: ResourceSubscription,
		ResourceID:   &id,
		Data:         data,
	})
}

// principal 取 ctx 里的登录主体；没有主体回 401。
func principal(ctx context.Context) (authn.Principal, error) {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return authn.Principal{}, errs.New(errs.Unauthorized)
	}
	return p, nil
}

// adminPrincipal 取后台登录主体；不是后台会话回 403。
func adminPrincipal(ctx context.Context) (authn.Principal, error) {
	p, err := principal(ctx)
	if err != nil {
		return p, err
	}
	if p.Audience != authn.AudienceAdmin {
		return p, errs.New(errs.Forbidden)
	}
	return p, nil
}

// actorFrom 把登录主体转成事件与流水上的操作者（后台 admin、门户 user）。
func actorFrom(p authn.Principal) Actor {
	id := p.UserID
	if p.Audience == authn.AudienceAdmin {
		return Actor{Kind: ActorAdmin, ID: &id}
	}
	return Actor{Kind: ActorUser, ID: &id}
}

// subErr 把 repo 的约束哨兵转成对外错误；已是 *errs.Error 的原样返回。
func subErr(err error) error {
	if err == nil {
		return nil
	}
	var e *errs.Error
	if errors.As(err, &e) {
		return e
	}
	switch {
	case errors.Is(err, repo.ErrDuplicate):
		return errs.Newf(errs.Conflict, "记录已存在")
	case errors.Is(err, repo.ErrForeignKey):
		return errs.Invalid(map[string]string{"reference": "关联对象不存在或不属于这个用户"})
	case errors.Is(err, repo.ErrCheck):
		return errs.Newf(errs.ValidationFailed, "订阅数据校验未通过")
	case errors.Is(err, repo.ErrAppendOnly):
		return errs.Wrap(errs.Internal, err)
	}
	return err
}

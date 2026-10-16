// Package service 是 subscription 模块的业务层：订阅与配额的开通、续期、叠加、延期、换套餐、
// 送流量（配额行的 bonus_value）、手动重置、改设备数、备注名，以及状态口径与按用户批量读；
// 还有订阅分发：链接拉取、链接列表与换链接、线路预览、按日用量、拉取日志的保留期清理。
//
// 给别的模块用的写方法（Provision、Renew、Stack、Extend、ChangePlan、GrantBonus、ResetTraffic）
// 都经 s.tx.Do 执行：ctx 里已有事务就加入调用方的事务，钱与订阅在同一个事务里落库。
// 这些方法自己锁订阅行（FOR NO KEY UPDATE）；调用方先锁订单、再调这里（锁序：订单 → 订阅 → 配额）。
//
// 状态口径（生效、可救回、已停用）只在 state.go 定义；别的模块调 StateAt 或 SummariesByUsers，不再自己写这类 SQL。
// 名单变化（会影响节点下发的事实）在提交后经可选的 RosterNotifier 通知（由 node 模块注册）。
package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/module/subscription/model"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/pluginevents"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
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
	// IsDeleted 报告用户是否已注销（不存在回 false），读时给用户行加共享锁并持有到事务提交（见 rejectDeletedOwner）。
	IsDeleted(ctx context.Context, userID uuid.UUID) (bool, error)
}

// SiteSettings 是站点设置的读取面（配置名里的站点名）；*settingssvc.Service 直接满足。
type SiteSettings interface {
	Site(ctx context.Context) (settingssvc.Site, error)
}

// Sealer 封存与解开订阅链接令牌的原文（AAD 是 CredentialAAD：用途前缀加订阅 id），也封存拉取日志里的 IP 与 UA；
// *crypto.Sealer 直接满足。
type Sealer interface {
	Seal(plain, aad []byte) ([]byte, error)
	Open(sealed, aad []byte) ([]byte, error)
}

// RosterNotifier 由 node 模块实现：一份订阅里影响节点下发名单的事实变了
// （开通、续期、延期、换套餐、加流量包、流量包转移、重置流量、改设备数），在事务提交后调用。
// 实现必须很快返回（只记下要重算的订阅、排任务），不能回调本模块的写方法。没注册时什么都不做。
type RosterNotifier interface {
	SubscriptionsChanged(ctx context.Context, subscriptionIDs []uuid.UUID)
}

// OnlineDeviceCounter 由 node 模块实现：订阅在设备识别窗口内跨节点去重的在线设备数。
// 没有在线记录的订阅不在结果里。没注册时一律按 0。
type OnlineDeviceCounter interface {
	OnlineDeviceCounts(ctx context.Context, subIDs []uuid.UUID) (map[uuid.UUID]int, error)
}

// Service 是 subscription 模块对外的唯一入口。
type Service struct {
	repo     *repo.Repo
	tx       txRunner
	events   pluginevents.Emitter // 插件钩子出站事件，装配时注入（plugin_events.go）
	audit    auditWriter
	settings SiteSettings
	catalog  Catalog
	users    Users
	sealer   Sealer
	roster   RosterNotifier
	online   OnlineDeviceCounter
	now      func() time.Time
	rt       *realtime.Publisher // 实时推送，装配时注入（realtime.go）；nil 时不推

	// 订阅分发：nodes 与 delivery 由装配注入（nodes.go）。
	nodes    NodeSource
	delivery *Delivery
	// prefix 是订阅路径前缀的进程内副本：前缀生成后不再变，读到一次就一直用，前缀不对的请求不碰库。
	prefix atomic.Pointer[string]
	// failures 给未认证失败的拉取日志采样（fetchlog.go）。
	failures *failureSampler
	// extras 是拉取用的后台模板与订阅附加设置的进程内快照（pull_extras.go），30 秒过期。
	extras pullExtras
	// scores 是订阅域名的分数（Redis，domain_scores.go）；装配时经 UseDomainScores 注入，nil 时一律按基础权重
	scores *domainScores
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
		failures: newFailureSampler(failureSampleWindow, failureSamplesPerWindow),
	}
}

// UseRosterNotifier 只在启动装配时调用（不并发安全）。
func (s *Service) UseRosterNotifier(n RosterNotifier) { s.roster = n }

// UseOnlineDeviceCounter 只在启动装配时调用（不并发安全）。
func (s *Service) UseOnlineDeviceCounter(c OnlineDeviceCounter) { s.online = c }

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

// notifyRosterBatch 在提交后把一批订阅一次通知出去（扣量一批可能有很多份同时用尽）。
func (s *Service) notifyRosterBatch(ctx context.Context, subIDs []uuid.UUID) {
	ids := make([]uuid.UUID, 0, len(subIDs))
	for _, id := range subIDs {
		if id != uuid.Nil {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	// 键每批唯一：同一事务里两批不能互相吞掉
	tx.AfterCommit(ctx, "subscription:roster:batch:"+uuid.NewString(), func(ctx context.Context) {
		if s.roster != nil {
			s.roster.SubscriptionsChanged(ctx, ids)
		}
	})
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

// deliveryReady 与 needDelivery 一致：分发依赖齐全才算已注入。
func (s *Service) deliveryReady() bool {
	return s.delivery != nil && s.delivery.Settings != nil && s.delivery.Owners != nil &&
		s.delivery.Hasher != nil && s.delivery.Limiter != nil
}

// MissingPorts 返回还没注入的必需端口名（装配完成后由 internal/app 统一检查）。
func (s *Service) MissingPorts() []string {
	var out []string
	for _, p := range []struct {
		name string
		ok   bool
	}{
		{"Delivery", s.deliveryReady()},
		{"NodeSource", s.nodes != nil},
		{"OnlineDeviceCounter", s.online != nil},
		{"RosterNotifier", s.roster != nil},
		{"PluginEvents", s.events != nil},
		{"Realtime", s.rt != nil},
		{"DomainScores", s.scores != nil},
	} {
		if !p.ok {
			out = append(out, p.name)
		}
	}
	return out
}

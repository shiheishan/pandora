// Package service 是 support 模块的业务层：门户的提单、我的工单、回复、关闭、撤回；
// 后台的队列、详情、客服回复与内部备注、指派、改状态、SLA 升级、快捷回复。
//
// 两条必须守住的性质：
//
//  1. 内部备注对用户不可见。门户的读取路径一律在 SQL 里带 internal_note = false，不在 Go 里过滤；
//     门户的消息作者名只给用户自己的显示名，客服一律为 null。
//  2. SLA 超时自动升级。截止时间建单时按优先级算好，扫描只做「过期且未响应 → escalated」这一件幂等的事。
//
// 工单正文可能含用户隐私：审计只记 id、状态、计数，不记正文、标题、撤回说明、改状态原因。
// 状态转换走转换表（CanTransition）加 WHERE status = 旧状态（docs/architecture.md 第 5.4 节）。
package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/support/model"
	"github.com/aegispanel/aegis/internal/module/support/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/pluginevents"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// auditWriter 是 audit service 的写入面。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// idempotency 是幂等 service 在业务事务内完成认领的方法；*idemsvc.Service 直接满足。
type idempotency interface {
	Complete(ctx context.Context, c *idemsvc.Claim, r idemsvc.Response) error
}

// store 是 service 对 repo 的全部要求；*repo.Repo 实现它。
type store interface {
	// InsertTicket 插入工单，回填 ID、CreatedAt、UpdatedAt。
	InsertTicket(ctx context.Context, t *model.Ticket) error
	// InsertMessages 插入一批消息（一条 SQL），回填 ID、CreatedAt。
	InsertMessages(ctx context.Context, ms []*model.Message) error
	// GetTicket 读工单；owner 非 nil 时只读该用户的。不存在回 false。
	GetTicket(ctx context.Context, id uuid.UUID, owner *uuid.UUID) (model.Ticket, bool, error)
	// LockTicket 同 GetTicket，带 FOR UPDATE。
	LockTicket(ctx context.Context, id uuid.UUID, owner *uuid.UUID) (model.Ticket, bool, error)
	// UpdateTicket 按 id 且 status = from 更新 cols（列名 → 值，nil 写 NULL），并写 updated_at；返回是否改到行。
	UpdateTicket(ctx context.Context, id uuid.UUID, from string, cols map[string]any) (bool, error)
	// LockUserTickets 取该用户建单的事务级咨询锁（串行化「未结工单上限」）。
	LockUserTickets(ctx context.Context, userID uuid.UUID) error
	// CountOpenTickets 数该用户未解决、未关闭的工单。
	CountOpenTickets(ctx context.Context, userID uuid.UUID) (int64, error)
	// UserTickets 门户「我的工单」：按 updated_at、id 倒序，最多 limit 张；统计只数非内部备注。
	UserTickets(ctx context.Context, userID uuid.UUID, limit int) ([]repo.TicketRow, error)
	// Queue 后台队列：升级的最前，再按优先级（urgent→low）、建单时间、id；统计数全部消息。返回本页与总数。
	Queue(ctx context.Context, f repo.QueueFilter) ([]repo.TicketRow, int64, error)
	// Messages 一张工单的消息，按 created_at、id 升序；includeInternal 为 false 时 SQL 里排除内部备注。
	Messages(ctx context.Context, ticketID uuid.UUID, includeInternal bool) ([]model.Message, error)
	// EscalateOverdue 把首次响应超时、未响应、未解决未关闭、从没升级过的工单（最多 limit 张，FOR UPDATE SKIP LOCKED）
	// 置为 escalated、写 escalated_at、优先级升一级（low→normal→high→urgent），返回改动的工单（按 id 升序）。
	EscalateOverdue(ctx context.Context, now time.Time, limit int) ([]repo.Escalated, error)
	// TicketNos 批量取工单号。
	TicketNos(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
	// ListMacros 全部快捷回复，按 sort_order、created_at、id 升序。
	ListMacros(ctx context.Context) ([]model.Macro, error)
	// InsertMacro 插入快捷回复，回填 ID 与时间。
	InsertMacro(ctx context.Context, m *model.Macro) error
	// UpdateMacro 覆盖标题、正文、排序；不存在回 false。
	UpdateMacro(ctx context.Context, id uuid.UUID, title, body string, sortOrder int) (bool, error)
	// DeleteMacro 删除并返回原标题；不存在回 false。
	DeleteMacro(ctx context.Context, id uuid.UUID) (string, bool, error)
}

var _ store = (*repo.Repo)(nil)

// Users 是 support 要的用户读取面；*identitysvc.Service 直接满足。
type Users interface {
	// Contacts 批量取邮箱与显示名（一条 SQL）。
	Contacts(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]identitysvc.Contact, error)
	// IDsByEmail 邮箱包含 q 的用户（最多 limit 个）。
	IDsByEmail(ctx context.Context, q string, limit int) ([]uuid.UUID, error)
}

// Admins 是管理员名单（属 auth）；*authsvc.Service 直接满足。可指派的客服就是可登录的管理员。
type Admins interface {
	// ActiveAdminIDs 返回在名单里且账号 active 的管理员。
	ActiveAdminIDs(ctx context.Context) ([]uuid.UUID, error)
	// IsActiveAdmin 报告该用户此刻是不是可登录的管理员。
	IsActiveAdmin(ctx context.Context, userID uuid.UUID) (bool, error)
}

// Orders 校验门户用户关联的订单（属 order），由 app 装配适配器实现。
type Orders interface {
	// OwnedOrderNo 返回 ctx 里门户主体名下这张订单的订单号；不存在或不是本人的回 errs.NotFound。
	OwnedOrderNo(ctx context.Context, orderID uuid.UUID) (string, error)
}

// PlanNames 给后台队列与详情显示提单人在用的套餐（属 subscription），由 app 装配适配器实现。
type PlanNames interface {
	// ActivePlanNames 返回这些用户当前订阅生效时的套餐名；没有生效订阅的用户不在结果里。
	ActivePlanNames(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]string, error)
}

// ReplyNotifier 由 notify 实现：客服公开回复后，在回复的同一事务里给提单人排一条「工单有新回复」通知
// （旧模板 ticket.replied，变量 subject；去重键 "ticket-replied:" + MessageID），提交后由 notify 自己催派发。
// 返回错误时回复一起回滚。内部备注用户看不见，不通知。
type ReplyNotifier interface {
	TicketReplied(ctx context.Context, n ReplyNotice) error
}

// ReplyNotice 是一条客服回复通知的内容。
type ReplyNotice struct {
	UserID    uuid.UUID
	TicketID  uuid.UUID
	MessageID uuid.UUID
	TicketNo  string
	Subject   string
}

// Service 是 support 模块对外的唯一入口。
type Service struct {
	repo     store
	tx       txRunner
	events   pluginevents.Emitter // 插件钩子出站事件，装配时注入（plugin_events.go）
	audit    auditWriter
	idem     idempotency
	users    Users
	admins   Admins
	orders   Orders
	plans    PlanNames
	notifier ReplyNotifier
	now      func() time.Time
	rt       *realtime.Publisher // 实时推送（realtime.go 的 UseRealtime）；nil 时不推
}

// New 构造 support service。users 是 identity service，admins 是 auth service。
func New(r *repo.Repo, txm *tx.Manager, audit *auditsvc.Service, idem *idemsvc.Service, users Users, admins Admins) *Service {
	return newService(r, txm, audit, idem, users, admins)
}

// newService 是 New 的接口版本，单测用假实现。
func newService(r store, txm txRunner, audit auditWriter, idem idempotency, users Users, admins Admins) *Service {
	return &Service{
		repo:   r,
		tx:     txm,
		audit:  audit,
		idem:   idem,
		users:  users,
		admins: admins,
		now:    func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) },
	}
}

// UseOrders 接上订单校验（启动装配时调用，不并发安全）。没接上时带 order_id 建单回 422。
func (s *Service) UseOrders(o Orders) { s.orders = o }

// UsePlanNames 接上在用套餐名（启动装配时调用）。没接上时队列与详情不带 user_active_plan。
func (s *Service) UsePlanNames(p PlanNames) { s.plans = p }

// UseReplyNotifier 接上客服回复通知（启动装配时调用）。
func (s *Service) UseReplyNotifier(n ReplyNotifier) { s.notifier = n }

// MissingPorts 返回没装配的必需端口名（装配完成后由 internal/app 统一检查）。
func (s *Service) MissingPorts() []string {
	var out []string
	if s.orders == nil {
		out = append(out, "Orders")
	}
	if s.plans == nil {
		out = append(out, "PlanNames")
	}
	if s.notifier == nil {
		out = append(out, "ReplyNotifier")
	}
	if s.events == nil {
		out = append(out, "PluginEvents")
	}
	if s.rt == nil {
		out = append(out, "Realtime")
	}
	return out
}

// completeIdem 在当前事务里完成幂等认领；ctx 里没有认领或 reply 为 nil 时什么都不做。
func (s *Service) completeIdem(ctx context.Context, reply func() (idemsvc.Response, error)) error {
	claim, ok := idemsvc.ClaimFrom(ctx)
	if !ok || reply == nil {
		return nil
	}
	resp, err := reply()
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	return s.idem.Complete(ctx, claim, resp)
}

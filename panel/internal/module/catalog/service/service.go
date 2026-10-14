// Package service 是 catalog 模块的业务层：套餐、套餐版本、价格、权益与配额定义、流量包，
// 以及给 order、subscription、giftcard、promo 用的下单快照、批量读与库存预留。
//
// 后台写用例拆两段：事务外的 prepare*/validate* 规整与校验，事务里的 *Tx 假定输入已校验，
// 向导因此能把多步编排进同一个事务，任一步失败库里不留半成品。
// 目录冻结（已发布版本及其权益、配额不可改，价格只能归档）在这里拒绝，库里的触发器兜底。
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/catalog/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
)

// auditWriter 是 audit service 的写入面。
type auditWriter interface {
	Write(ctx context.Context, e auditsvc.Event) error
}

// txRunner 是 *tx.Manager 的事务入口。
type txRunner interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}

// Users 是 catalog 要的用户读取面，由 identity 实现、启动时传入。
type Users interface {
	// Emails 批量取用户邮箱（一条 SQL），后台版本行显示「谁建的」。
	Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
	// UserGroupID 取用户所在的用户组；没有分组时返回 nil；用户不存在返回 errs.NotFound。
	UserGroupID(ctx context.Context, userID uuid.UUID) (*uuid.UUID, error)
}

// PublishGate 由 node 模块实现：发布版本前检查绑定的线路里有可服务的节点，不满足时返回 422。
// 在调用方事务里执行。没注入时跳过这项检查。
type PublishGate interface {
	CheckPublishable(ctx context.Context, versionID uuid.UUID) error
}

// Service 是 catalog 模块对外的唯一入口。
type Service struct {
	repo  *repo.Repo
	tx    txRunner
	audit auditWriter
	users Users
	gate  PublishGate
	// pools 由 node 实现（plan_pools.go 的 PoolGuard），向导绑池时锁池。
	pools PoolGuard
	now   func() time.Time
	rt    *realtime.Publisher // 实时推送（realtime.go 的 UseRealtime）；nil 时不推
}

// New 构造 catalog service。users 一般是 identity service。
func New(r *repo.Repo, txm *tx.Manager, audit *auditsvc.Service, users Users) *Service {
	return &Service{repo: r, tx: txm, audit: audit, users: users, now: time.Now}
}

// UsePublishGate 只在启动装配时调用（不并发安全）。
func (s *Service) UsePublishGate(g PublishGate) { s.gate = g }

// 审计动作名。
const (
	ActionPlanCreate     = "plan.create"
	ActionPlanUpdate     = "plan.update"
	ActionPlanArchive    = "plan.archive"
	ActionVersionCreate  = "plan_version.create"
	ActionVersionUpdate  = "plan_version.update"
	ActionVersionPublish = "plan_version.publish"
	ActionPriceCreate    = "price.create"
	ActionPriceArchive   = "price.archive"
	ActionPriceSync      = "price.sync"
	ActionPackCreate     = "traffic_pack.create"
	ActionPackUpdate     = "traffic_pack.update"
	ActionPackArchive    = "traffic_pack.archive"
	ActionPackRestore    = "traffic_pack.restore"
)

// 审计资源类型。
const (
	ResourcePlan        = "plan"
	ResourcePlanVersion = "plan_version"
	ResourcePrice       = "price"
	ResourceTrafficPack = "traffic_pack"
)

// 对外的固定错误。同一个变量只读共享，调用方可以 errors.Is 比较。
var (
	// ErrNotFound 是不存在或对这个用户不可见（不暴露存在与否）。
	ErrNotFound = errs.New(errs.NotFound)
	// ErrNewPurchaseClosed 是套餐关了新购。
	ErrNewPurchaseClosed = errs.Newf(errs.Conflict, "该套餐当前不接受新购")
	// ErrNoPublishedVersion 是套餐还没有发布过版本。
	ErrNoPublishedVersion = errs.Newf(errs.Conflict, "该套餐尚未发布可用版本")
	// ErrPriceArchived 是价格已归档。
	ErrPriceArchived = errs.Newf(errs.Conflict, "该价格已下架")
	// ErrPriceOutOfWindow 是价格不在有效期内。
	ErrPriceOutOfWindow = errs.Newf(errs.Conflict, "该价格当前不在有效期内")
	// ErrRenewalNotAllowed 是套餐关了续费。
	ErrRenewalNotAllowed = errs.Newf(errs.Conflict, "该套餐当前不允许续费")
	// ErrRenewPriceGone 是续费选的价格不能用了（下架、过期、不对这个用户开放、不属于这个套餐）。
	ErrRenewPriceGone = errs.Newf(errs.Conflict, "所选价格已下架，请重新选择")
	// ErrChangeNotAllowed 是目标套餐关了变更。
	ErrChangeNotAllowed = errs.Newf(errs.Conflict, "目标套餐不允许变更")
	// ErrSoldOut 是有限库存已经卖完（含已预留）。
	ErrSoldOut = errs.Newf(errs.Conflict, "该套餐已售罄")
	// ErrPackArchived 是流量包已下架。
	ErrPackArchived = errs.Newf(errs.Conflict, "流量包已下架")
	// ErrSnapshotFrozen 是改到了已发布的目录快照（库里触发器拒绝时也映射成它）。
	ErrSnapshotFrozen = errs.Newf(errs.Conflict, "已发布目录快照不可修改")
)

// errStockLost 是释放或扣减库存时预留对不上（调用方记账出错），对外是 internal_error。
var errStockLost = errors.New("catalog: 库存预留对不上")

// 状态转换表（docs/architecture.md 第 5.4 节）：repo 的状态更新带 WHERE status = 旧状态，影响 0 行当并发冲突。
var (
	planTransitions = map[string][]string{
		PlanDraft:  {PlanActive, PlanArchived},
		PlanActive: {PlanActive, PlanArchived},
	}
	priceTransitions = map[string][]string{
		PriceActive: {PriceArchived},
	}
)

func canTransition(table map[string][]string, from, to string) bool {
	for _, t := range table[from] {
		if t == to {
			return true
		}
	}
	return false
}

// CanTransitionPlan 报告套餐能否从 from 变到 to（active → active 是发布新版本）。
func CanTransitionPlan(from, to string) bool { return canTransition(planTransitions, from, to) }

// CanTransitionPrice 报告价格能否从 from 变到 to。
func CanTransitionPrice(from, to string) bool { return canTransition(priceTransitions, from, to) }

// rowConflict 是乐观锁冲突：409，fields 带当前值（前端只 Toast 信封原文）。
func rowConflict(kind string, current int64) error {
	return &errs.Error{
		Code:    errs.Conflict,
		Message: kind + "已被其他管理员修改，请刷新后重试",
		Fields:  map[string]string{"row_version": fmt.Sprintf("current=%d", current)},
	}
}

// packConflict 是流量包的乐观锁冲突（乐观锁是 updated_at）。
func packConflict(current time.Time) error {
	return &errs.Error{
		Code:    errs.Conflict,
		Message: "流量包已被其他管理员修改，请刷新后重试",
		Fields:  map[string]string{"updated_at": "current=" + current.UTC().Format(time.RFC3339Nano)},
	}
}

// catalogErr 把 repo 的约束哨兵转成对外错误；已是 *errs.Error 的原样返回。
func catalogErr(err error) error {
	if err == nil {
		return nil
	}
	var e *errs.Error
	if errors.As(err, &e) {
		return e
	}
	switch {
	case errors.Is(err, repo.ErrFrozen):
		return ErrSnapshotFrozen
	case errors.Is(err, repo.ErrDuplicate):
		return errs.Newf(errs.Conflict, "目录对象已存在或活动报价发生冲突")
	case errors.Is(err, repo.ErrForeignKey):
		return errs.Invalid(map[string]string{"reference": "关联对象不存在或已被删除"})
	case errors.Is(err, repo.ErrCheck):
		return errs.Newf(errs.ValidationFailed, "目录语义校验未通过")
	}
	return err
}

// writeAudit 在 ctx 的事务里写一条后台审计（操作者由 audit 从登录主体取）。
func (s *Service) writeAudit(ctx context.Context, action, resourceType string, id uuid.UUID, data map[string]any) error {
	return s.audit.Write(ctx, auditsvc.Event{
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   &id,
		Data:         data,
	})
}

// viewerGroup 取用户的分组；userID 为 nil（匿名）时返回 nil。
func (s *Service) viewerGroup(ctx context.Context, userID *uuid.UUID) (*uuid.UUID, error) {
	if userID == nil || *userID == uuid.Nil {
		return nil, nil
	}
	return s.users.UserGroupID(ctx, *userID)
}

// MissingPorts 返回还没注入的必需端口名（装配完成后由 internal/app 统一检查）。
func (s *Service) MissingPorts() []string {
	var out []string
	for _, p := range []struct {
		name string
		ok   bool
	}{
		{"PublishGate", s.gate != nil},
		{"PoolGuard", s.pools != nil},
		{"Realtime", s.rt != nil},
	} {
		if !p.ok {
			out = append(out, p.name)
		}
	}
	return out
}

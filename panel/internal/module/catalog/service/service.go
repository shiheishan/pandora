// Package service 是 catalog 模块的业务层：两类套餐（订阅 / 一次性）、套餐版本（用量）、价格、流量重置、线路（节点池绑定），
// 以及给 order、subscription、giftcard、promo、node 用的下单快照与批量读。
//
// 后台写用例拆两段：事务外的 validate* 规整与校验，事务里的 *Tx 假定输入已校验；
// 新建套餐因此能把套餐、版本、价格、线路、发布编排进同一个事务，任一步失败库里不留半成品。
// 每个写用例都以套餐的 row_version 做乐观锁（改价、改线路、改用量也推进它）。
// 目录冻结（已发布版本及其权益、配额不可改，价格只能归档）在这里拒绝，库里的触发器兜底。
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/catalog/model"
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

// PublishGate 由 node 模块实现：发布版本前检查套餐绑定的线路里有可服务的节点，不满足时返回 422。
// 在调用方事务里执行（新建即发布时能读到同一事务里刚绑的池）。没注入时跳过这项检查。
type PublishGate interface {
	CheckPublishable(ctx context.Context, planID uuid.UUID) error
}

// Service 是 catalog 模块对外的唯一入口。
type Service struct {
	repo  *repo.Repo
	tx    txRunner
	audit auditWriter
	users Users
	gate  PublishGate
	// pools 由 node 实现（plan_pools.go 的 PoolGuard），绑池时锁池、改绑后推进节点名单纪元。
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
	ActionPlanSales      = "plan.sales_update"
	ActionPlanReset      = "plan.reset_update"
	ActionPlanPrices     = "plan.prices_update"
	ActionPlanPools      = "plan.pools_update"
	ActionPlanUsage      = "plan.usage_update"
	ActionPlanArchive    = "plan.archive"
	ActionVersionPublish = "plan_version.publish"
)

// 审计资源类型。
const (
	ResourcePlan        = "plan"
	ResourcePlanVersion = "plan_version"
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
	// ErrRenewalNotAllowed 是套餐关了续费或已归档（归档后老用户不能续费，到期结束）。
	ErrRenewalNotAllowed = errs.Newf(errs.Conflict, "该套餐当前不允许续费")
	// ErrRenewPriceGone 是续费选的价格不能用了（下架、不属于这个套餐）。
	ErrRenewPriceGone = errs.Newf(errs.Conflict, "所选价格已下架，请重新选择")
	// ErrChangeNotAllowed 是目标套餐关了变更；一次性套餐续费也报它（一次性不能续费）。
	ErrChangeNotAllowed = errs.Newf(errs.Conflict, "目标套餐不允许变更")
	// ErrStackNotAllowed 是叠加的不是一次性套餐（只有一次性套餐再买同一个才叠加）。
	ErrStackNotAllowed = errs.Newf(errs.Conflict, "只有一次性套餐可以叠加")
	// ErrPlanArchived 是改到了已归档的套餐（归档不可恢复、不可编辑）。
	ErrPlanArchived = errs.Newf(errs.Conflict, "已归档套餐不能编辑")
	// ErrSnapshotFrozen 是改到了已发布的目录快照（库里触发器拒绝时也映射成它）。
	ErrSnapshotFrozen = errs.Newf(errs.Conflict, "已发布目录快照不可修改")
)

// 状态转换表（docs/architecture.md 第 5.4 节）：repo 的状态更新带 WHERE status = 旧状态，影响 0 行当并发冲突。
var (
	planTransitions = map[string][]string{
		PlanDraft:  {PlanActive, PlanArchived},
		PlanActive: {PlanActive, PlanArchived},
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

// rowConflict 是乐观锁冲突：409，fields 带当前值（前端只 Toast 信封原文）。
func rowConflict(kind string, current int64) error {
	return &errs.Error{
		Code:    errs.Conflict,
		Message: kind + "已被其他管理员修改，请刷新后重试",
		Fields:  map[string]string{"row_version": fmt.Sprintf("current=%d", current)},
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
		return errs.Newf(errs.Conflict, "目录对象已存在或在售价格发生冲突")
	case errors.Is(err, repo.ErrForeignKey):
		return errs.Invalid(map[string]string{"reference": "关联对象（用户组、节点池）不存在或已被删除"})
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

// lockEditablePlan 在事务里锁住套餐行（FOR UPDATE）并核对：存在、没归档、row_version 等于 expected。
// 所有后台写用例的第一步；之后的改动都在这把锁下进行。
func (s *Service) lockEditablePlan(ctx context.Context, planID uuid.UUID, expected int64) (model.Plan, error) {
	cur, found, err := s.repo.PlanByID(ctx, planID, repo.ForUpdate)
	if err != nil {
		return model.Plan{}, err
	}
	if !found {
		return model.Plan{}, ErrNotFound
	}
	if cur.Status == PlanArchived {
		return model.Plan{}, ErrPlanArchived
	}
	if cur.RowVersion != expected {
		return model.Plan{}, rowConflict("套餐", cur.RowVersion)
	}
	return cur, nil
}

// bumpPlanRow 把套餐 row_version 加一（改价、改线路、改用量这类不落在 plans 行上的改动），返回新值。
func (s *Service) bumpPlanRow(ctx context.Context, planID uuid.UUID, expected int64) (int64, error) {
	ok, err := s.repo.BumpPlanRow(ctx, planID, expected)
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, rowConflict("套餐", expected)
	}
	s.catalogChanged(ctx, rtTablePlans, realtime.OpUpdate, planID)
	return expected + 1, nil
}

// checkExpected 校验客户端带来的乐观锁值。
func checkExpected(expected int64) error {
	if expected <= 0 {
		return errs.Invalid(map[string]string{"expected_row_version": "必须为正整数"})
	}
	return nil
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

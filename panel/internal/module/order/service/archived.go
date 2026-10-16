package service

// 套餐归档后才到账（用户 2026-10-16 定「给他推到余额里面」）：归档前下的新购、续费、叠加、换套餐单，归档时不取消；
// 付款到了由 SettlePayment 按订单行套餐此刻的状态拒收（NotPayable archived_plan），payment 回滚保存点后调 CloseArchivedOrder：
// 不开通、不计佣、不记收入，订单关成 cancelled（cancel_reason = plan_archived），实付经挂账自动转入用户余额。

import (
	"context"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/module/order/model"
	"github.com/aegispanel/aegis/internal/module/order/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// planOrder 报告订单是否买套餐（订单行带套餐）：新购、续费、叠加、换套餐。
func planOrder(kind string) bool {
	switch kind {
	case KindNew, KindRenewal, KindStack, KindUpgrade:
		return true
	default:
		return false
	}
}

// planArchivedFor 读订单行套餐此刻是否已归档（经 catalog，一次查询；不锁套餐行，与归档并发时以先提交的为准）。
// 订单行没有套餐时回 false，由后面的检查处理。
func (s *Service) planArchivedFor(ctx context.Context, it model.Item) (bool, error) {
	if it.PlanID == nil {
		return false, nil
	}
	plans, err := s.catalog.PlansByIDs(ctx, []uuid.UUID{*it.PlanID})
	if err != nil {
		return false, err
	}
	p, ok := plans[*it.PlanID]
	return ok && p.Status == catalogsvc.PlanArchived, nil
}

// CloseArchivedOrder 实现 paymentsvc.Settler：SettlePayment 回 archived_plan 后，payment 在同一事务里调它关单并把钱转余额。
// settle 由 payment 给（挂账分录与挂账案、意图标成功、转入余额），这里负责订单一侧，锁序与结算主链同向（订单 → 券 → 科目 → 意图）：
//
//	锁订单行 → 释放预留与券 → settle（平台科目，再用户余额）→ 释放余额冻结 → 作废其余在途意图 → 订单 cancelled。
//
// 订单已不是待付（与取消、超时并发，已先释放）时没有要释放的，只调 settle：钱照样转余额。
// 同一笔收款重放由 payment 的收款去重挡在前面，到不了这里。
func (s *Service) CloseArchivedOrder(ctx context.Context, orderID uuid.UUID, settle func(ctx context.Context) error) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		o, ok, err := s.repo.OrderByID(ctx, orderID, repo.ForNoKeyUpdate)
		if err != nil {
			return err
		}
		if !ok {
			return errs.Newf(errs.NotFound, "订单不存在")
		}
		if o.Status != StatusPending {
			return settle(ctx)
		}
		_, err = s.releaseOrderWith(ctx, o, StatusCancelled, ReasonPlanArchived, EventCancel, Actor{Kind: ActorPayment}, settle)
		return err
	})
}

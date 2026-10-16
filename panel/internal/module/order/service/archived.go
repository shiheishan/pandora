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

// planArchivedFor 读订单行套餐此刻是否已归档（catalog 的 PlanStatuses，一条只取状态的 SQL；不锁套餐行，与归档并发时以先提交的为准）。
// 订单行没有套餐时回 false，由后面的检查处理。
func (s *Service) planArchivedFor(ctx context.Context, it model.Item) (bool, error) {
	if it.PlanID == nil {
		return false, nil
	}
	statuses, err := s.catalog.PlanStatuses(ctx, []uuid.UUID{*it.PlanID})
	if err != nil {
		return false, err
	}
	return statuses[*it.PlanID] == catalogsvc.PlanArchived, nil
}

// CloseArchivedOrder 实现 paymentsvc.Settler：SettlePayment 回 archived_plan 后，payment 在同一事务里调它关单并把钱转余额。
// settle 由 payment 给（挂账分录与挂账案、意图标成功、转入余额），orderStatus 是订单此刻的状态（记进挂账案）。实际加锁顺序：
//
//	订单行 → 预留 → 券 → settle：平台科目（挂账分录）→ 付款意图（标成功）→ 平台挂账与用户余额（转入）
//	→ 余额冻结行 → 用户冻结与余额（释放冻结）→ 其余在途意图 → 订单状态。
//
// 已知的小概率 40P01：一笔分录内按科目 id 升序加锁，而 ledger_accounts.id 是 uuidv7（按创建先后递增）。
// 平台科目通常早于用户科目建好，这里「先平台、后用户」与单笔分录的 id 序一致；但若某个渠道的 channel_cash 科目晚于
// 这个用户的冻结科目才建（后来才加的渠道），同一用户另一张单走同一渠道正常结算（PostOrderPaid 按 id 先拿冻结、再等渠道科目）
// 与这里（拿着渠道科目、等冻结）可能互等。钱不会错：PG 判死锁后整笔回滚，渠道重推再入账。概率很低，不为它改结算。
//
// 订单已不是待付（保存点回滚到重新锁单之间被取消或超时，冻结与券已由对方释放）时只调 settle，钱照样转余额。
// 同一笔收款重放由 payment 的收款去重挡在前面，到不了这里。
func (s *Service) CloseArchivedOrder(ctx context.Context, orderID uuid.UUID, settle func(ctx context.Context, orderStatus string) error) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		o, ok, err := s.repo.OrderByID(ctx, orderID, repo.ForNoKeyUpdate)
		if err != nil {
			return err
		}
		if !ok {
			return errs.Newf(errs.NotFound, "订单不存在")
		}
		if o.Status != StatusPending {
			return settle(ctx, o.Status)
		}
		_, err = s.releaseOrderWith(ctx, o, StatusCancelled, ReasonPlanArchived, EventCancel, Actor{Kind: ActorPayment},
			func(ctx context.Context) error { return settle(ctx, o.Status) })
		return err
	})
}

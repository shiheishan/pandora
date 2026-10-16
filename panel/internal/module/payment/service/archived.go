package service

// 套餐归档后才到账的付款（用户 2026-10-16 定「给他推到余额里面」）：Settler 回 NotPayable(CaseArchivedPlan) 时，
// 不开通、不计佣、不记收入，订单由 order 关成取消，实付经挂账在同一事务里由系统转入用户余额。
// 账目复用迟到付款的两笔：late_payment_suspense（与别的挂账一样，手续费照记手续费收入）→ late_payment_applied，不新增账本种类。

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/payment/model"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
)

// archivedApplyReason 是自动转入余额写进挂账案的处理原因（库里要求 5 字以上），也是这笔 late_payment_applied 分录的备注。
const archivedApplyReason = "套餐已归档，不开通，实付自动转入余额"

// ErrArchivedPlanOffline 是线下收款（后台标记已支付、人工单线下已收款）遇到订单行套餐已归档：不开通，也不挂账，整笔拒绝。
var ErrArchivedPlanOffline = errs.Newf(errs.Conflict, "套餐已归档，不能开通")

// afterSettleArchived 在保存点回滚之后处理 archived_plan：经 Settler.CloseArchivedOrder 重新锁订单、释放预留与券，
// 在它释放余额冻结之前回调这里记挂账、把意图标成功、再把挂账转入余额（锁序见 Settler 的说明）。
// 同一笔收款的重放在前面按 (渠道, 流水号) 去重挡掉，不会二次入余额；订单关掉后别的收款再到，按 released_order 挂账等后台处理。
func (s *Service) afterSettleArchived(ctx context.Context, evID uuid.UUID, in recordInput, npe *NotPayableError,
	haveIntent bool, intent model.Intent, orderID, userID uuid.UUID, orderNo string, paymentID uuid.UUID, surcharge decimal.Decimal) (RecordResult, error) {

	if in.Offline != nil {
		return RecordResult{}, ErrArchivedPlanOffline
	}
	var result RecordResult
	err := s.settler.CloseArchivedOrder(ctx, orderID, func(ctx context.Context, orderStatus string) error {
		// 挂账案记订单此刻的状态（保存点回滚到重新锁单之间可能已被取消或超时），不用回滚前读到的旧值
		cur := *npe
		if orderStatus != "" {
			cur.OrderStatus = orderStatus
		}
		res, late, err := s.quarantine(ctx, evID, in, &cur, haveIntent, intent, orderID, userID, orderNo, paymentID, surcharge)
		if err != nil {
			return err
		}
		// 自动转入是系统规则做的：账本与审计的操作者固定记系统，不随后台查单补记的管理员走。
		// 案是本事务刚插的，别人看不见，不用再锁
		if _, err := s.applyLateCase(systemActorCtx(ctx), late, archivedApplyReason); err != nil {
			return err
		}
		result = res
		return nil
	})
	if err != nil {
		return RecordResult{}, err
	}
	if !result.Processed {
		return RecordResult{}, errors.New("payment: 归档套餐的付款没有入账")
	}
	return result, nil
}

// systemActorCtx 去掉 ctx 里的登录主体与请求来源 IP（事务、req_id 照旧），让账本记 system、审计记 system。
func systemActorCtx(ctx context.Context) context.Context {
	ctx = authn.With(ctx, authn.Principal{})
	return reqmeta.With(ctx, reqmeta.Meta{})
}

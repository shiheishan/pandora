// 库存预留/释放/扣减必须在调用方已开启的事务里调用（经 ctx 传入同一连接）。
// 只有 ReserveStock 返回 true 的订单才需要 ReleaseStock 或 CaptureStock。
// catalog 只记总数、不认订单，不做防重：调用方必须先在自己的预留行上做状态转换
// （UPDATE … WHERE status = 旧状态，影响 1 行才继续），再在同一事务里调用；
// 重复调用 Release/Capture 会吃掉别的订单的预留。
// 本文件内不开事务，也不先读后写（ReserveStock 在 UPDATE 未命中后用 StockLimited 区分原因）。
// 库存变化在调用方事务提交后推 plans.changed。
package service

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

func validateStockQty(qty int) error {
	if qty <= 0 {
		return errs.Wrap(errs.Internal, fmt.Errorf("catalog: 库存数量必须为正，得到 %d", qty))
	}
	return nil
}

// ReserveStock 为下单预留 qty 份库存，返回是否真的预留了（不限量的套餐返回 false、nil，调用方据此决定之后要不要 Release/Capture）。
// 契约：必须在调用方事务里、与写入自己的预留行（初始状态）同一事务调用；同一订单只调一次。
// 建单时先 Offer(Lock) 再调它不会死锁（套餐行已是 FOR NO KEY UPDATE）。售罄回 ErrSoldOut。
func (s *Service) ReserveStock(ctx context.Context, planID uuid.UUID, qty int) (bool, error) {
	if err := validateStockQty(qty); err != nil {
		return false, err
	}
	ok, err := s.repo.ReserveStock(ctx, planID, qty)
	if err != nil {
		return false, catalogErr(err)
	}
	if ok {
		s.catalogChanged(ctx, rtTablePlans, realtime.OpUpdate, planID)
		return true, nil
	}
	limited, found, err := s.repo.StockLimited(ctx, planID)
	if err != nil {
		return false, catalogErr(err)
	}
	if !found {
		return false, ErrNotFound
	}
	if !limited {
		return false, nil
	}
	return false, ErrSoldOut
}

// ReleaseStock 释放此前 ReserveStock 预留的 qty 份（订单取消、过期）。
// 契约：调用方先在同一事务里把自己的预留行从「已预留」转成「已释放」（WHERE status = 已预留，影响 1 行才继续），再调它；
// catalog 不防重，重复调用会吃掉别的订单的预留。总预留不足时回 internal 错误。
func (s *Service) ReleaseStock(ctx context.Context, planID uuid.UUID, qty int) error {
	if err := validateStockQty(qty); err != nil {
		return err
	}
	ok, err := s.repo.ReleaseStock(ctx, planID, qty)
	if err != nil {
		return catalogErr(err)
	}
	if !ok {
		return errs.Wrap(errs.Internal, errStockLost)
	}
	s.catalogChanged(ctx, rtTablePlans, realtime.OpUpdate, planID)
	return nil
}

// CaptureStock 把预留的 qty 份转成已售（订单付款成功）。
// 契约：调用方先在同一事务里把自己的预留行从「已预留」转成「已扣减」（WHERE status = 已预留，影响 1 行才继续），再调它；
// catalog 不防重，重复调用会吃掉别的订单的预留。总预留不足时回 internal 错误。
func (s *Service) CaptureStock(ctx context.Context, planID uuid.UUID, qty int) error {
	if err := validateStockQty(qty); err != nil {
		return err
	}
	ok, err := s.repo.CaptureStock(ctx, planID, qty)
	if err != nil {
		return catalogErr(err)
	}
	if !ok {
		return errs.Wrap(errs.Internal, errStockLost)
	}
	s.catalogChanged(ctx, rtTablePlans, realtime.OpUpdate, planID)
	return nil
}

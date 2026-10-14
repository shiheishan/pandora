package service

import (
	"context"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/commission/model"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// autoTransferEntry 在解冻的同一事务里把这条佣金转入余额（提现已关闭时）：走 postTransfer（借 可提现佣金 / 贷 用户余额），
// 转余额记录带 entry_id，库里唯一，同一条佣金只会自动转一次（解冻本身也只发生一次）；写审计 commission.auto_transferred_to_balance。
// 调用方已持有条目行锁与该用户的 LockUser。
func (s *Service) autoTransferEntry(ctx context.Context, e model.Entry) error {
	id := e.ID
	txnID, err := s.postTransfer(ctx, e.ReferrerUserID, money.Currency(e.Currency), e.CommissionAmount, &id, "佣金自动转入余额（提现已关闭）")
	if err != nil {
		return err
	}
	return s.audit.Write(ctx, auditsvc.Event{
		Action:       ActionAutoTransferred,
		ResourceType: ResourceCommissionEntry,
		ResourceID:   &id,
		Data: map[string]any{
			"amount":     e.CommissionAmount.StringFixed(2),
			"currency":   e.Currency,
			"ledger_txn": txnID.String(),
			"order_no":   e.OrderNo,
		},
	})
}

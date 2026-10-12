package service

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// RecordOffline 记一笔线下收款（后台「标记已支付」、人工单「线下已收款」），在调用方事务里执行，与回调走同一条入账路径。
// 订单不可付时不开挂账，回 409（调用方事务回滚）。
func (s *Service) RecordOffline(ctx context.Context, in OfflineInput) (RecordResult, error) {
	if in.OrderID == uuid.Nil || in.UserID == uuid.Nil {
		return RecordResult{}, errs.Newf(errs.BadRequest, "线下收款参数不完整")
	}
	orderNo := strings.TrimSpace(in.OrderNo)
	if orderNo == "" || len(orderNo) > 64 {
		return RecordResult{}, errs.Newf(errs.BadRequest, "线下收款参数不完整")
	}
	if !in.Amount.IsPositive() || money.Check(in.Amount) != nil || !in.Currency.Valid() {
		return RecordResult{}, errs.Newf(errs.BadRequest, "线下收款参数不完整")
	}

	ref := strings.TrimSpace(in.Reference)
	if ref == "" || len(ref) > 128 || strings.ContainsRune(ref, 0) {
		return RecordResult{}, errs.Invalid(map[string]string{"reference": "请填写线下凭证号（银行流水号、收据编号等），最多 128 字"})
	}
	reason := in.Reason
	if strings.ContainsRune(reason, 0) || utf8.RuneCountInString(reason) > 500 {
		return RecordResult{}, errs.Invalid(map[string]string{"reason": "原因最多 500 字"})
	}

	p, err := s.loadProvider(ctx, OfflineProviderCode)
	if err != nil {
		return RecordResult{}, err
	}
	if !p.Enabled {
		return RecordResult{}, ErrProviderDisabled
	}

	actorID := ""
	if pr, ok := authn.From(ctx); ok {
		actorID = pr.UserID.String()
	}

	target := &offlineTarget{OrderID: in.OrderID, UserID: in.UserID, OrderNo: orderNo}
	eventID := "offline:" + in.OrderID.String() + ":" + ref

	return s.recordPayment(ctx, recordInput{
		Provider:   p,
		EventID:    eventID,
		EventType:  "payment.succeeded",
		PaymentRef: "offline:" + ref,
		Offline:    target,
		Amount:     in.Amount,
		Fee:        decimal.Zero,
		Currency:   in.Currency,
		Method:     OfflineMethod,
		Raw: map[string]string{
			"source":    "admin_offline",
			"actor_id":  actorID,
			"reference": ref,
			"reason":    reason,
		},
		SignatureVerified: true,
	}, nil)
}

package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// lateStatusFilters 是挂账列表接受的状态筛选；空串表示不筛。
var lateStatusFilters = map[string]bool{
	"": true, string(LateSuspense): true, string(LateApplied): true,
}

// ListLatePayments 后台挂账列表：未处理的排前面；待处理合计按币种分开。
func (s *Service) ListLatePayments(ctx context.Context, status string, limit, offset int) (LateList, error) {
	status = strings.TrimSpace(status)
	if !lateStatusFilters[status] {
		return LateList{}, errs.Newf(errs.BadRequest, "不支持的挂账状态")
	}
	rows, total, err := s.repo.ListLateCases(ctx, status, limit, offset)
	if err != nil {
		return LateList{}, err
	}
	totals, err := s.repo.PendingLateTotals(ctx)
	if err != nil {
		return LateList{}, err
	}

	// 邮箱一次批量取
	seen := make(map[uuid.UUID]bool, len(rows))
	ids := make([]uuid.UUID, 0, len(rows))
	for _, r := range rows {
		if !seen[r.UserID] {
			seen[r.UserID] = true
			ids = append(ids, r.UserID)
		}
	}
	emails := map[uuid.UUID]string{}
	if len(ids) > 0 {
		emails, err = s.identity.Emails(ctx, ids)
		if err != nil {
			return LateList{}, err
		}
	}

	out := LateList{
		Cases:   make([]LateCase, 0, len(rows)),
		Total:   total,
		Pending: make(map[money.Currency]decimal.Decimal, len(totals)),
	}
	for _, r := range rows {
		out.Cases = append(out.Cases, LateCase{
			ID:               r.ID,
			Kind:             CaseKind(r.CaseKind),
			Status:           LateStatus(r.Status),
			Amount:           r.Amount,
			Currency:         money.Currency(r.Currency),
			OrderID:          r.OrderID,
			OrderNo:          r.OrderNo,
			OrderStatus:      r.OrderStatus,
			UserID:           r.UserID,
			UserEmail:        emails[r.UserID],
			ReceivedAt:       r.ReceivedAt,
			ResolvedAt:       r.ResolvedAt,
			ResolutionReason: r.ResolutionReason,
		})
	}
	for _, t := range totals {
		out.Pending[money.Currency(t.Currency)] = t.Amount
	}
	return out, nil
}

// ApplyLatePayment 把挂账转入用户余额：借 late_payment_suspense、贷用户余额；同事务改案状态、写审计。返回记账交易 ID。
func (s *Service) ApplyLatePayment(ctx context.Context, caseID uuid.UUID, reason string) (uuid.UUID, error) {
	reason = strings.TrimSpace(reason)
	if n := utf8.RuneCountInString(reason); !utf8.ValidString(reason) || strings.ContainsRune(reason, 0) || n < 5 || n > 500 {
		return uuid.Nil, errs.Invalid(map[string]string{"reason": "请写清处理原因，5 到 500 个字"})
	}
	var txnID uuid.UUID
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		c, ok, err := s.repo.LockLateCase(ctx, caseID)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		if !CanTransitionLate(LateStatus(c.Status), LateApplied) {
			return ErrCaseResolved
		}
		cur := money.Currency(c.Currency)
		txnID, err = s.ledger.Post(ctx, ledgersvc.Posting{
			Kind:       ledgersvc.KindLatePaymentApplied,
			Currency:   cur,
			SourceType: ledgersvc.SourcePayment,
			SourceID:   c.PaymentID,
			Memo:       reason,
			Legs: []ledgersvc.Leg{
				{Account: ledgersvc.PlatformAccount(ledgersvc.LatePaymentSuspense, ledgersvc.RefMain), Direction: ledgersvc.Debit, Amount: c.Amount},
				{Account: ledgersvc.UserAccount(ledgersvc.UserBalance, c.UserID), Direction: ledgersvc.Credit, Amount: c.Amount},
			},
		})
		if err != nil {
			return err
		}
		applied, err := s.repo.ApplyLateCase(ctx, c.ID, txnID, reason)
		if err != nil {
			return err
		}
		if !applied {
			return errors.New("payment: 挂账状态更新失败")
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       "late_payment.applied",
			ResourceType: "late_payment_case",
			ResourceID:   &c.ID,
			Data: map[string]any{
				"before_status": string(LateSuspense),
				"status":        string(LateApplied),
				"amount":        c.Amount.StringFixed(2),
				"currency":      c.Currency,
				"user_id":       c.UserID.String(),
				"order_id":      c.OrderID.String(),
				"order_no":      c.OrderNo,
				"reason":        reason,
				"ledger_txn":    txnID.String(),
			},
		})
	})
	if err != nil {
		return uuid.Nil, err
	}
	return txnID, nil
}

// PurgeUnverifiedEvents 删一批早于 before、验签失败的回调事件（库函数只删 30 天以前的），返回删除行数；
// retention:payment_unverified_events 任务循环调用。
func (s *Service) PurgeUnverifiedEvents(ctx context.Context, before time.Time, batch int) (int, error) {
	return s.repo.PurgeUnverifiedEvents(ctx, before, batch)
}

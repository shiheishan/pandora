package service

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/commission/repo"
	ledgersvc "github.com/aegispanel/aegis/internal/module/ledger/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/google/uuid"
)

// ReviewEntry 复核一条待复核的佣金（后台）：approve 为 true 是通过，否则作废；返回条目复核后的状态。
//   - 通过：清 review_required、记复核人与复核时间，状态仍是 pending，之后由解冻任务照常处理（冻结期已过的下一轮就解冻）。原因可选，只进审计。
//   - 作废：同一事务里记 commission_voided 冲回计提（借 推荐人待结佣金 / 贷 平台收入 main，按来源唯一），条目转 voided。原因必填，至少 5 个字。
//
// 只对 pending 且 review_required 的条目有效：行锁后检查一次，更新时 WHERE 再把关一次；不满足回 409，重放与并发的第二次也只会得到 409。
func (s *Service) ReviewEntry(ctx context.Context, id uuid.UUID, approve bool, reason string) (string, error) {
	reason, err := validateEntryReview(approve, reason)
	if err != nil {
		return "", err
	}
	adminID, err := principalID(ctx)
	if err != nil {
		return "", err
	}
	status := EntryPending
	if !approve {
		status = EntryVoided
	}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		e, ok, err := s.repo.LockEntry(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return ErrEntryNotFound
		}
		if e.Status != EntryPending || !e.ReviewRequired {
			return ErrEntryNotReviewable
		}
		now := s.clock()
		data := map[string]any{
			"reason":        reason,
			"review_reason": deref(e.ReviewReason),
			"amount":        e.CommissionAmount.StringFixed(2),
			"currency":      e.Currency,
			"order_no":      e.OrderNo,
		}
		if approve {
			changed, err := s.repo.ApproveEntry(ctx, id, adminID, now)
			if err != nil {
				return err
			}
			if !changed {
				return ErrEntryNotReviewable
			}
			return s.audit.Write(ctx, auditsvc.Event{
				Action:       ActionEntryApproved,
				ResourceType: ResourceCommissionEntry,
				ResourceID:   &id,
				Data:         data,
			})
		}
		if !CanEntryTransition(e.Status, EntryVoided) {
			return ErrEntryNotReviewable
		}
		txnID, err := s.ledger.Post(ctx, ledgersvc.Posting{
			Kind:       ledgersvc.KindCommissionVoided,
			Currency:   money.Currency(e.Currency),
			SourceType: SourceCommissionEntry,
			SourceID:   e.ID,
			Memo:       "佣金复核作废，冲回计提",
			Legs: []ledgersvc.Leg{
				{Account: ledgersvc.UserAccount(ledgersvc.UserCommissionPending, e.ReferrerUserID), Direction: ledgersvc.Debit, Amount: e.CommissionAmount},
				{Account: ledgersvc.PlatformAccount(ledgersvc.PlatformRevenue, ledgersvc.RefMain), Direction: ledgersvc.Credit, Amount: e.CommissionAmount},
			},
		})
		if err != nil {
			if errors.Is(err, ledgersvc.ErrInsufficientBalance) {
				return ErrVoidUnderfunded
			}
			if errors.Is(err, ledgersvc.ErrDuplicatePosting) {
				return ErrEntryNotReviewable
			}
			return err
		}
		changed, err := s.repo.VoidEntry(ctx, id, txnID, reason, adminID, now)
		if err != nil {
			return err
		}
		if !changed {
			return ErrEntryNotReviewable
		}
		data["ledger_txn"] = txnID.String()
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionEntryVoided,
			ResourceType: ResourceCommissionEntry,
			ResourceID:   &id,
			Data:         data,
		})
	})
	if err != nil {
		return "", err
	}
	return status, nil
}

// validateEntryReview 校验复核原因，返回去掉首尾空白的原因：作废必填且不少于 5 个字，通过可空；都不超过 500 个字、不含 NUL。
func validateEntryReview(approve bool, reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	n := utf8.RuneCountInString(reason)
	switch {
	case strings.ContainsRune(reason, 0):
		return "", errs.Invalid(map[string]string{"reason": "原因含有非法字符"})
	case !approve && n < MinVoidReasonRunes:
		// 作废会冲回推荐人的佣金，推荐人会来问：写清依据
		return "", errs.Invalid(map[string]string{"reason": "作废必须填写原因，至少 5 个字"})
	case n > MaxEntryReviewRunes:
		return "", errs.Invalid(map[string]string{"reason": "原因最多 500 个字"})
	}
	return reason, nil
}

// AdminReviewEntries 列出待复核的佣金（后台），按计提时间倒序键集翻页；cursor 是上一页给出的 NextCursor，空串从头取。
// limit 为 0 取默认 50，大于 100 按 100。推荐人与被推荐人邮箱一次批量取回。
func (s *Service) AdminReviewEntries(ctx context.Context, cursor string, limit int) (ReviewEntryPage, error) {
	switch {
	case limit < 0:
		return ReviewEntryPage{}, errs.Invalid(map[string]string{"limit": "格式不正确"})
	case limit == 0:
		limit = ReviewEntriesDefault
	case limit > ReviewEntriesMax:
		limit = ReviewEntriesMax
	}
	after, err := decodeEntryCursor(cursor)
	if err != nil {
		return ReviewEntryPage{}, err
	}
	refs, err := s.needRefs()
	if err != nil {
		return ReviewEntryPage{}, err
	}
	// 多取一条判断有没有下一页
	rows, err := s.repo.ListReviewEntries(ctx, after, limit+1)
	if err != nil {
		return ReviewEntryPage{}, err
	}
	out := ReviewEntryPage{Entries: make([]ReviewEntryRow, 0, min(len(rows), limit))}
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1]
		out.NextCursor = encodeEntryCursor(repo.EntryCursor{CreatedAt: last.CreatedAt, ID: last.ID})
	}
	users := make([]uuid.UUID, 0, 2*len(rows))
	seen := make(map[uuid.UUID]bool, 2*len(rows))
	for _, e := range rows {
		for _, u := range []uuid.UUID{e.ReferrerUserID, e.RefereeUserID} {
			if !seen[u] {
				seen[u] = true
				users = append(users, u)
			}
		}
	}
	emails, err := refs.Emails(ctx, users)
	if err != nil {
		return ReviewEntryPage{}, err
	}
	for _, e := range rows {
		out.Entries = append(out.Entries, ReviewEntryRow{
			ID:            e.ID,
			OrderNo:       e.OrderNo,
			ReferrerEmail: emails[e.ReferrerUserID],
			RefereeEmail:  emails[e.RefereeUserID],
			Amount:        e.CommissionAmount,
			Currency:      e.Currency,
			ReviewReason:  deref(e.ReviewReason),
			CreatedAt:     e.CreatedAt,
			FrozenUntil:   e.FrozenUntil,
		})
	}
	return out, nil
}

// errBadCursor 是翻页位置解析不了（每次新建，不共享可变的 Fields）。
func errBadCursor() error {
	return errs.Invalid(map[string]string{"cursor": "翻页位置无效，请从第一页重新加载"})
}

// encodeEntryCursor 把翻页位置编码成不透明串：base64url("<RFC3339Nano 时间>|<id>")。
func encodeEntryCursor(c repo.EntryCursor) string {
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeEntryCursor 解析 encodeEntryCursor 的结果；空串表示从头取（返回 nil）。
func decodeEntryCursor(s string) (*repo.EntryCursor, error) {
	if s == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, errBadCursor()
	}
	ts, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return nil, errBadCursor()
	}
	at, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return nil, errBadCursor()
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil, errBadCursor()
	}
	return &repo.EntryCursor{CreatedAt: at, ID: id}, nil
}

// deref 取可空字符串的值，nil 为空串。
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

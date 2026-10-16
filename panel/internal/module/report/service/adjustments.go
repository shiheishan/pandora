package service

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.uber.org/zap"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/report/model"
	"github.com/aegispanel/aegis/internal/module/report/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/money"
)

// 审计口径。
const (
	ActionAdjustmentCreate  = "revenue.adjustment.create"
	ActionAdjustmentReverse = "revenue.adjustment.reverse"
	ResourceAdjustment      = "revenue_report_adjustment"
	// AdjustmentListLimit 是调整列表一次最多给的行数
	AdjustmentListLimit = 200
)

// adjustmentMaxAbs 是单笔调整的绝对值上限（元），与库里 CHECK 一致。
var adjustmentMaxAbs = decimal.New(10000000000, 0)

// ListAdjustments 列出最近的报表调整（含冲正行），按登记时间倒序。
func (s *Service) ListAdjustments(ctx context.Context) ([]Adjustment, error) {
	rows, err := s.repo.ListAdjustments(ctx, AdjustmentListLimit)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0)
	seen := map[uuid.UUID]struct{}{}
	for _, r := range rows {
		if _, ok := seen[r.CreatedBy]; ok {
			continue
		}
		seen[r.CreatedBy] = struct{}{}
		ids = append(ids, r.CreatedBy)
	}
	emails := map[uuid.UUID]string{}
	if len(ids) > 0 {
		emails, err = s.deps.Users.Emails(ctx, ids)
		if err != nil {
			return nil, err
		}
	}
	out := make([]Adjustment, 0, len(rows))
	for _, r := range rows {
		var email *string
		if e, ok := emails[r.CreatedBy]; ok {
			cp := e
			email = &cp
		}
		out = append(out, adjustmentOut(model.Adjustment{
			ID:          r.ID,
			Amount:      r.Amount,
			Reason:      r.Reason,
			EffectiveOn: r.EffectiveOn,
			ReversalOf:  r.ReversalOf,
			CreatedBy:   r.CreatedBy,
			CreatedAt:   r.CreatedAt,
		}, email, r.Reversed))
	}
	return out, nil
}

// CreateAdjustment 登记一条报表调整；登记人是当前后台主体。只影响报表展示值，不动账本。
func (s *Service) CreateAdjustment(ctx context.Context, in CreateAdjustmentInput) (Adjustment, error) {
	p, ok := authn.From(ctx)
	if !ok {
		return Adjustment{}, errs.New(errs.Unauthorized)
	}
	loc, err := s.location(ctx)
	if err != nil {
		return Adjustment{}, err
	}
	today := dayString(midnight(s.now(), loc))

	fields := map[string]string{}
	if in.Amount.IsZero() || in.Amount.Abs().GreaterThan(adjustmentMaxAbs) || money.Check(in.Amount) != nil {
		fields["amount"] = "调整金额必须非零且绝对值不超过 100 亿"
	}
	reason, reasonOK := checkReason(in.Reason)
	if !reasonOK {
		fields["reason"] = "理由需为 5–500 个字符"
	}
	effectiveOn := strings.TrimSpace(in.EffectiveOn)
	if effectiveOn == "" {
		effectiveOn = today
	} else {
		t, err := time.ParseInLocation(time.DateOnly, effectiveOn, loc)
		if err != nil {
			fields["effective_on"] = "日期格式应为 YYYY-MM-DD"
		} else {
			effectiveOn = dayString(t)
			if effectiveOn > today {
				fields["effective_on"] = "生效日期不能晚于今天"
			}
		}
	}
	if len(fields) > 0 {
		return Adjustment{}, errs.Invalid(fields)
	}

	var created model.Adjustment
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		a := model.Adjustment{
			Amount:      in.Amount,
			Reason:      reason,
			EffectiveOn: effectiveOn,
			CreatedBy:   p.UserID,
		}
		if err := s.repo.InsertAdjustment(ctx, &a); err != nil {
			return err
		}
		created = a
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionAdjustmentCreate,
			ResourceType: ResourceAdjustment,
			ResourceID:   &a.ID,
			Data: map[string]any{
				"amount":       a.Amount.StringFixed(2),
				"effective_on": a.EffectiveOn,
				"reason":       a.Reason,
			},
		})
	})
	if err != nil {
		return Adjustment{}, err
	}

	var email *string
	emails, err := s.deps.Users.Emails(ctx, []uuid.UUID{p.UserID})
	if err != nil {
		plog.From(ctx).Warn("读取登记人邮箱失败", zap.Error(err))
	} else if e, ok := emails[p.UserID]; ok {
		cp := e
		email = &cp
	}
	return adjustmentOut(created, email, false), nil
}

// ReverseAdjustment 追加一条与原调整金额相反、生效日相同的冲正；原行不改。冲正行本身不能再冲，一条只能冲一次。
func (s *Service) ReverseAdjustment(ctx context.Context, id uuid.UUID, reason string) (Adjustment, error) {
	p, ok := authn.From(ctx)
	if !ok {
		return Adjustment{}, errs.New(errs.Unauthorized)
	}
	reason, reasonOK := checkReason(reason)
	if !reasonOK {
		return Adjustment{}, errs.Invalid(map[string]string{"reason": "撤销理由需为 5–500 个字符"})
	}

	var rev model.Adjustment
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockAdjustment(ctx, id); err != nil {
			return err
		}
		orig, found, err := s.repo.GetAdjustment(ctx, id)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		if orig.ReversalOf != nil {
			return errs.Newf(errs.Conflict, "反向记录不能再次撤销")
		}
		has, err := s.repo.HasReversal(ctx, id)
		if err != nil {
			return err
		}
		if has {
			return errs.Newf(errs.Conflict, "该调整已撤销")
		}
		a := model.Adjustment{
			Amount:      orig.Amount.Neg(),
			Reason:      reason,
			EffectiveOn: orig.EffectiveOn,
			ReversalOf:  &orig.ID,
			CreatedBy:   p.UserID,
		}
		if err := s.repo.InsertAdjustment(ctx, &a); err != nil {
			if errors.Is(err, repo.ErrDuplicate) {
				return errs.Newf(errs.Conflict, "该调整已撤销")
			}
			return err
		}
		rev = a
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionAdjustmentReverse,
			ResourceType: ResourceAdjustment,
			ResourceID:   &a.ID,
			Data: map[string]any{
				"original_id":     id.String(),
				"amount":          a.Amount.StringFixed(2),
				"original_reason": orig.Reason,
				"reason":          reason,
			},
		})
	})
	if err != nil {
		return Adjustment{}, err
	}

	var email *string
	emails, err := s.deps.Users.Emails(ctx, []uuid.UUID{p.UserID})
	if err != nil {
		plog.From(ctx).Warn("读取登记人邮箱失败", zap.Error(err))
	} else if e, ok := emails[p.UserID]; ok {
		cp := e
		email = &cp
	}
	return adjustmentOut(rev, email, false), nil
}

// checkReason 去掉首尾空白后校验原因：5–500 个字符、不含 NUL。
func checkReason(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "\x00") {
		return "", false
	}
	n := utf8.RuneCountInString(s)
	if n < 5 || n > 500 {
		return "", false
	}
	return s, true
}

// adjustmentOut 把表行转成对外的 Adjustment。
func adjustmentOut(a model.Adjustment, email *string, reversed bool) Adjustment {
	return Adjustment{
		ID:             a.ID,
		Amount:         a.Amount,
		Reason:         a.Reason,
		EffectiveOn:    a.EffectiveOn,
		ReversalOf:     a.ReversalOf,
		CreatedBy:      a.CreatedBy,
		CreatedByEmail: email,
		CreatedAt:      a.CreatedAt,
		Reversed:       reversed,
	}
}

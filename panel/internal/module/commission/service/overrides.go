package service

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/commission/model"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// 单独佣金设置的审计动作与上限。
const (
	ActionOverrideSet     = "commission.override_set"
	ActionOverrideRemoved = "commission.override_removed"
	ResourceUser          = "user"
	// OverridesLimit 是后台列表最多返回的条数。
	OverridesLimit = 200
	// overrideEmailLookup 是按邮箱找人时取的候选数（IDsByEmail 是包含匹配，再在里面找完全相同的邮箱）。
	overrideEmailLookup = 20
)

// ErrOverrideNotFound 是这个用户没有单独设置（删除时）。
var ErrOverrideNotFound = errs.Newf(errs.NotFound, "这个用户没有单独的佣金设置")

// UserOverride 是后台「单独佣金设置」列表的一行；RatePercent / Scope 为 nil 表示这一项跟全局走。
type UserOverride struct {
	UserID      uuid.UUID
	Email       string
	RatePercent *int
	Scope       *string
	UpdatedAt   time.Time
}

// OverrideInput 是后台设置某人单独佣金的输入：按邮箱找人；两项至少给一项。
type OverrideInput struct {
	Email       string
	RatePercent *int
	Scope       *string
}

// validateOverrideInput 校验后台输入，返回去首尾空白的邮箱与字段错误（无错为 nil）。
func validateOverrideInput(in OverrideInput) (string, map[string]string) {
	email := strings.TrimSpace(in.Email)
	fields := map[string]string{}
	if email == "" {
		fields["email"] = "请填写用户邮箱"
	} else if utf8.RuneCountInString(email) > 254 {
		fields["email"] = "邮箱太长"
	}
	if in.RatePercent != nil {
		r := *in.RatePercent
		if r < 0 || r > settingssvc.CommissionMaxRatePercent {
			fields["rate_percent"] = "佣金比例需在 0 到 50 之间"
		}
	}
	if in.Scope != nil && !settingssvc.ValidCommissionScope(*in.Scope) {
		fields["scope"] = "计佣范围只能是 first_order 或 every_order"
	}
	if in.RatePercent == nil && in.Scope == nil {
		fields["rate_percent"] = "比例与计佣范围至少设一项；都跟全局走就删除这条设置"
	}
	if len(fields) > 0 {
		return email, fields
	}
	return email, nil
}

// findUserByEmail 按邮箱精确找人（不区分大小写）：IDsByEmail 是包含匹配，取回候选后比对完整邮箱。
func (s *Service) findUserByEmail(ctx context.Context, refs Referrals, email string) (uuid.UUID, string, bool, error) {
	ids, err := refs.IDsByEmail(ctx, email, overrideEmailLookup)
	if err != nil {
		return uuid.Nil, "", false, err
	}
	if len(ids) == 0 {
		return uuid.Nil, "", false, nil
	}
	emails, err := refs.Emails(ctx, ids)
	if err != nil {
		return uuid.Nil, "", false, err
	}
	for _, id := range ids {
		stored, ok := emails[id]
		if !ok {
			continue
		}
		if strings.EqualFold(stored, email) {
			return id, stored, true, nil
		}
	}
	return uuid.Nil, "", false, nil
}

// ListOverrides 列出后台单独佣金设置（按更新时间倒序，最多 OverridesLimit 条）。
func (s *Service) ListOverrides(ctx context.Context) ([]UserOverride, error) {
	refs, err := s.needRefs()
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListOverrides(ctx, OverridesLimit)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(rows))
	for _, o := range rows {
		ids = append(ids, o.UserID)
	}
	emails, err := refs.Emails(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]UserOverride, 0, len(rows))
	for _, o := range rows {
		out = append(out, UserOverride{
			UserID:      o.UserID,
			Email:       emails[o.UserID],
			RatePercent: o.RatePercent,
			Scope:       o.Scope,
			UpdatedAt:   o.UpdatedAt,
		})
	}
	return out, nil
}

// SetOverride 按邮箱为用户写入或更新单独佣金设置。
// 只影响该用户作为直接邀请人的以后订单；已计提的佣金不变。
func (s *Service) SetOverride(ctx context.Context, in OverrideInput) (UserOverride, error) {
	email, fieldErrs := validateOverrideInput(in)
	if fieldErrs != nil {
		return UserOverride{}, errs.Invalid(fieldErrs)
	}
	refs, err := s.needRefs()
	if err != nil {
		return UserOverride{}, err
	}
	adminID, err := principalID(ctx)
	if err != nil {
		return UserOverride{}, err
	}
	userID, storedEmail, ok, err := s.findUserByEmail(ctx, refs, email)
	if err != nil {
		return UserOverride{}, err
	}
	if !ok {
		return UserOverride{}, errs.Invalid(map[string]string{"email": "找不到这个邮箱的用户"})
	}
	now := s.clock()
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		old, had, err := s.repo.LockOverride(ctx, userID)
		if err != nil {
			return err
		}
		o := &model.Override{
			UserID:      userID,
			RatePercent: in.RatePercent,
			Scope:       in.Scope,
			UpdatedBy:   &adminID,
			UpdatedAt:   now,
		}
		if err := s.repo.UpsertOverride(ctx, o); err != nil {
			return err
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionOverrideSet,
			ResourceType: ResourceUser,
			ResourceID:   &userID,
			Data: map[string]any{
				"before": overrideData(old, had),
				"after":  overrideData(*o, true),
			},
		})
	})
	if err != nil {
		return UserOverride{}, err
	}
	return UserOverride{
		UserID:      userID,
		Email:       storedEmail,
		RatePercent: in.RatePercent,
		Scope:       in.Scope,
		UpdatedAt:   now,
	}, nil
}

// DeleteOverride 删除某用户的单独佣金设置。
func (s *Service) DeleteOverride(ctx context.Context, userID uuid.UUID) error {
	if _, err := principalID(ctx); err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		old, had, err := s.repo.LockOverride(ctx, userID)
		if err != nil {
			return err
		}
		if !had {
			return ErrOverrideNotFound
		}
		deleted, err := s.repo.DeleteOverride(ctx, userID)
		if err != nil {
			return err
		}
		if !deleted {
			return ErrOverrideNotFound
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionOverrideRemoved,
			ResourceType: ResourceUser,
			ResourceID:   &userID,
			Data: map[string]any{
				"before": overrideData(old, true),
			},
		})
	})
}

// overrideData 是审计里的设置快照；ok 为 false（原来没有设置）时为 nil。
func overrideData(o model.Override, ok bool) map[string]any {
	if !ok {
		return nil
	}
	var rate any
	if o.RatePercent != nil {
		rate = *o.RatePercent
	}
	var scope any
	if o.Scope != nil {
		scope = *o.Scope
	}
	return map[string]any{
		"rate_percent": rate,
		"scope":        scope,
	}
}

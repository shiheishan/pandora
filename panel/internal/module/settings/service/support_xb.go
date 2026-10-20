package service

import (
	"context"
	"encoding/json"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
)

const (
	keySupportWaitForReply   = "support.wait_for_reply"     // bool，缺省 false：用户须等客服回复后才能再发
	keySupportAutoClose      = "support.auto_close_enabled" // bool，缺省 false：自动关闭「等待用户」太久的工单
	keySupportAutoCloseHours = "support.auto_close_hours"   // int，缺省 24（照 Xboard check:ticket），1–720
	keySupportMaxOpenTickets = "support.max_open_tickets"   // int，缺省 5，1–50：每人同时打开（未解决、未关闭）的工单上限

	auditSupportChanged = "settings.support_updated"
	// DefaultSupportAutoCloseHours 是自动关闭的缺省时长（小时）。
	DefaultSupportAutoCloseHours = 24
	// SupportAutoCloseMaxHours 是自动关闭时长上限（30 天）。
	SupportAutoCloseMaxHours = 720
	// DefaultSupportMaxOpenTickets 是每人同时打开的工单上限缺省值。
	DefaultSupportMaxOpenTickets = 5
	// SupportMaxOpenTicketsLimit 是同时打开工单上限的可选范围上界。
	SupportMaxOpenTicketsLimit = 50
)

var supportKeys = []string{keySupportWaitForReply, keySupportAutoClose, keySupportAutoCloseHours, keySupportMaxOpenTickets}

// SupportRules 是工单规则（Xboard 补齐），缺省都关。
type SupportRules struct {
	WaitForReply   bool
	AutoClose      bool
	AutoCloseHours int
	MaxOpenTickets int // 每人同时打开的工单上限（1–50，缺省 5）
}

// SupportRules 读取工单规则。
func (s *Service) SupportRules(ctx context.Context) (SupportRules, error) {
	vals, err := s.store.Values(ctx, supportKeys)
	if err != nil {
		return SupportRules{}, err
	}
	return decodeSupportRules(ctx, vals), nil
}

// ValidateSupportRules 校验：AutoCloseHours 在 1–SupportAutoCloseMaxHours（不管 AutoClose 开没开都校验）。
func ValidateSupportRules(v SupportRules) error {
	fields := map[string]string{}
	if v.AutoCloseHours < 1 || v.AutoCloseHours > SupportAutoCloseMaxHours {
		fields["auto_close_hours"] = "自动关闭时长要在 1 到 720 小时之间"
	}
	if v.MaxOpenTickets < 1 || v.MaxOpenTickets > SupportMaxOpenTicketsLimit {
		fields["max_open_tickets"] = "同时打开的工单上限要在 1 到 50 之间"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

// SetSupportRules 在锁住本组各行的事务里整份写入工单规则；有改动才写 settings.support_updated 审计。
func (s *Service) SetSupportRules(ctx context.Context, v SupportRules) error {
	if err := ValidateSupportRules(v); err != nil {
		return err
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, supportKeys)
		if err != nil {
			return err
		}
		old := decodeSupportRules(ctx, vals)
		changes, b, a := diff(supportMap(old), supportMap(v))
		if len(changes) == 0 {
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		return s.writeAudit(ctx, auditSupportChanged, auditResourceSettings, map[string]any{
			"group":  "support",
			"before": b,
			"after":  a,
		})
	})
}

func decodeSupportRules(ctx context.Context, vals map[string]json.RawMessage) SupportRules {
	wait := decodeSupportItem(ctx, vals, keySupportWaitForReply, false, func(b bool) bool { return true })
	auto := decodeSupportItem(ctx, vals, keySupportAutoClose, false, func(b bool) bool { return true })
	hours := decodeSupportItem(ctx, vals, keySupportAutoCloseHours, DefaultSupportAutoCloseHours, func(n int) bool {
		return n >= 1 && n <= SupportAutoCloseMaxHours
	})
	maxOpen := decodeSupportItem(ctx, vals, keySupportMaxOpenTickets, DefaultSupportMaxOpenTickets, func(n int) bool {
		return n >= 1 && n <= SupportMaxOpenTicketsLimit
	})
	return SupportRules{WaitForReply: wait, AutoClose: auto, AutoCloseHours: hours, MaxOpenTickets: maxOpen}
}

func decodeSupportItem[T any](ctx context.Context, vals map[string]json.RawMessage, key string, def T, ok func(T) bool) T {
	raw, exists := vals[key]
	if !exists || len(raw) == 0 {
		return def
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		plog.From(ctx).Warn("工单规则设置的值非法，按缺省值", zap.String("key", key))
		return def
	}
	if !ok(v) {
		plog.From(ctx).Warn("工单规则设置的值非法，按缺省值", zap.String("key", key))
		return def
	}
	return v
}

func supportMap(v SupportRules) map[string]any {
	return map[string]any{
		keySupportWaitForReply:   v.WaitForReply,
		keySupportAutoClose:      v.AutoClose,
		keySupportAutoCloseHours: v.AutoCloseHours,
		keySupportMaxOpenTickets: v.MaxOpenTickets,
	}
}

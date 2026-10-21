package service

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// 站内信与去重键保留期（任务模块引用）。
const (
	InboxRetentionDays = 90
	EventRetentionDays = 400
)

// preferenceCatalog 是固定 5 项通知偏好目录（顺序固定）。营销类只有后台群发邮件（registry 的 admin.broadcast），没有 Telegram 一格；库里有 CHECK notification_preferences_marketing_email_only 兜底（迁移 00064）。
var preferenceCatalog = []Preference{
	{Category: "transactional", Channel: "email", Enabled: true, Locked: true},
	{Category: "transactional", Channel: "telegram", Enabled: true, Locked: true},
	{Category: "service", Channel: "email", Enabled: true},
	{Category: "service", Channel: "telegram", Enabled: true},
	{Category: "marketing", Channel: "email", Enabled: true},
}

func validPreference(category, channel string) bool {
	for _, p := range preferenceCatalog {
		if p.Category == category && p.Channel == channel {
			return true
		}
	}
	return false
}

// Inbox 返回本人最近的站内信（onlyUnread 只看未读，limit 由 handler 夹在 1–100）与全部未读数。
func (s *Service) Inbox(ctx context.Context, userID uuid.UUID, onlyUnread bool, limit int) ([]InboxEntry, int64, error) {
	rows, err := s.store.ListInbox(ctx, userID, onlyUnread, limit)
	if err != nil {
		return nil, 0, err
	}
	unread, err := s.store.CountUnread(ctx, userID)
	if err != nil {
		return nil, 0, err
	}
	out := make([]InboxEntry, 0, len(rows))
	for _, r := range rows {
		out = append(out, InboxEntry{
			ID:      r.ID,
			Code:    r.TemplateCode,
			Subject: r.Subject,
			Body:    r.Body,
			SentAt:  r.CreatedAt,
			ReadAt:  r.ReadAt,
		})
	}
	return out, unread, nil
}

// MarkInboxRead 把本人的一条站内信标为已读；不存在、不属于本人或已读都不算错。
func (s *Service) MarkInboxRead(ctx context.Context, userID, id uuid.UUID) error {
	return s.store.MarkInboxRead(ctx, userID, id)
}

// MarkAllInboxRead 把本人全部未读站内信标为已读。
func (s *Service) MarkAllInboxRead(ctx context.Context, userID uuid.UUID) error {
	_, err := s.store.MarkAllInboxRead(ctx, userID)
	return err
}

// Preferences 返回本人的有效通知偏好：固定 5 项，没改过的取缺省，交易类恒开且 Locked。
func (s *Service) Preferences(ctx context.Context, userID uuid.UUID) ([]Preference, error) {
	stored, err := s.store.Preferences(ctx, userID)
	if err != nil {
		return nil, err
	}
	over := map[string]bool{}
	for _, p := range stored {
		over[p.Category+"/"+p.Channel] = p.Enabled
	}
	out := make([]Preference, len(preferenceCatalog))
	for i, item := range preferenceCatalog {
		p := item
		if !p.Locked {
			if v, ok := over[p.Category+"/"+p.Channel]; ok {
				p.Enabled = v
			}
		}
		out[i] = p
	}
	return out, nil
}

// SetPreference 改一项偏好。类别与渠道不在目录里 → 422 {"preference": "不支持的通知类别或渠道"}；交易类关闭 → errs.Newf(errs.ValidationFailed, "交易类通知无法关闭")。
func (s *Service) SetPreference(ctx context.Context, userID uuid.UUID, category, channel string, enabled bool) error {
	if !validPreference(category, channel) {
		return errs.Invalid(map[string]string{"preference": "不支持的通知类别或渠道"})
	}
	if category == categoryTransactional && !enabled {
		return errs.Newf(errs.ValidationFailed, "交易类通知无法关闭")
	}
	return s.store.UpsertPreference(ctx, &model.Preference{
		UserID:   userID,
		Category: category,
		Channel:  channel,
		Enabled:  enabled,
	})
}

// PurgeInbox 删除早于 before 的站内信。
func (s *Service) PurgeInbox(ctx context.Context, before time.Time, batch int) (int, error) {
	return s.store.PurgeInbox(ctx, before, batch)
}

// PurgeEvents 删除早于 before 的去重键。
func (s *Service) PurgeEvents(ctx context.Context, before time.Time, batch int) (int, error) {
	return s.store.PurgeEvents(ctx, before, batch)
}

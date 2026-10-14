package service

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/appearance/model"
	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// ListSlots 按目录顺序返回全部插槽位（库里没有的为零值）。
func (s *Service) ListSlots(ctx context.Context) ([]Slot, error) {
	rows, err := s.repo.ListSlots(ctx)
	if err != nil {
		return nil, err
	}
	saved := make(map[string]model.Slot, len(rows))
	for _, r := range rows {
		saved[r.Key] = r
	}
	loc := s.location(ctx)
	out := make([]Slot, 0, len(SlotCatalog))
	for _, c := range SlotCatalog {
		x := Slot{Key: c.Key, Label: c.Label, Where: c.Where}
		if r, ok := saved[c.Key]; ok {
			x.Content = r.Content
			x.Enabled = r.Enabled
			if !r.UpdatedAt.IsZero() {
				x.UpdatedAt = r.UpdatedAt.In(loc).Format(slotTimeLayout)
			}
		}
		out = append(out, x)
	}
	return out, nil
}

// SaveSlot 保存一个插槽，返回净化时丢掉的内容说明。
func (s *Service) SaveSlot(ctx context.Context, in SaveSlotInput) ([]string, error) {
	known := false
	for _, c := range SlotCatalog {
		if c.Key == in.Key {
			known = true
			break
		}
	}
	if !known {
		return nil, errs.Invalid(map[string]string{"key": "未知的插槽位"})
	}
	if strings.Contains(in.Content, "\x00") {
		return nil, errs.Invalid(map[string]string{"content": "内容不能包含空字符"})
	}
	clean, notes := SanitizeHTML(in.Content)
	if len(clean) > MaxSlotBytes {
		return nil, errs.Invalid(map[string]string{"content": "内容超过 32KB，请精简"})
	}

	var updatedBy *uuid.UUID
	if p, ok := authn.From(ctx); ok {
		updatedBy = &p.UserID
	}

	err := s.tx.Do(ctx, func(ctx context.Context) error {
		sl := model.Slot{
			Key:       in.Key,
			Content:   clean,
			Enabled:   in.Enabled,
			UpdatedBy: updatedBy,
		}
		if err := s.repo.UpsertSlot(ctx, &sl); err != nil {
			return err
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionSlotSave,
			ResourceType: "site_slot",
			Data: map[string]any{
				"key":     in.Key,
				"enabled": in.Enabled,
				"bytes":   len(clean),
				"dropped": len(notes),
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return notes, nil
}

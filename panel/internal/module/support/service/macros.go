package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/support/model"
)

// ListMacros 列出全部快捷回复。
func (s *Service) ListMacros(ctx context.Context) ([]Macro, error) {
	_, err := adminUser(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListMacros(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Macro, 0, len(rows))
	for _, m := range rows {
		out = append(out, Macro{
			ID:        m.ID,
			Title:     m.Title,
			Body:      m.Body,
			SortOrder: m.SortOrder,
			UpdatedAt: m.UpdatedAt,
		})
	}
	return out, nil
}

// SaveMacro 新建或覆盖快捷回复。
func (s *Service) SaveMacro(ctx context.Context, id *uuid.UUID, in MacroInput) (uuid.UUID, error) {
	actor, err := adminUser(ctx)
	if err != nil {
		return uuid.Nil, err
	}
	in, err = normalizeMacro(in)
	if err != nil {
		return uuid.Nil, err
	}
	created := id == nil
	var outID uuid.UUID
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if id == nil {
			m := &model.Macro{
				Title:     in.Title,
				Body:      in.Body,
				SortOrder: in.SortOrder,
				CreatedBy: &actor,
			}
			if err := s.repo.InsertMacro(ctx, m); err != nil {
				return err
			}
			outID = m.ID
		} else {
			ok, err := s.repo.UpdateMacro(ctx, *id, in.Title, in.Body, in.SortOrder)
			if err != nil {
				return err
			}
			if !ok {
				return ErrMacroNotFound
			}
			outID = *id
		}
		return s.writeAudit(ctx, ActionMacroSaved, ResourceMacro, outID, map[string]any{
			"title":      in.Title,
			"sort_order": in.SortOrder,
			"created":    created,
		})
	})
	if err != nil {
		return uuid.Nil, err
	}
	return outID, nil
}

// DeleteMacro 删除快捷回复。
func (s *Service) DeleteMacro(ctx context.Context, id uuid.UUID) error {
	_, err := adminUser(ctx)
	if err != nil {
		return err
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		title, ok, err := s.repo.DeleteMacro(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return ErrMacroNotFound
		}
		return s.writeAudit(ctx, ActionMacroDeleted, ResourceMacro, id, map[string]any{
			"title": title,
		})
	})
}

package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/module/catalog/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// ListTrafficPacks 后台列表；status 只能是 ""、active、archived，否则 errs.Invalid。
func (s *Service) ListTrafficPacks(ctx context.Context, status string) ([]TrafficPack, error) {
	if status != "" && status != PackActive && status != PackArchived {
		return nil, errs.Invalid(map[string]string{"status": "只能是 active 或 archived"})
	}
	rows, err := s.repo.ListPacks(ctx, status)
	if err != nil {
		return nil, catalogErr(err)
	}
	out := make([]TrafficPack, 0, len(rows))
	for _, r := range rows {
		out = append(out, packFromModel(r))
	}
	return out, nil
}

// ActiveTrafficPacks 门户可买的流量包（在售，按 sort_order, created_at, id）。
func (s *Service) ActiveTrafficPacks(ctx context.Context) ([]TrafficPack, error) {
	rows, err := s.repo.ListPacks(ctx, PackActive)
	if err != nil {
		return nil, catalogErr(err)
	}
	out := make([]TrafficPack, 0, len(rows))
	for _, r := range rows {
		out = append(out, packFromModel(r))
	}
	return out, nil
}

func packInputModel(in PackInput, status string) model.TrafficPack {
	return model.TrafficPack{
		Name:         in.Name,
		TrafficBytes: in.TrafficBytes,
		Currency:     in.Currency,
		UnitAmount:   in.UnitAmount,
		Recommended:  in.Recommended,
		SortOrder:    in.SortOrder,
		Status:       status,
	}
}

// CreateTrafficPack 新建在售流量包。
func (s *Service) CreateTrafficPack(ctx context.Context, in PackInput) (TrafficPack, error) {
	if err := preparePackInput(&in); err != nil {
		return TrafficPack{}, catalogErr(err)
	}
	var out TrafficPack
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		m := packInputModel(in, PackActive)
		if err := s.repo.InsertPack(ctx, &m); err != nil {
			return err
		}
		s.catalogChanged(ctx, rtTablePacks, realtime.OpInsert, m.ID)
		row, found, err := s.repo.PackByID(ctx, m.ID, repo.NoLock)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		out = packFromModel(row)
		return s.writeAudit(ctx, ActionPackCreate, ResourceTrafficPack, out.ID,
			map[string]any{"after": packDigest(out)})
	})
	if err != nil {
		return TrafficPack{}, catalogErr(err)
	}
	return out, nil
}

// UpdateTrafficPack 改流量包资料。
func (s *Service) UpdateTrafficPack(ctx context.Context, packID uuid.UUID, expected *time.Time, in PackInput) (TrafficPack, error) {
	if expected == nil {
		return TrafficPack{}, errs.Invalid(map[string]string{"expected_updated_at": "必填：列表里读到的 updated_at"})
	}
	if err := preparePackInput(&in); err != nil {
		return TrafficPack{}, catalogErr(err)
	}
	var out TrafficPack
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		cur, found, err := s.repo.PackByID(ctx, packID, repo.ForUpdate)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		before := packFromModel(cur)
		if !before.UpdatedAt.Equal(*expected) {
			return packConflict(before.UpdatedAt)
		}
		m := packInputModel(in, before.Status)
		ok, err := s.repo.UpdatePack(ctx, packID, *expected, &m)
		if err != nil {
			return err
		}
		if !ok {
			row, found, err := s.repo.PackByID(ctx, packID, repo.NoLock)
			if err != nil {
				return err
			}
			if !found {
				return ErrNotFound
			}
			return packConflict(packFromModel(row).UpdatedAt)
		}
		s.catalogChanged(ctx, rtTablePacks, realtime.OpUpdate, packID)
		row, found, err := s.repo.PackByID(ctx, packID, repo.NoLock)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		out = packFromModel(row)
		return s.writeAudit(ctx, ActionPackUpdate, ResourceTrafficPack, packID,
			map[string]any{"before": packDigest(before), "after": packDigest(out)})
	})
	if err != nil {
		return TrafficPack{}, catalogErr(err)
	}
	return out, nil
}

// SetTrafficPackStatus 上架或下架。
func (s *Service) SetTrafficPackStatus(ctx context.Context, packID uuid.UUID, status string, expected *time.Time) (TrafficPack, error) {
	fields := map[string]string{}
	if status != PackActive && status != PackArchived {
		fields["status"] = "只能是 active 或 archived"
	}
	if expected == nil {
		fields["expected_updated_at"] = "必填：列表里读到的 updated_at"
	}
	if len(fields) > 0 {
		return TrafficPack{}, errs.Invalid(fields)
	}
	var out TrafficPack
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		cur, found, err := s.repo.PackByID(ctx, packID, repo.ForUpdate)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		before := packFromModel(cur)
		if !before.UpdatedAt.Equal(*expected) {
			return packConflict(before.UpdatedAt)
		}
		if before.Status == status {
			if status == PackActive {
				return errs.Newf(errs.Conflict, "流量包已经在售")
			}
			return errs.Newf(errs.Conflict, "流量包已经下架")
		}
		ok, err := s.repo.SetPackStatus(ctx, packID, *expected, before.Status, status)
		if err != nil {
			return err
		}
		if !ok {
			row, found, err := s.repo.PackByID(ctx, packID, repo.NoLock)
			if err != nil {
				return err
			}
			if !found {
				return ErrNotFound
			}
			return packConflict(packFromModel(row).UpdatedAt)
		}
		s.catalogChanged(ctx, rtTablePacks, realtime.OpUpdate, packID)
		row, found, err := s.repo.PackByID(ctx, packID, repo.NoLock)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		out = packFromModel(row)
		action := ActionPackArchive
		if status == PackActive {
			action = ActionPackRestore
		}
		return s.writeAudit(ctx, action, ResourceTrafficPack, packID,
			map[string]any{"before": map[string]any{"status": before.Status}, "after": map[string]any{"status": out.Status}})
	})
	if err != nil {
		return TrafficPack{}, catalogErr(err)
	}
	return out, nil
}

// packDigest 是审计摘要。
func packDigest(p TrafficPack) map[string]any {
	return map[string]any{
		"name":          p.Name,
		"traffic_bytes": p.TrafficBytes,
		"currency":      string(p.Currency),
		"unit_amount":   p.UnitAmount.StringFixed(2),
		"recommended":   p.Recommended,
		"status":        p.Status,
		"sort_order":    p.SortOrder,
	}
}

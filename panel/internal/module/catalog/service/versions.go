package service

// 版本承载用量（流量配额、设备数、限速）；改用量写进草稿（没有就新建，权益从当前版本复制），
// 发布即冻结并成为当前版本：新购用新版本，订阅老用户在下个周期切换（subscription 的周期滚动做），一次性老用户不受影响。

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/catalog/model"
	"github.com/aegispanel/aegis/internal/module/catalog/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// writeUsageTx 把用量写入草稿版本（权益与流量配额一并替换）。
func (s *Service) writeUsageTx(ctx context.Context, kind string, versionID uuid.UUID, usage UsageInput, ents []model.Entitlement) error {
	ok, err := s.repo.UpdateVersionUsage(ctx, versionID, usage.MaxDevices, usage.ThrottleKbps)
	if err != nil {
		return err
	}
	if !ok {
		return errs.Newf(errs.Conflict, "只有未发布的草稿版本可以修改")
	}
	if ents == nil {
		ents = []model.Entitlement{}
	}
	for i := range ents {
		ents[i].ID = uuid.Nil
	}
	if err := s.repo.ReplaceVersionChildren(ctx, versionID, ents, quotaModels(trafficQuotas(kind, usage.TrafficBytes))); err != nil {
		return err
	}
	s.catalogChanged(ctx, rtTableVersions, realtime.OpUpdate, versionID)
	return nil
}

// UpdateUsage 改用量：写入草稿版本（没有则新建），不发布。
func (s *Service) UpdateUsage(ctx context.Context, planID uuid.UUID, in UsageUpdate) (PlanDetail, error) {
	if err := checkExpected(in.ExpectedRowVersion); err != nil {
		return PlanDetail{}, err
	}
	fields := map[string]string{}
	validateUsage(in.Usage, fields)
	if err := invalid(fields); err != nil {
		return PlanDetail{}, err
	}

	err := s.tx.Do(ctx, func(ctx context.Context) error {
		cur, err := s.lockEditablePlan(ctx, planID, in.ExpectedRowVersion)
		if err != nil {
			return err
		}

		drafts, err := s.repo.DraftVersionIDs(ctx, []uuid.UUID{planID})
		if err != nil {
			return err
		}

		var vid uuid.UUID
		var ents []model.Entitlement

		if draftID, ok := drafts[planID]; ok {
			vid = draftID
			entMap, err := s.repo.Entitlements(ctx, []uuid.UUID{vid})
			if err != nil {
				return err
			}
			ents = entMap[vid]
		} else {
			var createdBy *uuid.UUID
			if p, ok := authn.From(ctx); ok && p.UserID != uuid.Nil {
				id := p.UserID
				createdBy = &id
			}
			v, err := s.repo.InsertNextVersion(ctx, planID, createdBy)
			if err != nil {
				if errors.Is(err, repo.ErrDuplicate) {
					return errs.Newf(errs.Conflict, "该套餐已有未发布的草稿版本")
				}
				return err
			}
			s.catalogChanged(ctx, rtTableVersions, realtime.OpInsert, v.ID)
			vid = v.ID
			if cur.CurrentVersionID != nil {
				entMap, err := s.repo.Entitlements(ctx, []uuid.UUID{*cur.CurrentVersionID})
				if err != nil {
					return err
				}
				ents = entMap[*cur.CurrentVersionID]
			}
		}

		if err := s.writeUsageTx(ctx, cur.Kind, vid, in.Usage, ents); err != nil {
			return err
		}

		next, err := s.bumpPlanRow(ctx, planID, in.ExpectedRowVersion)
		if err != nil {
			return err
		}
		return s.writeAudit(ctx, ActionPlanUsage, ResourcePlan, planID, map[string]any{
			"after": map[string]any{
				"row_version":   next,
				"version_id":    vid,
				"traffic_bytes": in.Usage.TrafficBytes,
				"max_devices":   in.Usage.MaxDevices,
				"throttle_kbps": in.Usage.ThrottleKbps,
			},
		})
	})
	if err != nil {
		return PlanDetail{}, catalogErr(err)
	}
	return s.GetPlan(ctx, planID)
}

// publishTx 发布草稿版本并设为套餐当前版本（新建即发布与 PublishVersion 共用）。
func (s *Service) publishTx(ctx context.Context, planID, versionID uuid.UUID, expected int64) error {
	plan, err := s.lockEditablePlan(ctx, planID, expected)
	if err != nil {
		return err
	}

	ver, found, err := s.repo.VersionByID(ctx, planID, versionID, repo.ForUpdate)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	if ver.Status != VersionDraft {
		return errs.Newf(errs.Conflict, "只有未发布的草稿版本可以发布")
	}

	prices, err := s.repo.ActivePrices(ctx, []uuid.UUID{planID}, repo.ForShare)
	if err != nil {
		return err
	}
	if len(prices) == 0 {
		return errs.Invalid(map[string]string{"prices": "发布前至少设一档在售价格"})
	}

	if s.gate != nil {
		if err := s.gate.CheckPublishable(ctx, planID); err != nil {
			return err
		}
	}

	ok, err := s.repo.PublishVersion(ctx, versionID)
	if err != nil {
		return err
	}
	if !ok {
		return errs.Newf(errs.Conflict, "只有未发布的草稿版本可以发布")
	}
	s.catalogChanged(ctx, rtTableVersions, realtime.OpUpdate, versionID)

	ok, err = s.repo.ActivateVersion(ctx, planID, versionID, expected)
	if err != nil {
		return err
	}
	if !ok {
		return rowConflict("套餐", plan.RowVersion)
	}
	s.catalogChanged(ctx, rtTablePlans, realtime.OpUpdate, planID)

	return s.writeAudit(ctx, ActionVersionPublish, ResourcePlanVersion, versionID, map[string]any{
		"before": map[string]any{
			"plan_row_version":   expected,
			"current_version_id": plan.CurrentVersionID,
			"plan_status":        plan.Status,
		},
		"after": map[string]any{
			"plan_id":          planID,
			"plan_row_version": expected + 1,
			"version":          ver.Version,
		},
	})
}

// PublishVersion 发布指定草稿版本。
func (s *Service) PublishVersion(ctx context.Context, planID, versionID uuid.UUID, expected int64) (PlanDetail, error) {
	if err := checkExpected(expected); err != nil {
		return PlanDetail{}, err
	}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		return s.publishTx(ctx, planID, versionID, expected)
	})
	if err != nil {
		return PlanDetail{}, catalogErr(err)
	}
	return s.GetPlan(ctx, planID)
}

package service

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// BulkBan 按批量运营的筛选封禁一批账号：命中人数必须与预览时一致（防止条件下数据变了）；一次最多 BulkBanMax 人；
// 至少要有一个筛选条件；自己、管理员、已封禁、已注销的跳过并计数；其余改成 banned、吊销会话、逐人写审计，末尾写一条汇总审计。
func (s *Service) BulkBan(ctx context.Context, f BulkFilter, expectedTotal int64, reason string) (BulkBanResult, error) {
	p, err := principal(ctx)
	if err != nil {
		return BulkBanResult{}, err
	}

	r, ok := cleanText(reason)
	if !ok || utf8.RuneCountInString(r) < 5 || utf8.RuneCountInString(r) > 500 {
		return BulkBanResult{}, errs.Invalid(map[string]string{"reason": "请写清原因，5 到 500 个字"})
	}
	// 不带任何条件就是全站：批量封禁必须至少选一个筛选条件，防止误点封掉所有人
	if len(bulkFilterDigest(f)) == 0 {
		return BulkBanResult{}, errs.Invalid(map[string]string{"filter": "批量封禁至少要选一个筛选条件"})
	}

	f, q, err := s.bulkQuery(ctx, f)
	if err != nil {
		return BulkBanResult{}, err
	}

	var res BulkBanResult
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		total, err := s.repo.CountBulk(ctx, q)
		if err != nil {
			return err
		}
		if total != expectedTotal {
			return errs.Newf(errs.Conflict, fmt.Sprintf("命中人数变了（预览时 %d 人，现在 %d 人），请重新预览", expectedTotal, total))
		}
		if total > BulkBanMax {
			return errs.Invalid(map[string]string{"expected_total": fmt.Sprintf("一次最多封禁 %d 人，请收窄条件", BulkBanMax)})
		}

		refs, err := s.repo.BulkUsers(ctx, q, BulkBanMax)
		if err != nil {
			return err
		}

		ids := make([]uuid.UUID, len(refs))
		for i, ref := range refs {
			ids[i] = ref.ID
		}
		sort.Slice(ids, func(i, j int) bool {
			return bytes.Compare(ids[i][:], ids[j][:]) < 0
		})

		res = BulkBanResult{Skipped: map[string]int{}}

		revoker, err := s.needRevoker()
		if err != nil {
			return err
		}

		for _, id := range ids {
			if id == p.UserID {
				res.Skipped[BanSkipSelf]++
				continue
			}
			u, locked, err := s.repo.LockUser(ctx, id)
			if err != nil {
				return err
			}
			if !locked {
				continue
			}
			staff, err := s.isStaff(ctx, id)
			if err != nil {
				return err
			}
			if staff {
				res.Skipped[BanSkipAdmin]++
				continue
			}
			if u.Status == StatusDeleted {
				res.Skipped[BanSkipDeleted]++
				continue
			}
			if u.Status == StatusBanned {
				res.Skipped[BanSkipAlreadyBanned]++
				continue
			}

			if err := s.repo.SetUserStatus(ctx, id, StatusBanned); err != nil {
				return err
			}
			s.notifyRoster(ctx, id)
			if err := revoker.RevokeUser(ctx, id, ""); err != nil {
				return err
			}
			if err := s.writeAudit(ctx, auditsvc.Event{
				Action:       ActionStatusChange,
				ResourceType: ResourceUser,
				ResourceID:   &id,
				Data: map[string]any{
					"before":           u.Status,
					"status":           StatusBanned,
					"reason":           r,
					"sessions_revoked": true,
					"source":           "bulk",
				},
			}); err != nil {
				return err
			}
			res.Banned++
		}

		return s.writeAudit(ctx, auditsvc.Event{
			Action:       ActionBulkBanned,
			ResourceType: ResourceUser,
			ResourceID:   nil,
			Data: map[string]any{
				"filter":  bulkFilterDigest(f),
				"reason":  r,
				"banned":  res.Banned,
				"skipped": res.Skipped,
				"matched": total,
			},
		})
	})
	if err != nil {
		return BulkBanResult{}, err
	}
	return res, nil
}

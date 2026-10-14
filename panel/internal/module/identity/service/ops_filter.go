package service

import (
	"context"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/module/identity/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// validateBulkFilter 校验并规整批量运营筛选条件。
func validateBulkFilter(f BulkFilter) (BulkFilter, SubFilter, error) {
	fields := map[string]string{}
	query, ok := cleanText(f.Query)
	if !ok {
		fields["query"] = "搜索词格式不正确"
	} else if utf8.RuneCountInString(query) > 200 {
		fields["query"] = "搜索词最多 200 字"
	}
	f.Query = query

	if f.Status != "" && f.Status != StatusActive && f.Status != StatusSuspended && f.Status != StatusBanned {
		fields["status"] = "不支持的用户状态"
	}
	if f.ExpiresWithinDays < 0 || f.ExpiresWithinDays > 365 {
		fields["expires_within_days"] = "到期天数只能是 1–365"
	}
	if f.SubState != "" && f.SubState != "active" && f.SubState != "expired" && f.SubState != "none" {
		fields["sub_state"] = "订阅状态只能是 active、expired 或 none"
	}
	if len(fields) > 0 {
		return f, SubFilter{}, errs.Invalid(fields)
	}

	sf := SubFilter{
		State:             f.SubState,
		HasLive:           f.HasActiveSub,
		PlanID:            f.PlanID,
		ExpiresWithinDays: f.ExpiresWithinDays,
	}
	return f, sf, nil
}

// subIDs 按订阅条件取用户 id 集合与排除标志。
func (s *Service) subIDs(ctx context.Context, sf SubFilter) (filter bool, ids []uuid.UUID, exclude bool, err error) {
	if sf.Empty() {
		return false, nil, false, nil
	}
	op, err := s.needSubs()
	if err != nil {
		return false, nil, false, err
	}
	ids, exclude, err = op.UserIDsBySubFilter(ctx, sf)
	if err != nil {
		return false, nil, false, err
	}
	return true, ids, exclude, nil
}

// bulkQuery 校验 BulkFilter 并拼成 repo 查询条件。
func (s *Service) bulkQuery(ctx context.Context, f BulkFilter) (BulkFilter, repo.BulkQuery, error) {
	f, sf, err := validateBulkFilter(f)
	if err != nil {
		return f, repo.BulkQuery{}, err
	}
	subFilter, subIDs, subExclude, err := s.subIDs(ctx, sf)
	if err != nil {
		return f, repo.BulkQuery{}, err
	}
	q := repo.BulkQuery{
		Status:     f.Status,
		GroupID:    f.GroupID,
		SubFilter:  subFilter,
		SubIDs:     subIDs,
		SubExclude: subExclude,
	}
	if f.Query != "" {
		q.Pattern = repo.LikePattern(f.Query)
	}
	return f, q, nil
}

func bulkFilterDigest(f BulkFilter) map[string]any {
	d := map[string]any{}
	if f.Status != "" {
		d["status"] = f.Status
	}
	if f.GroupID != nil {
		d["group_id"] = f.GroupID.String()
	}
	if f.Query != "" {
		d["query"] = f.Query
	}
	if f.SubState != "" {
		d["sub_state"] = f.SubState
	}
	if f.HasActiveSub != nil {
		d["has_active_sub"] = *f.HasActiveSub
	}
	if f.PlanID != nil {
		d["plan_id"] = f.PlanID.String()
	}
	if f.ExpiresWithinDays > 0 {
		d["expires_within_days"] = f.ExpiresWithinDays
	}
	return d
}

// BulkFilterDigest 是审计用的圈人条件摘要（供 notify 群发审计）。
func BulkFilterDigest(f BulkFilter) map[string]any {
	return bulkFilterDigest(f)
}

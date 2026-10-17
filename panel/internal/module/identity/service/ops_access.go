package service

import (
	"context"
	"sort"
	"strings"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// accessCategoryRules 是审计动作到展示分类的唯一映射；按顺序匹配前缀，先命中的分类生效，所以顺序有意义。
var accessCategoryRules = []struct {
	category string
	prefixes []string
}{
	{"login", []string{"user.login"}},
	{"register", []string{"user.registered"}},
	{"reset_password", []string{"user.password", "user.reset"}},
	{"order", []string{"order."}},
	{"admin", []string{"node.", "route_group.", "admin.", "server.", "plan.", "payment_provider."}},
	{"payment", []string{"payment"}},
	{"ticket", []string{"ticket."}},
}

// categoryFromAction 把审计 action 归到展示分类。
func categoryFromAction(action string) string {
	for _, rule := range accessCategoryRules {
		for _, p := range rule.prefixes {
			if strings.HasPrefix(action, p) {
				return rule.category
			}
		}
	}
	return "other"
}

// auditCategoryFilter 把分类翻成审计查询用的 include / exclude 前缀。
func auditCategoryFilter(category string) (include, exclude []string, ok bool) {
	if category == "" {
		return nil, nil, true
	}
	var earlier []string
	for _, rule := range accessCategoryRules {
		if rule.category == category {
			return rule.prefixes, earlier, true
		}
		earlier = append(earlier, rule.prefixes...)
	}
	if category == "other" {
		return nil, earlier, true
	}
	return nil, nil, false
}

// clusterRisk 给同源 IP 聚类定风险等级（机房出口或账号数偏多判高）。
func clusterRisk(accounts int, kind string) string {
	switch {
	case accounts >= 5 || kind == "datacenter":
		return "high"
	case accounts >= 3:
		return "mid"
	default:
		return "low"
	}
}

// accessMaxOffset 是访问明细最多往后翻的条数（加上每页上限 200 不超过 audit 单次最多取的 10000 行）。
const accessMaxOffset = 9800

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func accessTextOK(field, s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.ContainsRune(s, 0) || len(s) > 200 {
		return errs.Invalid(map[string]string{field: "格式不正确或过长"})
	}
	return nil
}

// AccessLog 返回审计与订阅拉取归并后的访问明细（按时间倒序分页）。
func (s *Service) AccessLog(ctx context.Context, q AccessLogQuery) ([]AccessItem, error) {
	limit := q.Limit
	if limit < 1 || limit > 200 {
		limit = 50
	}
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	// 两路各取 limit+offset 条再归并切页，offset 不设上限会让一次请求扫无界的行
	if offset > accessMaxOffset {
		return nil, errs.Invalid(map[string]string{"offset": "最多往后翻 9800 条，请加筛选条件缩小范围"})
	}
	category := strings.ToLower(strings.TrimSpace(q.Category))
	outcome := strings.ToLower(strings.TrimSpace(q.Outcome))

	include, exclude, known := auditCategoryFilter(category)
	if !known && category != "subscribe" {
		return nil, errs.Invalid(map[string]string{"category": "不认识的分类"})
	}
	switch outcome {
	case "", "success", "failure", "denied", "partial", "error":
	default:
		return nil, errs.Invalid(map[string]string{"outcome": "只支持 success、failure、denied、partial 或 error"})
	}
	ip := strings.TrimSpace(q.IP)
	if err := accessTextOK("ip", ip); err != nil {
		return nil, err
	}
	user := strings.TrimSpace(q.User)
	if err := accessTextOK("user", user); err != nil {
		return nil, err
	}

	var filterActors bool
	var actorIDs []uuid.UUID
	if user != "" {
		if id, err := uuid.Parse(user); err == nil {
			filterActors = true
			actorIDs = []uuid.UUID{id}
		} else {
			ids, err := s.repo.IDsByEmailLike(ctx, user, 1000)
			if err != nil {
				return nil, err
			}
			filterActors = true
			actorIDs = ids
		}
	}

	fetchLimit := limit + offset
	var err error
	var audits []auditsvc.AccessEvent
	var fetches []FetchRecord

	if category != "subscribe" {
		var inc, exc []string
		if category != "" {
			inc, exc = include, exclude
		}
		audits, err = s.ops.AccessEvents(ctx, auditsvc.AccessQuery{
			Include:      nonNilStrings(inc),
			Exclude:      nonNilStrings(exc),
			IP:           ip,
			FilterActors: filterActors,
			ActorIDs:     actorIDs,
			Outcome:      outcome,
			Limit:        fetchLimit,
		})
		if err != nil {
			return nil, err
		}
	}

	includeFetches := outcome == "" || outcome == "success" || outcome == "error"
	if (category == "" || category == "subscribe") && includeFetches {
		subs, err := s.needSubs()
		if err != nil {
			return nil, err
		}
		fetches, err = subs.Fetches(ctx, FetchQuery{
			IP:          ip,
			FilterUsers: filterActors,
			UserIDs:     actorIDs,
			Outcome:     outcome,
			Limit:       fetchLimit,
		})
		if err != nil {
			return nil, err
		}
	}

	filter := map[string]any{"limit": limit, "offset": offset}
	if category != "" {
		filter["category"] = category
	}
	if outcome != "" {
		filter["outcome"] = outcome
	}
	if ip != "" {
		filter["ip"] = ip
	}
	if user != "" {
		filter["user"] = user
	}
	if err := s.ops.RecordSourceIPView(ctx, auditsvc.ViewAccessLog, nil, filter); err != nil {
		return nil, err
	}

	items := make([]AccessItem, 0, len(audits)+len(fetches))
	for _, a := range audits {
		items = append(items, AccessItem{
			Category:  categoryFromAction(a.Action),
			Action:    a.Action,
			Outcome:   a.Outcome,
			IP:        a.IP,
			UserAgent: a.UserAgent,
			UserID:    a.ActorID,
			At:        a.At,
		})
	}
	for _, f := range fetches {
		items = append(items, AccessItem{
			Category:  "subscribe",
			Action:    "subscription.fetch",
			Outcome:   f.Result,
			IP:        f.IP,
			UserAgent: f.UserAgent,
			UserID:    f.UserID,
			At:        f.At,
		})
	}

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].At.After(items[j].At)
	})
	if offset > 0 && offset < len(items) {
		items = items[offset:]
	} else if offset > 0 {
		items = items[:0]
	}
	if len(items) > limit {
		items = items[:limit]
	}

	pageIDSet := make(map[uuid.UUID]struct{})
	for _, it := range items {
		if it.UserID != nil {
			pageIDSet[*it.UserID] = struct{}{}
		}
	}
	if len(pageIDSet) > 0 {
		pageIDs := make([]uuid.UUID, 0, len(pageIDSet))
		for id := range pageIDSet {
			pageIDs = append(pageIDs, id)
		}
		emails, err := s.repo.Emails(ctx, pageIDs)
		if err != nil {
			return nil, err
		}
		for i := range items {
			if items[i].UserID != nil {
				items[i].UserEmail = emails[*items[i].UserID]
			}
		}
	}

	for i := range items {
		items[i].Geo, items[i].NetworkKind = s.locate(items[i].IP)
	}
	if items == nil {
		items = []AccessItem{}
	}
	return items, nil
}

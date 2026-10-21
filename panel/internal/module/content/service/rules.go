package service

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/content/model"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

var allowedKinds = map[string]bool{
	"page": true, "kb_article": true, "tutorial": true, "legal": true,
}

func normalizePublishInput(in PublishInput) (PublishInput, error) {
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.Kind = strings.TrimSpace(in.Kind)
	in.Category = strings.TrimSpace(in.Category)
	in.Title = strings.TrimSpace(in.Title)
	in.Summary = strings.TrimSpace(in.Summary)
	in.Body = strings.TrimSpace(in.Body)
	in.Visibility = strings.TrimSpace(in.Visibility)
	in.Status = strings.TrimSpace(in.Status)
	if in.Kind == "" {
		in.Kind = DefaultKind
	}
	if in.Visibility == "" {
		in.Visibility = VisibilityAuthenticated
	}
	if in.Status == "" {
		in.Status = StatusDraft
	}

	fields := map[string]string{}
	if len(in.Slug) > 80 || !slugPattern.MatchString(in.Slug) {
		fields["slug"] = "标识只能由小写字母、数字和单个连字符组成，最长 80 字符"
	}
	if !allowedKinds[in.Kind] {
		fields["kind"] = "类型必须是 page / kb_article / tutorial / legal"
	}
	if n := utf8.RuneCountInString(in.Category); n > 80 {
		fields["category"] = "分类最长 80 字"
	}
	if n := utf8.RuneCountInString(in.Title); n < 2 || n > 160 {
		fields["title"] = "标题需在 2–160 字之间"
	}
	if n := utf8.RuneCountInString(in.Summary); n > 500 {
		fields["summary"] = "摘要最长 500 字"
	}
	if n := utf8.RuneCountInString(in.Body); n < 10 || n > 100000 {
		fields["body"] = "正文需在 10–100000 字之间"
	}
	if in.Visibility != VisibilityAuthenticated && in.Visibility != VisibilityInternal {
		fields["visibility"] = "可见性必须是 authenticated / internal"
	}
	if in.Status != StatusDraft && in.Status != StatusPublished {
		fields["status"] = "状态必须是 draft / published"
	}

	planSeen := map[string]bool{}
	validPlans := make([]string, 0, len(in.TargetPlanIDs))
	for _, raw := range in.TargetPlanIDs {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			fields["target_plan_ids"] = "套餐 ID 格式不正确"
			continue
		}
		value := id.String()
		if !planSeen[value] {
			planSeen[value] = true
			validPlans = append(validPlans, value)
		}
	}
	sort.Strings(validPlans)
	in.TargetPlanIDs = validPlans

	if strings.Contains(in.Title, "\x00") {
		fields["title"] = "不能包含空字符"
	}
	if strings.Contains(in.Summary, "\x00") {
		fields["summary"] = "不能包含空字符"
	}
	if strings.Contains(in.Body, "\x00") {
		fields["body"] = "不能包含空字符"
	}
	if strings.Contains(in.Category, "\x00") {
		fields["category"] = "不能包含空字符"
	}

	if len(fields) > 0 {
		return PublishInput{}, errs.Invalid(fields)
	}
	return in, nil
}

// encodeList 把字符串列表编成 JSON 数组原文；nil 当空数组。
func encodeList(v []string) string {
	if v == nil {
		v = []string{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// decodeList 解析 JSON 数组；失败或空返回非 nil 的空切片。
func decodeList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}

func toPage(m model.Page) Page {
	return Page{
		ID:               m.ID,
		Slug:             m.Slug,
		Kind:             m.Kind,
		Category:         m.Category,
		Version:          m.Version,
		Title:            m.Title,
		Summary:          m.Summary,
		Body:             m.Body,
		SanitizerVersion: m.SanitizerVersion,
		TargetPlanIDs:    decodeList(m.TargetPlanIDs),
		Visibility:       m.Visibility,
		Status:           m.Status,
		ReviewDueAt:      m.ReviewDueAt,
		PublishedAt:      m.PublishedAt,
		CreatedAt:        m.CreatedAt,
		UpdatedAt:        m.UpdatedAt,
	}
}

// actorID 取当前登录用户 ID。
func actorID(ctx context.Context) *uuid.UUID {
	p, ok := authn.From(ctx)
	if !ok || p.UserID == uuid.Nil {
		return nil
	}
	id := p.UserID
	return &id
}

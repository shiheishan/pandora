package handler

import (
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// listAuditRequest 是审计列表的查询参数。
type listAuditRequest struct {
	Action    string `form:"action" binding:"max=100"`
	ActorKind string `form:"actor_kind" binding:"omitempty,oneof=user admin system agent node plugin anonymous"`
	Outcome   string `form:"outcome" binding:"omitempty,oneof=success failure denied partial"`
	Q         string `form:"q" binding:"max=200"`
}

// exportAuditRequest 是审计 CSV 导出的查询参数。
type exportAuditRequest struct {
	Action    string `form:"action" binding:"max=100"`
	ActorKind string `form:"actor_kind" binding:"omitempty,oneof=user admin system agent node plugin anonymous"`
	Outcome   string `form:"outcome" binding:"omitempty,oneof=success failure denied partial"`
	Q         string `form:"q" binding:"max=200"`
	From      string `form:"from"`
	To        string `form:"to"`
}

// auditEventResponse 与前台 auditEventSchema 字段一致。
type auditEventResponse struct {
	ID            string    `json:"id"`
	OccurredAt    time.Time `json:"occurred_at"`
	ActorKind     string    `json:"actor_kind"`
	ActorEmail    *string   `json:"actor_email"`
	Action        string    `json:"action"`
	ResourceType  *string   `json:"resource_type"`
	ResourceID    *string   `json:"resource_id"`
	APIDomain     *string   `json:"api_domain"`
	Outcome       string    `json:"outcome"`
	Reason        *string   `json:"reason"`
	ResourceLabel *string   `json:"resource_label"`
	AuthContext   *string   `json:"auth_context"`
	SourceIP      *string   `json:"source_ip"`
}

// listAuditResponse 是审计列表的 JSON 形状。
type listAuditResponse struct {
	Events []auditEventResponse `json:"events"`
	Total  int64                `json:"total"`
}

// toAuditEvent 把 service 行转成 API 响应。
func toAuditEvent(r service.Row) auditEventResponse {
	out := auditEventResponse{
		ID:            r.ID.String(),
		OccurredAt:    r.OccurredAt,
		ActorKind:     r.ActorKind,
		ActorEmail:    r.ActorEmail,
		Action:        r.Action,
		ResourceType:  r.ResourceType,
		APIDomain:     r.APIDomain,
		Outcome:       r.Outcome,
		Reason:        r.Reason,
		ResourceLabel: r.ResourceLabel,
		AuthContext:   r.AuthContext,
		SourceIP:      r.SourceIP,
	}
	if r.ResourceID != nil {
		s := r.ResourceID.String()
		out.ResourceID = &s
	}
	return out
}

// filter 转成 service 层筛选条件（不含日期区间）。
func (r listAuditRequest) filter() service.Filter {
	return service.Filter{
		ActionPrefix: r.Action,
		ActorKind:    r.ActorKind,
		Outcome:      r.Outcome,
		Query:        r.Q,
	}
}

// parseExportRange 解析导出日期：YYYY-MM-DD，半开区间 [from, to+1 天)。
func parseExportRange(from, to string) (*time.Time, *time.Time, error) {
	parse := func(field, raw string) (*time.Time, error) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil, nil
		}
		t, err := time.Parse("2006-01-02", raw)
		if err != nil {
			return nil, errs.Invalid(map[string]string{field: "日期格式应为 YYYY-MM-DD"})
		}
		return &t, nil
	}
	fromT, err := parse("from", from)
	if err != nil {
		return nil, nil, err
	}
	toT, err := parse("to", to)
	if err != nil {
		return nil, nil, err
	}
	if toT != nil {
		next := toT.AddDate(0, 0, 1)
		toT = &next
	}
	if fromT != nil && toT != nil && !fromT.Before(*toT) {
		return nil, nil, errs.Invalid(map[string]string{"to": "结束日期不能早于开始日期"})
	}
	return fromT, toT, nil
}

// exportFilter 转成带日期区间的 service 筛选条件。
func (r exportAuditRequest) exportFilter(from, to *time.Time) service.Filter {
	f := service.Filter{
		ActionPrefix: r.Action,
		ActorKind:    r.ActorKind,
		Outcome:      r.Outcome,
		Query:        r.Q,
		From:         from,
		To:           to,
	}
	return f
}

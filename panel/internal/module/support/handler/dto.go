package handler

import (
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/support/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// ---------------------------------------------------------------------------
// 请求
// ---------------------------------------------------------------------------

type createRequest struct {
	Subject  string `json:"subject"`
	Category string `json:"category"`
	Body     string `json:"body"`
	OrderID  string `json:"order_id"`
}

func (r createRequest) orderID() (*uuid.UUID, error) {
	raw := strings.TrimSpace(r.OrderID)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, errs.Invalid(map[string]string{"order_id": "订单不存在"})
	}
	return &id, nil
}

type bodyRequest struct {
	Body string `json:"body"`
}

type withdrawRequest struct {
	Reason string `json:"reason"`
}

type agentReplyRequest struct {
	Body         string `json:"body"`
	InternalNote bool   `json:"internal_note"`
}

type assignRequest struct {
	AssignedTo string `json:"assigned_to"`
}

func (r assignRequest) assigneeID() (*uuid.UUID, error) {
	raw := strings.TrimSpace(r.AssignedTo)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, errs.Invalid(map[string]string{"assigned_to": "该用户不是可指派的管理员"})
	}
	return &id, nil
}

type statusRequest struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type macroRequest struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	SortOrder int    `json:"sort_order"`
}

// ---------------------------------------------------------------------------
// 响应
// ---------------------------------------------------------------------------

type orderRefDTO struct {
	ID      string `json:"id"`
	OrderNo string `json:"order_no"`
}

type ticketRowDTO struct {
	ID           string       `json:"id"`
	TicketNo     string       `json:"ticket_no"`
	Subject      string       `json:"subject"`
	Category     string       `json:"category"`
	Priority     string       `json:"priority"`
	Status       string       `json:"status"`
	CreatedAt    time.Time    `json:"created_at"`
	UpdatedAt    time.Time    `json:"updated_at"`
	ResolvedAt   *time.Time   `json:"resolved_at"`
	MessageCount int          `json:"message_count"`
	LastReplyAt  time.Time    `json:"last_reply_at"`
	ClosedReason *string      `json:"closed_reason"`
	RelatedOrder *orderRefDTO `json:"related_order"`
}

type portalMessageDTO struct {
	ID         string    `json:"id"`
	AuthorKind string    `json:"author_kind"`
	AuthorName *string   `json:"author_name"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"created_at"`
}

type portalDetailDTO struct {
	ticketRowDTO
	ReplyBlocked bool               `json:"reply_blocked"`
	Messages     []portalMessageDTO `json:"messages"`
}

type adminTicketDTO struct {
	ID                    string       `json:"id"`
	TicketNo              string       `json:"ticket_no"`
	Subject               string       `json:"subject"`
	Category              string       `json:"category"`
	Priority              string       `json:"priority"`
	Status                string       `json:"status"`
	CreatedAt             time.Time    `json:"created_at"`
	UpdatedAt             time.Time    `json:"updated_at"`
	ResolvedAt            *time.Time   `json:"resolved_at"`
	UserID                string       `json:"user_id"`
	UserEmail             string       `json:"user_email"`
	AssignedTo            *string      `json:"assigned_to,omitempty"`
	AssigneeEmail         *string      `json:"assignee_email,omitempty"`
	SLAFirstResponseDue   time.Time    `json:"sla_first_response_due"`
	SLAResolutionDue      time.Time    `json:"sla_resolution_due"`
	FirstRespondedAt      *time.Time   `json:"first_responded_at,omitempty"`
	EscalatedAt           *time.Time   `json:"escalated_at,omitempty"`
	SLABreached           bool         `json:"sla_breached,omitempty"`
	LastMessageAuthorKind string       `json:"last_message_author_kind,omitempty"`
	UserActivePlan        *string      `json:"user_active_plan,omitempty"`
	MessageCount          int          `json:"message_count"`
	LastReplyAt           time.Time    `json:"last_reply_at"`
	ClosedReason          *string      `json:"closed_reason"`
	RelatedOrder          *orderRefDTO `json:"related_order"`
}

type adminMessageDTO struct {
	ID           string    `json:"id"`
	AuthorKind   string    `json:"author_kind"`
	AuthorName   *string   `json:"author_name"`
	Body         string    `json:"body"`
	InternalNote bool      `json:"internal_note,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type adminDetailDTO struct {
	adminTicketDTO
	Messages []adminMessageDTO `json:"messages"`
}

type queueResponse struct {
	Tickets []adminTicketDTO `json:"tickets"`
	Total   int64            `json:"total"`
}

type assigneeDTO struct {
	ID          string  `json:"id"`
	Email       string  `json:"email"`
	DisplayName *string `json:"display_name,omitempty"`
}

type assigneesResponse struct {
	Assignees []assigneeDTO `json:"assignees"`
}

type macroDTO struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	SortOrder int       `json:"sort_order"`
	UpdatedAt time.Time `json:"updated_at"`
}

type macrosResponse struct {
	Macros []macroDTO `json:"macros"`
}

type macroSavedResponse struct {
	ID string `json:"id"`
}

type categoryDTO struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type categoriesResponse struct {
	Categories []categoryDTO `json:"categories"`
}

type myTicketsResponse struct {
	Tickets []ticketRowDTO `json:"tickets"`
}

type okResponse struct {
	OK bool `json:"ok"`
}

type escalatedResponse struct {
	Escalated int `json:"escalated"`
}

type withdrawnResponse struct {
	Withdrawn bool `json:"withdrawn"`
}

// ---------------------------------------------------------------------------
// 转换
// ---------------------------------------------------------------------------

func orderRefFrom(r *service.OrderRef) *orderRefDTO {
	if r == nil {
		return nil
	}
	return &orderRefDTO{ID: r.ID.String(), OrderNo: r.OrderNo}
}

func ticketRowFrom(t service.Ticket) ticketRowDTO {
	return ticketRowDTO{
		ID: t.ID.String(), TicketNo: t.TicketNo, Subject: t.Subject, Category: t.Category,
		Priority: t.Priority, Status: t.Status, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		ResolvedAt: t.ResolvedAt, MessageCount: t.MessageCount, LastReplyAt: t.LastReplyAt,
		ClosedReason: t.ClosedReason, RelatedOrder: orderRefFrom(t.RelatedOrder),
	}
}

func portalDetailFrom(d service.TicketDetail) portalDetailDTO {
	out := portalDetailDTO{
		ticketRowDTO: ticketRowFrom(d.Ticket),
		ReplyBlocked: d.ReplyBlocked,
		Messages:     make([]portalMessageDTO, 0, len(d.Messages)),
	}
	for _, m := range d.Messages {
		out.Messages = append(out.Messages, portalMessageDTO{
			ID: m.ID.String(), AuthorKind: m.AuthorKind, AuthorName: m.AuthorName,
			Body: m.Body, CreatedAt: m.CreatedAt,
		})
	}
	return out
}

func adminTicketFrom(t service.Ticket) adminTicketDTO {
	out := adminTicketDTO{
		ID: t.ID.String(), TicketNo: t.TicketNo, Subject: t.Subject, Category: t.Category,
		Priority: t.Priority, Status: t.Status, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
		ResolvedAt: t.ResolvedAt, UserID: t.UserID.String(), UserEmail: t.UserEmail,
		SLAFirstResponseDue: t.SLAFirstResponseDue, SLAResolutionDue: t.SLAResolutionDue,
		MessageCount: t.MessageCount, LastReplyAt: t.LastReplyAt,
		ClosedReason: t.ClosedReason, RelatedOrder: orderRefFrom(t.RelatedOrder),
	}
	if t.AssignedTo != nil {
		s := t.AssignedTo.String()
		out.AssignedTo = &s
	}
	if t.AssigneeEmail != nil {
		out.AssigneeEmail = t.AssigneeEmail
	}
	if t.FirstRespondedAt != nil {
		out.FirstRespondedAt = t.FirstRespondedAt
	}
	if t.EscalatedAt != nil {
		out.EscalatedAt = t.EscalatedAt
	}
	if t.SLABreached {
		out.SLABreached = true
	}
	if t.LastMessageAuthorKind != "" {
		out.LastMessageAuthorKind = t.LastMessageAuthorKind
	}
	if t.UserActivePlan != nil {
		out.UserActivePlan = t.UserActivePlan
	}
	return out
}

func adminDetailFrom(d service.TicketDetail) adminDetailDTO {
	out := adminDetailDTO{adminTicketDTO: adminTicketFrom(d.Ticket), Messages: make([]adminMessageDTO, 0, len(d.Messages))}
	for _, m := range d.Messages {
		msg := adminMessageDTO{
			ID: m.ID.String(), AuthorKind: m.AuthorKind, AuthorName: m.AuthorName,
			Body: m.Body, CreatedAt: m.CreatedAt,
		}
		if m.InternalNote {
			msg.InternalNote = true
		}
		out.Messages = append(out.Messages, msg)
	}
	return out
}

func assigneesFrom(rows []service.Assignee) assigneesResponse {
	out := assigneesResponse{Assignees: make([]assigneeDTO, 0, len(rows))}
	for _, a := range rows {
		row := assigneeDTO{ID: a.ID.String(), Email: a.Email}
		if a.DisplayName != nil {
			row.DisplayName = a.DisplayName
		}
		out.Assignees = append(out.Assignees, row)
	}
	return out
}

func macrosFrom(rows []service.Macro) macrosResponse {
	out := macrosResponse{Macros: make([]macroDTO, 0, len(rows))}
	for _, m := range rows {
		out.Macros = append(out.Macros, macroDTO{
			ID: m.ID.String(), Title: m.Title, Body: m.Body, SortOrder: m.SortOrder, UpdatedAt: m.UpdatedAt,
		})
	}
	return out
}

func categoriesFrom(rows []service.Category) categoriesResponse {
	out := categoriesResponse{Categories: make([]categoryDTO, 0, len(rows))}
	for _, c := range rows {
		out.Categories = append(out.Categories, categoryDTO{Code: c.Code, Name: c.Name})
	}
	return out
}

func okReply() (idemsvc.Response, error) {
	return idemsvc.JSON(http.StatusOK, okResponse{OK: true})
}

func createReply(t service.Ticket) (idemsvc.Response, error) {
	return idemsvc.JSON(http.StatusCreated, ticketRowFrom(t))
}

func escalateReply(n int) (idemsvc.Response, error) {
	return idemsvc.JSON(http.StatusOK, escalatedResponse{Escalated: n})
}

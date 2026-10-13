package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"

	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/support/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/route"
)

// fakeSvc 只实现用例设置了的方法。
type fakeSvc struct {
	Service
	categories  func() []service.Category
	myTickets   func() ([]service.Ticket, error)
	myTicket    func(uuid.UUID) (service.TicketDetail, error)
	create      func(service.CreateInput) (service.Ticket, error)
	userClose   func(uuid.UUID, service.ReplyFunc) error
	withdraw    func(uuid.UUID, string) error
	queue       func(service.QueueFilter) ([]service.Ticket, int64, error)
	adminTicket func(uuid.UUID) (service.TicketDetail, error)
	assignees   func() ([]service.Assignee, error)
	listMacros  func() ([]service.Macro, error)
	saveMacro   func(*uuid.UUID, service.MacroInput) (uuid.UUID, error)
	deleteMacro func(uuid.UUID) error
	agentReply  func(service.AgentReplyInput) error
	assign      func(service.AssignInput) error
	setStatus   func(service.StatusInput) error
	escalate    func(func(int) (idemsvc.Response, error)) (int, error)
}

func (f *fakeSvc) Categories() []service.Category {
	if f.categories != nil {
		return f.categories()
	}
	return nil
}
func (f *fakeSvc) MyTickets(_ context.Context) ([]service.Ticket, error) {
	if f.myTickets != nil {
		return f.myTickets()
	}
	return nil, nil
}
func (f *fakeSvc) MyTicket(_ context.Context, id uuid.UUID) (service.TicketDetail, error) {
	return f.myTicket(id)
}
func (f *fakeSvc) Create(_ context.Context, in service.CreateInput) (service.Ticket, error) {
	return f.create(in)
}
func (f *fakeSvc) UserClose(_ context.Context, id uuid.UUID, reply service.ReplyFunc) error {
	return f.userClose(id, reply)
}
func (f *fakeSvc) Withdraw(_ context.Context, id uuid.UUID, reason string) error {
	return f.withdraw(id, reason)
}
func (f *fakeSvc) Queue(_ context.Context, ftr service.QueueFilter) ([]service.Ticket, int64, error) {
	return f.queue(ftr)
}
func (f *fakeSvc) AdminTicket(_ context.Context, id uuid.UUID) (service.TicketDetail, error) {
	return f.adminTicket(id)
}
func (f *fakeSvc) Assignees(_ context.Context) ([]service.Assignee, error) {
	return f.assignees()
}
func (f *fakeSvc) ListMacros(_ context.Context) ([]service.Macro, error) {
	return f.listMacros()
}
func (f *fakeSvc) SaveMacro(_ context.Context, id *uuid.UUID, in service.MacroInput) (uuid.UUID, error) {
	return f.saveMacro(id, in)
}
func (f *fakeSvc) DeleteMacro(_ context.Context, id uuid.UUID) error {
	return f.deleteMacro(id)
}
func (f *fakeSvc) AgentReply(_ context.Context, in service.AgentReplyInput) error {
	if f.agentReply != nil {
		return f.agentReply(in)
	}
	return nil
}
func (f *fakeSvc) Assign(_ context.Context, in service.AssignInput) error {
	return f.assign(in)
}
func (f *fakeSvc) SetStatus(_ context.Context, in service.StatusInput) error {
	if f.setStatus != nil {
		return f.setStatus(in)
	}
	return nil
}
func (f *fakeSvc) EscalateOverdue(_ context.Context, reply func(int) (idemsvc.Response, error)) (int, error) {
	return f.escalate(reply)
}

type kitRec struct {
	scopes []string
}

func pass(c *gin.Context) { c.Next() }

func newEngine(svc Service, admin bool, rec *kitRec) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.RequestID(zap.NewNop()), httpx.Errors())
	audience := authn.AudiencePublic
	if admin {
		audience = authn.AudienceAdmin
	}
	p := authn.Principal{UserID: uuid.New(), SessionID: "s", Audience: audience}
	e.Use(func(c *gin.Context) { authn.Set(c, p); c.Next() })
	if rec == nil {
		rec = &kitRec{}
	}
	kit := route.Kit{
		RequireAuth: pass,
		Idempotency: func(scope string) gin.HandlerFunc {
			return func(c *gin.Context) {
				rec.scopes = append(rec.scopes, scope)
				c.Next()
			}
		},
	}
	h := New(svc, nil)
	if admin {
		h.RegisterAdmin(e.Group("/v1"), kit)
	} else {
		h.RegisterPublic(e.Group("/v1"), kit)
	}
	return e
}

func do(e *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func mustStatus(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want {
		t.Fatalf("状态 %d，期望 %d：%s", w.Code, want, w.Body)
	}
}

func obj(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("不是 JSON 对象: %v %s", err, b)
	}
	return m
}

func keysExactly(t *testing.T, m map[string]any, want []string) {
	t.Helper()
	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("键 %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("键 %v，期望 %v", got, want)
		}
	}
}

var portalRowKeys = []string{
	"id", "ticket_no", "subject", "category", "priority", "status",
	"created_at", "updated_at", "resolved_at", "message_count", "last_reply_at",
	"closed_reason", "related_order",
}

var portalMsgKeys = []string{"id", "author_kind", "author_name", "body", "created_at"}

func TestPortalListRowKeys(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	f := &fakeSvc{myTickets: func() ([]service.Ticket, error) {
		return []service.Ticket{{
			ID: uuid.New(), TicketNo: "TK1", Subject: "s", Category: "general", Priority: "normal",
			Status: "open", CreatedAt: now, UpdatedAt: now, MessageCount: 0, LastReplyAt: now,
		}}, nil
	}}
	w := do(newEngine(f, false, nil), http.MethodGet, "/v1/support/tickets", "")
	mustStatus(t, w, http.StatusOK)
	rows := obj(t, w.Body.Bytes())["tickets"].([]any)
	if len(rows) != 1 {
		t.Fatalf("行数 %d", len(rows))
	}
	row := rows[0].(map[string]any)
	keysExactly(t, row, portalRowKeys)
	if row["related_order"] != nil || row["resolved_at"] != nil || row["closed_reason"] != nil {
		t.Fatalf("可空字段应为 null: %v", row)
	}
}

func TestPortalDetailMessagesAndOrder(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	tid, oid := uuid.New(), uuid.New()
	f := &fakeSvc{myTicket: func(id uuid.UUID) (service.TicketDetail, error) {
		if id != tid {
			t.Fatalf("id %s", id)
		}
		return service.TicketDetail{
			Ticket: service.Ticket{
				ID: tid, TicketNo: "TK", Subject: "s", Category: "general", Priority: "normal",
				Status: "open", CreatedAt: now, UpdatedAt: now, MessageCount: 1, LastReplyAt: now,
				RelatedOrder: &service.OrderRef{ID: oid, OrderNo: "O1"},
			},
			Messages: []service.Message{{
				ID: uuid.New(), AuthorKind: "user", AuthorName: nil, Body: "hi", CreatedAt: now,
			}},
		}, nil
	}}
	w := do(newEngine(f, false, nil), http.MethodGet, "/v1/support/tickets/"+tid.String(), "")
	mustStatus(t, w, http.StatusOK)
	m := obj(t, w.Body.Bytes())
	msgs := m["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages %v", msgs)
	}
	keysExactly(t, msgs[0].(map[string]any), portalMsgKeys)
	ro := m["related_order"].(map[string]any)
	keysExactly(t, ro, []string{"id", "order_no"})

	f2 := &fakeSvc{myTicket: func(uuid.UUID) (service.TicketDetail, error) {
		return service.TicketDetail{Ticket: service.Ticket{
			ID: tid, TicketNo: "TK", Subject: "s", Category: "general", Priority: "normal",
			Status: "open", CreatedAt: now, UpdatedAt: now, MessageCount: 0, LastReplyAt: now,
		}, Messages: []service.Message{}}, nil
	}}
	w2 := do(newEngine(f2, false, nil), http.MethodGet, "/v1/support/tickets/"+tid.String(), "")
	mustStatus(t, w2, http.StatusOK)
	msgs2 := obj(t, w2.Body.Bytes())["messages"]
	if msgs2 == nil {
		t.Fatal("messages 不应为 null")
	}
	if len(msgs2.([]any)) != 0 {
		t.Fatalf("期望 []，得 %v", msgs2)
	}
}

func TestCreateReplyAndValidation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	var got service.CreateInput
	f := &fakeSvc{create: func(in service.CreateInput) (service.Ticket, error) {
		got = in
		return service.Ticket{
			ID: uuid.New(), TicketNo: "TK", Subject: "sub", Category: "general", Priority: "normal",
			Status: "open", CreatedAt: now, UpdatedAt: now, MessageCount: 1, LastReplyAt: now,
		}, nil
	}}
	rec := &kitRec{}
	w := do(newEngine(f, false, rec), http.MethodPost, "/v1/support/tickets",
		`{"subject":"sub","category":"general","body":"1234567890"}`)
	mustStatus(t, w, http.StatusCreated)
	keysExactly(t, obj(t, w.Body.Bytes()), portalRowKeys)
	if got.Reply == nil {
		t.Fatal("Reply 应为非 nil")
	}
	r, err := got.Reply(service.Ticket{ID: uuid.New(), TicketNo: "TK", Subject: "sub", Category: "general",
		Priority: "normal", Status: "open", CreatedAt: now, UpdatedAt: now, MessageCount: 1, LastReplyAt: now})
	if err != nil || r.Status != http.StatusCreated {
		t.Fatalf("reply %d %v", r.Status, err)
	}
	if len(rec.scopes) != 1 || rec.scopes[0] != ScopeCreate {
		t.Fatalf("幂等 %+v", rec.scopes)
	}
	bad := do(newEngine(f, false, nil), http.MethodPost, "/v1/support/tickets",
		`{"body":"1234567890","order_id":"not-uuid"}`)
	mustStatus(t, bad, http.StatusUnprocessableEntity)
}

func TestAdminQueueJSONShape(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	uid := uuid.New()
	assignID := uuid.New()
	plan := "Pro"
	f := &fakeSvc{queue: func(service.QueueFilter) ([]service.Ticket, int64, error) {
		return []service.Ticket{
			{
				ID: uuid.New(), TicketNo: "A", Subject: "s", Category: "general", Priority: "normal",
				Status: "open", CreatedAt: now, UpdatedAt: now, MessageCount: 0, LastReplyAt: now,
				UserID: uid, UserEmail: "u@e", SLAFirstResponseDue: now, SLAResolutionDue: now,
			},
			{
				ID: uuid.New(), TicketNo: "B", Subject: "s", Category: "general", Priority: "urgent",
				Status: "escalated", CreatedAt: now, UpdatedAt: now, ResolvedAt: &now, MessageCount: 2, LastReplyAt: now,
				UserID: uid, UserEmail: "u@e", AssignedTo: &assignID, AssigneeEmail: strPtr("a@e"),
				SLAFirstResponseDue: now, SLAResolutionDue: now, FirstRespondedAt: &now, EscalatedAt: &now,
				SLABreached: true, LastMessageAuthorKind: "user", UserActivePlan: &plan,
			},
		}, 2, nil
	}}
	w := do(newEngine(f, true, nil), http.MethodGet, "/v1/tickets", "")
	mustStatus(t, w, http.StatusOK)
	root := obj(t, w.Body.Bytes())
	if root["total"] != float64(2) {
		t.Fatalf("total %v", root["total"])
	}
	rows := root["tickets"].([]any)
	sparse := rows[0].(map[string]any)
	for _, k := range []string{"assigned_to", "assignee_email", "first_responded_at", "escalated_at",
		"sla_breached", "last_message_author_kind", "user_active_plan"} {
		if _, ok := sparse[k]; ok {
			t.Fatalf("稀疏行不应有 %s", k)
		}
	}
	if _, ok := sparse["closed_reason"]; !ok {
		t.Fatal("closed_reason 键应在")
	}
	if sparse["closed_reason"] != nil {
		t.Fatalf("closed_reason 应为 null: %v", sparse["closed_reason"])
	}
	if _, ok := sparse["related_order"]; !ok || sparse["related_order"] != nil {
		t.Fatalf("related_order: %v", sparse["related_order"])
	}
	for _, k := range []string{"user_id", "user_email", "sla_first_response_due", "sla_resolution_due"} {
		if _, ok := sparse[k]; !ok {
			t.Fatalf("缺 %s", k)
		}
	}
	full := rows[1].(map[string]any)
	for _, k := range []string{"assigned_to", "assignee_email", "first_responded_at", "escalated_at",
		"sla_breached", "last_message_author_kind", "user_active_plan"} {
		if _, ok := full[k]; !ok {
			t.Fatalf("满行缺 %s", k)
		}
	}
	if full["sla_breached"] != true {
		t.Fatalf("sla_breached %v", full["sla_breached"])
	}
}

func strPtr(s string) *string { return &s }

func TestAdminQueueParams(t *testing.T) {
	var got service.QueueFilter
	f := &fakeSvc{queue: func(ftr service.QueueFilter) ([]service.Ticket, int64, error) {
		got = ftr
		return []service.Ticket{}, 0, nil
	}}
	path := "/v1/tickets?status=open,%20pending_agent,&assigned_to=-&breached=1&q=%20x%20&limit=500"
	w := do(newEngine(f, true, nil), http.MethodGet, path, "")
	mustStatus(t, w, http.StatusOK)
	want := []string{"open", "pending_agent"}
	if len(got.Statuses) != 2 || got.Statuses[0] != want[0] || got.Statuses[1] != want[1] {
		t.Fatalf("Statuses %+v", got.Statuses)
	}
	if got.AssignedTo == nil || *got.AssignedTo != uuid.Nil {
		t.Fatalf("AssignedTo %+v", got.AssignedTo)
	}
	if !got.OnlyBreached || got.Query != "x" || got.Limit != 100 {
		t.Fatalf("filter %+v", got)
	}
}

func TestAdminDetailInternalNote(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	tid := uuid.New()
	f := &fakeSvc{adminTicket: func(id uuid.UUID) (service.TicketDetail, error) {
		return service.TicketDetail{
			Ticket: service.Ticket{
				ID: tid, TicketNo: "TK", Subject: "s", Category: "general", Priority: "normal",
				Status: "open", CreatedAt: now, UpdatedAt: now, MessageCount: 2, LastReplyAt: now,
				UserID: uuid.New(), UserEmail: "u@e", SLAFirstResponseDue: now, SLAResolutionDue: now,
			},
			Messages: []service.Message{
				{ID: uuid.New(), AuthorKind: "agent", AuthorName: nil, Body: "pub", CreatedAt: now},
				{ID: uuid.New(), AuthorKind: "agent", Body: "note", InternalNote: true, CreatedAt: now},
			},
		}, nil
	}}
	w := do(newEngine(f, true, nil), http.MethodGet, "/v1/tickets/"+tid.String(), "")
	mustStatus(t, w, http.StatusOK)
	msgs := obj(t, w.Body.Bytes())["messages"].([]any)
	pub := msgs[0].(map[string]any)
	if _, ok := pub["internal_note"]; ok {
		t.Fatal("普通消息不应有 internal_note")
	}
	if _, ok := pub["author_name"]; !ok {
		t.Fatal("author_name 键应在")
	}
	note := msgs[1].(map[string]any)
	if note["internal_note"] != true {
		t.Fatalf("内部备注 %v", note)
	}
}

func TestAssigneesDisplayName(t *testing.T) {
	f := &fakeSvc{assignees: func() ([]service.Assignee, error) {
		return []service.Assignee{{ID: uuid.New(), Email: "a@e", DisplayName: nil}}, nil
	}}
	w := do(newEngine(f, true, nil), http.MethodGet, "/v1/tickets/assignees", "")
	mustStatus(t, w, http.StatusOK)
	row := obj(t, w.Body.Bytes())["assignees"].([]any)[0].(map[string]any)
	if _, ok := row["display_name"]; ok {
		t.Fatal("display_name 不应出现")
	}
}

func TestMacrosCRUD(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	mid := uuid.New()
	f := &fakeSvc{
		listMacros: func() ([]service.Macro, error) {
			return []service.Macro{{ID: mid, Title: "t", Body: "b", SortOrder: 1, UpdatedAt: now}}, nil
		},
		saveMacro: func(id *uuid.UUID, in service.MacroInput) (uuid.UUID, error) {
			if id != nil {
				return *id, nil
			}
			return uuid.New(), nil
		},
		deleteMacro: func(uuid.UUID) error { return nil },
	}
	e := newEngine(f, true, nil)
	w := do(e, http.MethodGet, "/v1/ticket-macros", "")
	mustStatus(t, w, http.StatusOK)
	macro := obj(t, w.Body.Bytes())["macros"].([]any)[0].(map[string]any)
	for _, k := range []string{"id", "title", "body", "sort_order", "updated_at"} {
		if _, ok := macro[k]; !ok {
			t.Fatalf("缺 %s", k)
		}
	}
	w2 := do(e, http.MethodPost, "/v1/ticket-macros", `{"title":"t","body":"b","sort_order":0}`)
	mustStatus(t, w2, http.StatusOK)
	if _, ok := obj(t, w2.Body.Bytes())["id"]; !ok {
		t.Fatal("缺 id")
	}
	w3 := do(e, http.MethodPost, "/v1/ticket-macros/"+mid.String(), `{"title":"t","body":"b","sort_order":0}`)
	mustStatus(t, w3, http.StatusOK)
	w4 := do(e, http.MethodDelete, "/v1/ticket-macros/"+mid.String(), "")
	mustStatus(t, w4, http.StatusOK)
	if obj(t, w4.Body.Bytes())["ok"] != true {
		t.Fatalf("delete %s", w4.Body)
	}
}

func TestAssignValidation(t *testing.T) {
	tid := uuid.New()
	var got *uuid.UUID
	f := &fakeSvc{assign: func(in service.AssignInput) error {
		got = in.AssigneeID
		return nil
	}}
	e := newEngine(f, true, nil)
	w := do(e, http.MethodPost, "/v1/tickets/"+tid.String()+"/assign", `{"assigned_to":""}`)
	mustStatus(t, w, http.StatusOK)
	if got != nil {
		t.Fatalf("期望 nil assignee，得 %v", got)
	}
	bad := do(e, http.MethodPost, "/v1/tickets/"+tid.String()+"/assign", `{"assigned_to":"bad"}`)
	mustStatus(t, bad, http.StatusUnprocessableEntity)
}

func TestInvalidPathID404(t *testing.T) {
	f := &fakeSvc{
		myTicket: func(uuid.UUID) (service.TicketDetail, error) {
			t.Fatal("不该调用")
			return service.TicketDetail{}, nil
		},
		adminTicket: func(uuid.UUID) (service.TicketDetail, error) {
			t.Fatal("不该调用")
			return service.TicketDetail{}, nil
		},
	}
	w := do(newEngine(f, false, nil), http.MethodGet, "/v1/support/tickets/not-uuid", "")
	mustStatus(t, w, http.StatusNotFound)
	w2 := do(newEngine(f, true, nil), http.MethodGet, "/v1/tickets/not-uuid", "")
	mustStatus(t, w2, http.StatusNotFound)
}

func TestEscalateWithdrawClose(t *testing.T) {
	tid := uuid.New()
	f := &fakeSvc{
		escalate:  func(func(int) (idemsvc.Response, error)) (int, error) { return 3, nil },
		withdraw:  func(uuid.UUID, string) error { return nil },
		userClose: func(uuid.UUID, service.ReplyFunc) error { return nil },
	}
	w := do(newEngine(f, true, nil), http.MethodPost, "/v1/tickets/escalate", "")
	mustStatus(t, w, http.StatusOK)
	if obj(t, w.Body.Bytes())["escalated"] != float64(3) {
		t.Fatalf("escalate %s", w.Body)
	}
	w2 := do(newEngine(f, false, nil), http.MethodPost, "/v1/support/tickets/"+tid.String()+"/withdraw", `{}`)
	mustStatus(t, w2, http.StatusOK)
	if obj(t, w2.Body.Bytes())["withdrawn"] != true {
		t.Fatalf("withdraw %s", w2.Body)
	}
	w3 := do(newEngine(f, false, nil), http.MethodPost, "/v1/support/tickets/"+tid.String()+"/close", "")
	mustStatus(t, w3, http.StatusOK)
	if obj(t, w3.Body.Bytes())["ok"] != true {
		t.Fatalf("close %s", w3.Body)
	}
}

func TestAdminIdempotencyScopes(t *testing.T) {
	tid := uuid.New()
	f := &fakeSvc{
		assign:   func(service.AssignInput) error { return nil },
		escalate: func(func(int) (idemsvc.Response, error)) (int, error) { return 0, nil },
	}
	rec := &kitRec{}
	e := newEngine(f, true, rec)
	do(e, http.MethodPost, "/v1/tickets/"+tid.String()+"/reply", `{"body":"hello","internal_note":false}`)
	do(e, http.MethodPost, "/v1/tickets/"+tid.String()+"/assign", `{"assigned_to":""}`)
	do(e, http.MethodPost, "/v1/tickets/"+tid.String()+"/status", `{"status":"resolved","reason":""}`)
	do(e, http.MethodPost, "/v1/tickets/escalate", "")
	want := []string{ScopeAgentReply, ScopeAssign, ScopeStatus, ScopeEscalate}
	if len(rec.scopes) != len(want) {
		t.Fatalf("scopes %v 期望 %v", rec.scopes, want)
	}
	for i, s := range want {
		if rec.scopes[i] != s {
			t.Fatalf("scopes %v", rec.scopes)
		}
	}
}

package admin

import (
	"os"
	"strings"
	"testing"
)

func TestAdminSupportAssigneeCatalogIsReadProtectedAndPrecedesIDRoute(t *testing.T) {
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	catalog := strings.Index(source, `Get("/tickets/assignees", h.ticketAssignees)`)
	queue := strings.Index(source, `Get("/tickets", h.ticketQueue)`)
	detail := strings.Index(source, `Get("/tickets/{id}", h.ticketDetail)`)
	if catalog < 0 || queue < 0 || detail < 0 || catalog >= detail {
		t.Fatal("assignee catalog must be registered before /tickets/{id}")
	}
	window := source[max(0, catalog-180):catalog]
	if !strings.Contains(window, `RequirePermission("ops.ticket.read"`) {
		t.Fatal("assignee catalog must require ops.ticket.read")
	}
}

func TestAdminSupportAssigneeCatalogUsesDedicatedDomainQuery(t *testing.T) {
	raw, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	if !strings.Contains(source, "Support.ListEligibleAssignees(") {
		t.Fatal("assignee handler must use the dedicated eligible-assignee query")
	}
	if strings.Contains(source, "Ops.ListUsers") {
		// The admin package legitimately uses Ops.ListUsers elsewhere; ensure the
		// assignee handler itself does not become a wrapper around that directory.
		start := strings.Index(source, "func (h *handlers) ticketAssignees")
		end := strings.Index(source[start:], "func (h *handlers) ticketQueue")
		if start >= 0 && end > 0 && strings.Contains(source[start:start+end], "Ops.ListUsers") {
			t.Fatal("assignee handler must not use the ordinary user directory")
		}
	}
}
func TestAdminSupportWritesRequirePermissionThenIdempotency(t *testing.T) {
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, tc := range []struct {
		route string
		scope string
	}{
		{`Post("/tickets/{id}/reply", h.ticketReply)`, "support.AgentReplyIdempotencyScope"},
		{`Post("/tickets/{id}/assign", h.ticketAssign)`, "support.AssignIdempotencyScope"},
		{`Post("/tickets/{id}/status", h.ticketStatus)`, "support.StatusIdempotencyScope"},
		{`Post("/tickets/escalate", h.ticketEscalate)`, "support.EscalateIdempotencyScope"},
	} {
		at := strings.Index(source, tc.route)
		if at < 0 {
			t.Fatalf("admin support route missing %q", tc.route)
		}
		window := source[max(0, at-360):at]
		permissionAt := strings.LastIndex(window, `RequirePermission("ops.ticket.write"`)
		idemAt := strings.LastIndex(window, tc.scope)
		if permissionAt < 0 || idemAt < 0 || permissionAt >= idemAt {
			t.Fatalf("route %q must apply write permission before %s", tc.route, tc.scope)
		}
		if strings.Contains(window, "RequireRecentReauth") {
			t.Fatalf("ordinary support route %q must not require recent reauthentication", tc.route)
		}
	}
}

func TestAdminSupportHandlersUseAtomicPreparedResponses(t *testing.T) {
	raw, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, want := range []string{
		"Support.ReplyAsAgentAtomic(",
		"Support.AssignAtomic(",
		"Support.SetStatusAtomic(",
		"Support.EscalateOverdueAsAdminAtomic(",
		"middleware.IdempotencyClaimFrom(r.Context())",
		"httpx.WritePrepared(w, out.PreparedResponse())",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("admin support handler contract missing %q", want)
		}
	}
}

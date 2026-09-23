package support

import (
	"os"
	"strings"
	"testing"
)

func TestSupportAtomicMutationContracts(t *testing.T) {
	raw, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, want := range []string{
		"middleware.CompleteSuccessJSONInTx(ctx, tx, claim, prepared)",
		`Action: "ticket.reply"`,
		`action = "ticket.internal_note"`,
		`Action: "ticket.sla_escalate"`,
		`"body_runes": len([]rune(body))`,
		`"body_runes": len([]rune(in.Body))`,
		`if len([]rune(reason)) > 500`,
		"FOR UPDATE",
		"ORDER BY sla_first_response_due, id",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("support atomic contract missing %q", want)
		}
	}
	if strings.Contains(source, `"body": body`) || strings.Contains(source, `"body": in.Body`) ||
		strings.Contains(source, "body_hash") {
		t.Fatal("support audit evidence must not store reply content or a correlatable body hash")
	}
}

func TestSchedulerEntryDoesNotRequireHTTPClaim(t *testing.T) {
	raw, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "func (s *Service) EscalateOverdue(")
	end := strings.Index(source[start:], "func (s *Service) EscalateOverdueAsAdminAtomic(")
	if start < 0 || end < 0 {
		t.Fatal("scheduler/admin SLA entry points not found")
	}
	window := source[start : start+end]
	if strings.Contains(window, "IdempotencyClaim") {
		t.Fatal("scheduled SLA escalation must remain independent of an HTTP claim")
	}
	if !strings.Contains(window, `"system", nil, "admin", nil`) {
		t.Fatal("scheduled SLA audit must use system actor in the schema-supported admin domain")
	}
}

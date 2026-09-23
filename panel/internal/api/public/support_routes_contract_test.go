package public

import (
	"os"
	"strings"
	"testing"
)

func TestSupportWriteRoutesRequireStableIdempotency(t *testing.T) {
	raw, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, want := range []string{
		"support.CreateIdempotencyScope",
		"support.UserReplyIdempotencyScope",
		"support.UserCloseIdempotencyScope",
		`Post("/support/tickets", h.createTicket)`,
		`Post("/support/tickets/{id}/reply", h.replyTicket)`,
		`Post("/support/tickets/{id}/close", h.closeTicket)`,
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("public support route contract missing %q", want)
		}
	}
	createAt := strings.Index(source, `Post("/support/tickets", h.createTicket)`)
	if createAt < 0 {
		t.Fatal("create ticket route missing")
	}
	window := source[max(0, createAt-500):createAt]
	rateAt := strings.Index(window, `ByAccount("ticket_create"`)
	idemAt := strings.Index(window, "support.CreateIdempotencyScope")
	if rateAt < 0 || idemAt < 0 || rateAt >= idemAt {
		t.Fatal("ticket creation must apply account rate limiting before idempotency")
	}
}

func TestSupportHandlersCommitPreparedResponses(t *testing.T) {
	raw, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, want := range []string{
		"Support.CreateAtomic(",
		"Support.ReplyAsUserAtomic(",
		"Support.CloseByUserAtomic(",
		"middleware.IdempotencyClaimFrom(r.Context())",
		"httpx.WritePrepared(w, out.PreparedResponse())",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("public support handler contract missing %q", want)
		}
	}
}

// [INPUT]: 依赖 platform/sourcetest 按名取 NewRouter 与用户侧工单处理器的源码
// [OUTPUT]: 对外提供 TestSupportWriteRoutesRequireStableIdempotency、TestSupportHandlersCommitPreparedResponses
// [POS]: api/public 工单写路由先限流后幂等、处理器走原子预制响应
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestSupportWriteRoutesRequireStableIdempotency(t *testing.T) {
	source := sourcetest.Load(t, ".").Decl("NewRouter")
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
	source := sourcetest.Load(t, ".").Decls("handlers.createTicket", "handlers.replyTicket", "handlers.closeTicket")
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

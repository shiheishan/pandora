// [INPUT]: 依赖 platform/sourcetest 按名取工单各写入口的源码与整包源码
// [OUTPUT]: 对外提供 TestSupportAtomicMutationContracts、TestSchedulerEntryDoesNotRequireHTTPClaim
// [POS]: support 工单写入的原子与审计契约：预制响应同事务提交、审计只记字数不记正文、定时升级不依赖 HTTP 认领
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package support

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestSupportAtomicMutationContracts(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	// 工单的全部写入口（带幂等键的 *Atomic 与它们的事务体）
	source := pkg.Decls(
		"Service.CreateAtomic", "Service.ReplyAsUserAtomic", "Service.CloseByUserAtomic",
		"Service.ReplyAsAgentAtomic", "Service.AssignAtomic", "Service.SetStatusAtomic",
		"Service.EscalateOverdueAsAdminAtomic",
		"Service.create", "Service.replyAsUser", "Service.closeByUser", "Service.replyAsAgent",
		"Service.assign", "Service.setStatus", "Service.escalateOverdue")
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
	if all := pkg.Source(); strings.Contains(all, `"body": body`) || strings.Contains(all, `"body": in.Body`) ||
		strings.Contains(all, "body_hash") {
		t.Fatal("support audit evidence must not store reply content or a correlatable body hash")
	}
}

func TestSchedulerEntryDoesNotRequireHTTPClaim(t *testing.T) {
	window := sourcetest.Load(t, ".").Decl("Service.EscalateOverdue")
	if strings.Contains(window, "IdempotencyClaim") {
		t.Fatal("scheduled SLA escalation must remain independent of an HTTP claim")
	}
	if !strings.Contains(window, `"system", nil, "admin", nil`) {
		t.Fatal("scheduled SLA audit must use system actor in the schema-supported admin domain")
	}
}

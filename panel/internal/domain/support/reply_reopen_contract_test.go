package support

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 客服的公开回复会把 closed 工单改回 pending_user，任何把工单从 closed 改走的路径都要
// 清空 closed_reason / closed_note（与 setStatus 同口径），否则门户与解决率统计会认错。
// 行为由 closed_reason_pg18_test.go 在真库上验证，这里在本机也能拦住回退。
func TestAgentReplyClearsCloseReason(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("Service.replyAsAgent")
	set := body[strings.Index(body, "status = 'pending_user'"):]
	for _, want := range []string{"closed_reason = NULL", "closed_note = NULL"} {
		if !strings.Contains(set[:strings.Index(set, "`")], want) {
			t.Fatalf("replyAsAgent 改回 pending_user 时没有 %s", want)
		}
	}
}

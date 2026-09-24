package httpx

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// reauth_required 与 forbidden 同为 403、码不同：前端只凭码决定是否弹重新验证身份。
// 漏登记状态映射的话 Fail 会把它写成 500，前端连错误都认不出。
func TestReauthRequiredIsA403WithItsOwnCode(t *testing.T) {
	if got := statusByCode[CodeReauthRequired]; got != http.StatusForbidden {
		t.Fatalf("reauth_required status=%d want 403", got)
	}
	w := httptest.NewRecorder()
	Fail(w, httptest.NewRequest(http.MethodPost, "/v1/x", nil),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		New(CodeReauthRequired, "此操作需要重新验证身份"))
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"reauth_required"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

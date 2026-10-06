// [INPUT]: 依赖同包 httpx.go 的 statusByCode 与 Fail
// [OUTPUT]: 对外提供 TestReauthRequiredIsA403WithItsOwnCode、TestUpgradeRequiredIsA426
// [POS]: platform/httpx 的错误码状态映射测试：与别的码共用状态或用少见状态的码，各钉一条

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

// upgrade_required 是 426：节点旧 bootstrap 靠它告诉调用方换协议。
// 漏登记状态映射的话 Fail 会把它写成 500，节点会当成面板故障反复重试。
func TestUpgradeRequiredIsA426(t *testing.T) {
	w := httptest.NewRecorder()
	Fail(w, httptest.NewRequest(http.MethodPost, "/v1/x", nil),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		New(CodeUpgradeRequired, "use the new endpoint"))
	if w.Code != http.StatusUpgradeRequired || !strings.Contains(w.Body.String(), `"code":"upgrade_required"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

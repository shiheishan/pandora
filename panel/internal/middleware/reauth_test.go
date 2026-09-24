package middleware

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 重认证门必须回独立的 reauth_required，而不是通用 forbidden。
//
// 前端的「重新验证身份 → 用原 Idempotency-Key 重放」流程只认这个码：
// 回成 forbidden 的话，它与真正的拒绝无从区分，要么什么都弹框、要么什么都不弹。
func TestRequireRecentReauthRejectsWithReauthRequired(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reached := false
	h := RequireRecentReauth(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	}))

	serve := func(p *httpx.Principal) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/guarded", nil)
		if p != nil {
			req = req.WithContext(httpx.WithPrincipal(req.Context(), p))
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}

	for name, p := range map[string]*httpx.Principal{
		"stale reauth": {Kind: "admin", UserID: "u", ReauthedRecently: false},
		"no principal": nil,
	} {
		t.Run(name, func(t *testing.T) {
			reached = false
			w := serve(p)
			if reached {
				t.Fatal("handler reached without recent reauth")
			}
			if w.Code != http.StatusForbidden {
				t.Fatalf("status=%d want 403", w.Code)
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode body %q: %v", w.Body.String(), err)
			}
			if body.Error.Code != "reauth_required" {
				t.Fatalf("code=%q want reauth_required", body.Error.Code)
			}
			if body.Error.Message == "" {
				t.Fatal("message must stay displayable")
			}
		})
	}

	t.Run("recent reauth passes", func(t *testing.T) {
		reached = false
		w := serve(&httpx.Principal{Kind: "admin", UserID: "u", ReauthedRecently: true})
		if !reached || w.Code != http.StatusNoContent {
			t.Fatalf("recently reauthed principal blocked: status=%d", w.Code)
		}
	})
}

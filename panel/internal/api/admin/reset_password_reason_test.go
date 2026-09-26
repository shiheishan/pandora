package admin

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// R101：原因可选，只有超过 500 字才回 422 fields.reason。handlers 不带身份服务，
// 这条 422 必须在碰到它之前返回，否则测试会空指针。
func TestResetPasswordReasonOnlyCapped(t *testing.T) {
	h := &handlers{d: Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	r := chi.NewRouter()
	r.Post("/v1/users/{id}/reset-password", h.resetUserPassword)
	body := `{"new_password":"x","reason":"` + strings.Repeat("长", 501) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/users/u1/reset-password", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"reason":"原因最多 500 字"`) {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

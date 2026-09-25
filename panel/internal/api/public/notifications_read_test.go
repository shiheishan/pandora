package public

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// R84：非 UUID 的 id 在碰数据库之前回 404。handlers 不带连接池，
// 一旦漏过校验走进 InTx 就会空指针，测试同样失败。
func TestMarkNotificationReadRejectsNonUUID(t *testing.T) {
	h := &handlers{d: Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	r := chi.NewRouter()
	r.Post("/v1/me/notifications/{id}/read", h.markNotificationRead)
	for _, id := range []string{"not-a-uuid", "123", "0190a000-0000-7000-8000-00000000c0dz"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/me/notifications/"+id+"/read", nil)
		ctx := httpx.WithPrincipal(context.Background(), &httpx.Principal{Kind: "user", Audience: "public",
			UserID: "0190a000-0000-7000-8000-000000000001", TenantID: "0190a000-0000-7000-8000-000000000002"})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req.WithContext(ctx))
		if w.Code != http.StatusNotFound {
			t.Fatalf("id %q: status=%d body=%s", id, w.Code, w.Body.String())
		}
	}
}

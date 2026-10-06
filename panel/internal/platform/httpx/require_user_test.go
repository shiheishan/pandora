// [INPUT]: 依赖同包 require_user.go 的 RequireUser、context.go 的 WithPrincipal
// [OUTPUT]: 对外提供 TestRequireUserRejectsMissingOrNonUserPrincipal、TestRequireUserReturnsLoggedInUser
// [POS]: platform/httpx 登录检查出口的单测：没有主体、匿名主体、UserID 为空都回 401 unauthorized「需要登录」信封，登录用户原样返回且不写响应

package httpx

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRequireUserRejectsMissingOrNonUserPrincipal(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for name, p := range map[string]*Principal{
		"no principal":   nil,
		"anonymous":      {Kind: "anonymous", TenantID: "t"},
		"empty user id":  {Kind: "user", Audience: "public", TenantID: "t"},
		"node principal": {Kind: "node", TenantID: "t"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := WithRequestID(context.Background(), "req-1")
			if p != nil {
				ctx = WithPrincipal(ctx, p)
			}
			r := httptest.NewRequest(http.MethodGet, "/v1/me", nil).WithContext(ctx)
			w := httptest.NewRecorder()
			got, ok := RequireUser(w, r, log)
			if ok || got != nil {
				t.Fatalf("RequireUser = %v, %v; want nil, false", got, ok)
			}
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			var body errorBody
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body is not the error envelope: %v: %s", err, w.Body.String())
			}
			if body.Error.Code != CodeUnauthorized || body.Error.Message != "需要登录" || body.Error.RequestID != "req-1" {
				t.Fatalf("envelope = %+v", body.Error)
			}
		})
	}
}

func TestRequireUserReturnsLoggedInUser(t *testing.T) {
	want := &Principal{Kind: "user", Audience: "public", TenantID: "t", UserID: "u"}
	r := httptest.NewRequest(http.MethodGet, "/v1/me", nil).WithContext(WithPrincipal(context.Background(), want))
	w := httptest.NewRecorder()
	got, ok := RequireUser(w, r, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !ok || got != want {
		t.Fatalf("RequireUser = %v, %v; want the request principal", got, ok)
	}
	if w.Code != http.StatusOK || w.Body.Len() != 0 || len(w.Header()) != 0 {
		t.Fatalf("RequireUser wrote a response for a logged-in user: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
}

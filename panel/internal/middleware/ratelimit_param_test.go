package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// ByAccountParam 的键是「用户:规范化的路由参数」：同一份订阅换个写法不能换出新额度，
// 乱填的参数共用一个桶，没登录或没有参数时不适用。
func TestByAccountParamKeysOnUserAndCanonicalParam(t *testing.T) {
	l := ByAccountParam("sub_rotate_gap", "id", 10*time.Minute, 1)
	key := func(user, id string) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/v1/me/subscriptions/x/rotate", nil)
		rctx := chi.NewRouteContext()
		if id != "" {
			rctx.URLParams.Add("id", id)
		}
		ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
		if user != "" {
			ctx = httpx.WithPrincipal(ctx, &httpx.Principal{Kind: "user", UserID: user})
		}
		return l.KeyFn(req.WithContext(ctx))
	}
	const sub = "7a1b0000-0000-4000-8000-0000000000aa"
	want := "u1:" + sub
	for _, raw := range []string{sub, "7A1B0000-0000-4000-8000-0000000000AA", "{" + sub + "}", "urn:uuid:" + sub} {
		if got := key("u1", raw); got != want {
			t.Errorf("param %q keyed as %q, want %q", raw, got, want)
		}
	}
	if got := key("u2", sub); got != "u2:"+sub {
		t.Errorf("another user's key = %q", got)
	}
	if a, b := key("u1", "not-a-uuid"), key("u1", "also-not-a-uuid"); a != "u1:invalid" || b != a {
		t.Errorf("unparsable params keyed as %q / %q, want one shared bucket", a, b)
	}
	if got := key("", sub); got != "" {
		t.Errorf("anonymous request keyed as %q, want not applicable", got)
	}
	if got := key("u1", ""); got != "" {
		t.Errorf("route without the param keyed as %q, want not applicable", got)
	}
}

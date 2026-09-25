// [INPUT]: 依赖 platform/pg18test 打开 public_api 域的一次性库（publicAPIFixture），依赖 middleware.Authenticate 的会话校验与 last_seen_at 节流刷新、本包 selfservice.go 的 listMySessions、domain/identity 的 ListActiveSessions、platform/token 的签发
// [OUTPUT]: 对外提供 TestSessionLastSeenPG18
// [POS]: api/public 的 PG18 测试（R62）：带令牌的请求经真实认证中间件刷新本会话的 last_seen_at，5 分钟内不重复写，别的会话不动，GET v1/me/sessions 读得到刷新后的值
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/pg18test"
	"github.com/aegispanel/aegis/internal/platform/token"
)

func TestSessionLastSeenPG18(t *testing.T) {
	ctx, admin, app := pg18test.Open(t, publicAPIFixture)
	const (
		tenant  = "8d000000-0000-4000-8000-000000000001"
		user    = "8d000000-0000-4000-8000-000000000011"
		current = "8d000000-0000-4000-8000-000000000021"
		other   = "8d000000-0000-4000-8000-000000000022"
	)
	for _, row := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants(id,slug,display_name,default_currency) VALUES($1,'last-seen','Last Seen','CNY')`, []any{tenant}},
		{`INSERT INTO users(id,tenant_id,email,display_name,status) VALUES($2,$1,'u@last-seen.invalid','U','active')`, []any{tenant, user}},
		{`INSERT INTO sessions(id,tenant_id,user_id,audience,auth_methods,expires_at,last_seen_at) VALUES
		   ($2,$1,$3,'public',ARRAY['password'],now()+interval '30 days',now()-interval '1 hour'),
		   ($4,$1,$3,'public',ARRAY['password'],now()+interval '30 days',now()-interval '1 hour')`,
			[]any{tenant, current, user, other}},
	} {
		if _, err := admin.Exec(ctx, row.sql, row.args...); err != nil {
			t.Fatalf("seed: %v\nSQL: %s", err, row.sql)
		}
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	issuer := token.NewIssuer("public", []byte("0123456789abcdef0123456789abcdef"), time.Hour)
	raw, err := issuer.Issue(token.Claims{Subject: user, TenantID: tenant, SessionID: current, Kind: "user"})
	if err != nil {
		t.Fatal(err)
	}
	h := &handlers{d: Deps{Pool: app, Log: log,
		Identity: identity.NewService(app, nil, time.Hour, []byte("last-seen-salt"), false)}}
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(httpx.WithTenantID(req.Context(), tenant)))
		})
	})
	r.Use(middleware.Authenticate(app, issuer, log))
	r.Get("/v1/me/sessions", h.listMySessions)

	lastSeen := func(id string) time.Time {
		t.Helper()
		var at time.Time
		if err := admin.QueryRow(ctx, `SELECT last_seen_at FROM sessions WHERE id=$1`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	list := func() map[string]identity.SessionInfo {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v1/me/sessions", nil).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /v1/me/sessions: status=%d body=%s", w.Code, w.Body.String())
		}
		var out struct {
			Sessions []identity.SessionInfo `json:"sessions"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		m := map[string]identity.SessionInfo{}
		for _, s := range out.Sessions {
			m[s.ID] = s
		}
		return m
	}

	otherBefore := lastSeen(other)
	// 一小时没动的会话：这次请求把它刷到现在，列表在同一请求里就读得到
	got := list()
	touched := lastSeen(current)
	if time.Since(touched) > time.Minute || got[current].LastSeenAt == nil ||
		!got[current].LastSeenAt.Equal(touched) || !got[current].Current {
		t.Fatalf("current session last_seen_at=%s listed=%v", touched, got[current].LastSeenAt)
	}
	if !lastSeen(other).Equal(otherBefore) {
		t.Fatalf("another session was touched: %s -> %s", otherBefore, lastSeen(other))
	}
	t.Log("marker=session_last_seen_touched_ok")

	// 节流：5 分钟内再请求不写
	if _, err := admin.Exec(ctx, `UPDATE sessions SET last_seen_at = now()-interval '2 minutes' WHERE id=$1`, current); err != nil {
		t.Fatal(err)
	}
	pinned := lastSeen(current)
	list()
	if !lastSeen(current).Equal(pinned) {
		t.Fatalf("session touched again inside the throttle window: %s -> %s", pinned, lastSeen(current))
	}
	t.Log("marker=session_last_seen_throttled_ok")

	// 吊销的会话 401 且不刷新
	if _, err := admin.Exec(ctx, `UPDATE sessions SET revoked_at = now(), last_seen_at = now()-interval '1 hour' WHERE id=$1`, current); err != nil {
		t.Fatal(err)
	}
	revokedAt := lastSeen(current)
	req := httptest.NewRequest(http.MethodGet, "/v1/me/sessions", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+raw)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized || !lastSeen(current).Equal(revokedAt) {
		t.Fatalf("revoked session: status=%d last_seen_at %s -> %s", w.Code, revokedAt, lastSeen(current))
	}
}

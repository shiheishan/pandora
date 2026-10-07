package public

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 库不可用时（这里连一个没人监听的端口），订阅拉取对外仍是伪装 404，
// 对内留一条带 request_id 的 ERROR，且日志里没有令牌与请求路径。
// 实测里 169 次订阅 404 中有 135 次就是库错误被当成「不存在」吞掉、一行日志都没有。
func TestSubscribeDatabaseErrorIsLoggedButStillDecoy(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=2")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	h := &handlers{d: Deps{Log: log, Subscription: subscription.New(&db.Pool{Pool: pool}, []byte("salt"), nil)}}
	r := chi.NewRouter()
	r.Get("/{prefix}/{token}", h.subscribe)

	// 虚构令牌，低熵、只为在日志里好认
	token := strings.Repeat("leak", 10)
	req := httptest.NewRequest(http.MethodGet, "/a1b2c3d4e5f6/"+token+".yaml", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = httpx.WithTenantID(ctx, "00000000-0000-7000-8000-000000000001")
	ctx = httpx.WithRequestID(ctx, "req-subscribe-dberr")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req.WithContext(ctx))

	if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "Not Found") {
		t.Fatalf("status=%d body=%q, want the decoy 404", w.Code, w.Body.String())
	}
	out := logs.String()
	if !strings.Contains(out, `"level":"ERROR"`) || !strings.Contains(out, `"request_id":"req-subscribe-dberr"`) {
		t.Fatalf("database error must be logged at ERROR with request_id, got %s", out)
	}
	if strings.Contains(out, token) || strings.Contains(out, "a1b2c3d4e5f6") {
		t.Fatalf("log leaks the subscription token or path: %s", out)
	}
}

// 处理器只把 ErrNotFound 当成未认证失败（采样落库），其余错误走 ERROR 日志；
// 成功路径不再在写完响应后另开事务更新凭据（已并进 RecordSuccessfulFetch）。
func TestSubscribeHandlerSeparatesNotFoundFromErrors(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("handlers.subscribe")
	for _, want := range []string{
		"h.d.Subscription.LoadPull(ctx, tenantID, prefix, token)",
		"errors.Is(err, subscription.ErrNotFound)",
		"h.d.Subscription.RecordUnauthenticated(",
		`slog.String("request_id", httpx.RequestIDFrom(ctx))`,
		"h.d.Subscription.RecordSuccessfulFetch(",
		"writeDecoy(w)",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("subscribe handler missing %q", want)
		}
	}
	for _, banned := range []string{`"not_found"`, "TouchCredential", "Authenticate(", "rawToken, \"err\"", "\"token\", token"} {
		if strings.Contains(body, banned) {
			t.Fatalf("subscribe handler must not contain %q", banned)
		}
	}
}

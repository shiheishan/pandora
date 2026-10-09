package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/roundtrip"
)

// AccessLog 是三个网关的访问日志，同时给每个请求挂上往返计数器（platform/roundtrip）。
//
// 每个请求结束时打一行 info「access」：路由模板、状态码、耗时（微秒），以及这个请求
// 让数据库与 Valkey 付出的往返数 db_rt、kv_rt（口径见 roundtrip.Counts.DBTotal 与
// 追踪器的说明）。只记路由模板（如 /{prefix}/{token}），不记原始路径与查询串：
// 订阅链接、回调签名都在路径里。
//
// 挂在 RequestID 之后、其余中间件之前：鉴权的那次库往返、限流的 Valkey 往返都要
// 算进来，日志要带请求 ID。路由模板要在 chi 路由之后才有，所以必须经 r.Use 挂在
// chi 路由器上（不能挂在 platform/server 那一层）。
//
// 开销：每请求一次分配（accessRecord 同时是 context 节点、计数器、请求副本与
// 响应包装），外加一行日志。基准见 accesslog_test.go。
//
// noinline：不让本函数内联进调用方。内联后闭包会在调用方的包里重新编译，那一份里
// r.WithContext 可能不再内联、请求副本落到堆上，每请求多一次分配；只在本包编译一次时，
// 逃逸分析确认副本在栈上（go build -gcflags=-m 可见「new(http.Request) does not escape」）。
//
//go:noinline
func AccessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &accessRecord{ResponseWriter: w}
			rec.scope.Init(r.Context())
			// WithContext 的结果只拷进 rec，不逃逸，编译器把它放在栈上
			rec.req = *r.WithContext(&rec.scope)
			start := time.Now()
			next.ServeHTTP(rec, &rec.req)
			if log == nil || !log.Enabled(r.Context(), slog.LevelInfo) {
				return
			}

			// 路由器在 r 的 context 上放了路由上下文，路由完成后模板就在里面
			route := ""
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				route = rctx.RoutePattern()
			}
			if route == "" {
				route = "-" // 没匹配上任何路由（404 / 405）
			}
			status := rec.status
			if status == 0 {
				status = http.StatusOK
			}
			n := rec.scope.Counter.Snapshot()
			log.LogAttrs(r.Context(), slog.LevelInfo, "access",
				slog.String("route", r.Method+" "+route),
				slog.Int("status", status),
				slog.Int64("dur_us", time.Since(start).Microseconds()),
				slog.Int("db_rt", n.DBTotal()),
				slog.Int("kv_rt", n.KV),
				slog.String("request_id", httpx.RequestIDFrom(r.Context())),
			)
		})
	}
}

// accessRecord 是访问日志的每请求状态，一次分配装下：
//   - scope：挂着往返计数器的 context 节点（下游的 r.Context() 就是它）；
//   - req：换了 context 的请求副本（与 r.WithContext 相同的浅拷贝）；
//   - 响应包装：记下状态码。
type accessRecord struct {
	http.ResponseWriter
	status int
	scope  roundtrip.Scope
	req    http.Request
}

func (w *accessRecord) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *accessRecord) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Flush 实现 http.Flusher：事件流处理函数断言它（底层不支持时静默忽略）。
func (w *accessRecord) Flush() {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap 让 http.NewResponseController 穿透到底层 ResponseWriter（事件流靠它解除写超时）。
func (w *accessRecord) Unwrap() http.ResponseWriter { return w.ResponseWriter }

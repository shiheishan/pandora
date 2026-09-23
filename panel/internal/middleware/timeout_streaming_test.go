package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 长连接端点不能被请求超时切断。
//
// 这个测试存在的原因：节点事件流上线后连接稳定地在 20 秒断开，而两端日志
// 都看不出异常——超时中间件取消 context，handler 只是照常返回。豁免名单
// 当时只有浏览器那条 /v1/events，漏了节点这条。
//
// 所以这里断言的是「名单覆盖了所有流式端点」，而不是某一条路径的写法。
// 以后再加流式端点，忘了改名单的话，这个测试不会自己发现——但至少已有的
// 三条不会在重构中悄悄掉出去。
func TestStreamingPathsExemptFromTimeout(t *testing.T) {
	streaming := []string{
		"/v1/events",                     // public / admin：浏览器端推送
		"/v1/events/tickets",             // 子路径
		"/api/v1/server/UniProxy/stream", // node：节点配置与用户推送
		"/v2/some/future/stream",         // 约定式判断：将来的端点不用改代码
	}
	for _, path := range streaming {
		if !isStreamingPath(path) {
			t.Errorf("%s 应当豁免请求超时，否则长连接会被定期切断", path)
		}
	}

	// 普通接口必须仍然受超时保护——豁免判断写宽了比漏判更难发现，
	// 那会让一个卡住的查询永久占着连接。
	normal := []string{
		"/v1/nodes",
		"/api/v1/server/UniProxy/config",
		"/api/v1/server/UniProxy/user",
		"/v1/events-summary", // 末段不是 events，只是以它开头
		"/v1/streams",        // 同理：streams 不是 stream
		"/v1/nodes/events-count",
	}
	for _, path := range normal {
		if isStreamingPath(path) {
			t.Errorf("%s 不是流式端点，不该跳过超时", path)
		}
	}
}

// 超时中间件对流式请求确实不设 deadline。
//
// 上面那个测试只验证了判断函数。这个走真实的中间件，确认豁免真的接上了：
// 判断函数写对了但中间件没调用它，症状和完全没修一样。
func TestTimeoutMiddlewareLeavesStreamingContextOpen(t *testing.T) {
	const timeout = 50 * time.Millisecond

	check := func(path string, wantDeadline bool) {
		t.Helper()
		var hasDeadline bool
		h := Timeout(timeout)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, hasDeadline = r.Context().Deadline()
		}))
		req := httptest.NewRequest(http.MethodGet, path, nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
		if hasDeadline != wantDeadline {
			t.Errorf("%s: context 有 deadline=%v，期望 %v", path, hasDeadline, wantDeadline)
		}
	}

	check("/api/v1/server/UniProxy/stream", false)
	check("/v1/events", false)
	check("/v1/nodes", true)
}

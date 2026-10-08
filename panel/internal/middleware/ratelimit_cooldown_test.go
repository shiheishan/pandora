package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 冷却式维度的计数键不带时间窗编号（从第一次计数起过期）；普通维度照旧带。
// 带等待提示的维度超限时写明还要等几分钟（脚本回的剩余毫秒扣掉 1 秒余量，向上取整）。
func TestRateLimitCooldownKeysAndRetryHint(t *testing.T) {
	rdb, hook := newScriptedRedis(t, int64(1), int64(2), int64(150_000+1000))
	h := RateLimit(rdb, limitTestLog(),
		Limit{Name: "gap", Window: 10 * time.Minute, Max: 1, KeyFn: fixedKey("u")}.AsCooldown().WithRetryHint(),
		Limit{Name: "fixed", Window: time.Minute, Max: 5, KeyFn: fixedKey("u")},
	)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("handler must not run") }))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/", nil))
	args := hook.calls[0]
	if args[3] != "rl:gap:u" || !strings.HasPrefix(args[4].(string), "rl:fixed:u:") {
		t.Fatalf("keys=%v %v, want a window-free cooldown key then a windowed key", args[3], args[4])
	}
	var body struct {
		Error struct{ Message string } `json:"error"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if rr.Code != http.StatusTooManyRequests || body.Error.Message != "操作太频繁，请 3 分钟后再试" {
		t.Fatalf("status=%d message=%q", rr.Code, body.Error.Message)
	}

	// 不带提示的维度照旧是通用文案；脚本只回两项（旧形状）也接受
	rdb2, _ := newScriptedRedis(t, int64(1), int64(6))
	rr = httptest.NewRecorder()
	RateLimit(rdb2, limitTestLog(), Limit{Name: "a", Window: time.Minute, Max: 5, KeyFn: fixedKey("x")})(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	if rr.Code != http.StatusTooManyRequests || body.Error.Message != "请求过于频繁，请稍后再试" {
		t.Fatalf("plain limit: status=%d message=%q", rr.Code, body.Error.Message)
	}
}

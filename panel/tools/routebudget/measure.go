package routebudget

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// AccessLog 接在被测路由器的 Deps.Log 上，收下访问日志（middleware.AccessLog 打的
// 「access」行），按请求取出往返数。PG18 预算测试用它量真实路由：计数与生产的
// 访问日志同一条路径，日志字段本身也一并验了。
type AccessLog struct {
	mu      sync.Mutex
	entries []Entry
	others  [][]byte
}

// Entry 是一行访问日志里的往返数。
type Entry struct {
	Route   string `json:"route"`
	Status  int    `json:"status"`
	DBRT    int    `json:"db_rt"`
	KV      int    `json:"kv_rt"`
	Ping    int    `json:"db_ping"`
	Prepare int    `json:"db_prepare"`
	Reset   int    `json:"db_reset"`
	// RequestID 是请求头 X-Request-ID（合法 UUID 时网关沿用），用来认出是哪一次请求
	RequestID string `json:"request_id"`
}

// Statements 是语句往返：db_rt 扣掉存活探测、语句准备与非受控归还（预算的口径）。
func (e Entry) Statements() int { return e.DBRT - e.Ping - e.Prepare - e.Reset }

// Logger 返回写进本记录器的 JSON 日志。级别开到 debug：节点网关把成功的快请求降到
// debug（middleware.QuietSuccess），预算测试要逐条拿到。
func (a *AccessLog) Logger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(a, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// Write 实现 io.Writer：slog 的 JSON 处理器一次写一整行。
func (a *AccessLog) Write(p []byte) (int, error) {
	var head struct {
		Msg string `json:"msg"`
	}
	line := bytes.Clone(p)
	a.mu.Lock()
	defer a.mu.Unlock()
	if json.Unmarshal(line, &head) == nil && head.Msg == "access" {
		var e Entry
		if err := json.Unmarshal(line, &e); err == nil {
			a.entries = append(a.entries, e)
			return len(p), nil
		}
	}
	a.others = append(a.others, line)
	return len(p), nil
}

// Mark 返回当前已收的访问日志行数，配合 Since 取之后的那一行。
func (a *AccessLog) Mark() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.entries)
}

// Since 等到 mark 之后出现第一行访问日志并返回它。访问日志在处理函数返回之后才写，
// 走真实 HTTP 服务时客户端可能先拿到响应，这里最多等 2 秒。
func (a *AccessLog) Since(t testing.TB, mark int) Entry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		if len(a.entries) > mark {
			e := a.entries[mark]
			a.mu.Unlock()
			return e
		}
		a.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no access log line after request #%d; other log lines: %s", mark, a.otherLines())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Wait 等到请求 ID 为 id 的那一行访问日志并返回它（最多等 2 秒）。
func (a *AccessLog) Wait(t testing.TB, id string) Entry {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		for i := len(a.entries) - 1; i >= 0; i-- {
			if a.entries[i].RequestID == id {
				e := a.entries[i]
				a.mu.Unlock()
				return e
			}
		}
		a.mu.Unlock()
		if time.Now().After(deadline) {
			t.Fatalf("no access log line for request %s; other log lines: %s", id, a.otherLines())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// Range 等到 from 起的 n 行访问日志都到齐并返回它们。
func (a *AccessLog) Range(t testing.TB, from, n int) []Entry {
	t.Helper()
	if n > 0 {
		a.Since(t, from+n-1)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Entry(nil), a.entries[from:from+n]...)
}

func (a *AccessLog) otherLines() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return string(bytes.Join(a.others, nil))
}

// Budget 收集一个网关的逐路由实测值，最后与登记表比对。
type Budget struct {
	t        testing.TB
	gateway  string
	routes   map[string]Route
	measured map[string]Entry
	pool     ResetCounter
	resets0  int64
}

// ResetCounter 是 platform/db.Pool 的 SessionResets：池里真正执行过的会话清理条数。
type ResetCounter interface{ SessionResets() int64 }

// NewBudget 读登记表，准备比对某个网关。pool 是被测路由器用的连接池：从这里到 Verify，
// 池里一条会话清理语句都不许执行（受控路径归还的连接不清理，见 platform/db 的 afterRelease）。
func NewBudget(t testing.TB, gateway string, pool ResetCounter) *Budget {
	t.Helper()
	all, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	b := &Budget{t: t, gateway: gateway, routes: map[string]Route{}, measured: map[string]Entry{},
		pool: pool, resets0: pool.SessionResets()}
	for _, r := range all {
		if r.Gateway == gateway {
			b.routes[r.Key()] = r
		}
	}
	return b
}

// Measure 量一条路由：do 发一次请求（请求头 X-Request-ID 设成给它的 id）并返回状态码。
// 先发 warm 次把各连接上的语句缓存热起来（稳态不含准备），再量 3 次，按请求 ID 找到
// 各自的访问日志行。3 次的语句往返与 Valkey 往返必须一致，状态码要在 ok 里，非受控
// 归还必须为 0。route 是访问日志里的「方法 路由模板」。状态码不对时记错误、跳过这条，
// 一次运行把所有路由的问题都报出来。
func (b *Budget) Measure(log *AccessLog, route string, warm int, ok []int, do func(requestID string) int) {
	t := b.t
	t.Helper()
	good := func(code int, phase string) bool {
		t.Helper()
		if slices.Contains(ok, code) {
			return true
		}
		t.Errorf("%s %s: status %d, want one of %v", route, phase, code, ok)
		return false
	}
	for i := 0; i < warm; i++ {
		if !good(do(uuid.NewString()), "warm-up") {
			return
		}
	}
	var first Entry
	for i := 0; i < 3; i++ {
		id := uuid.NewString()
		if !good(do(id), "measured") {
			return
		}
		e := log.Wait(t, id)
		if e.Route != route {
			t.Errorf("access log route = %q, want %q", e.Route, route)
			return
		}
		if e.Reset != 0 {
			t.Errorf("%s: %d connection(s) released outside platform/db's scoped paths (each costs a session reset statement)",
				route, e.Reset)
		}
		if i == 0 {
			first = e
		} else if e.Statements() != first.Statements() || e.KV != first.KV {
			t.Errorf("%s is not steady: run 1 = %d db / %d kv, run %d = %d db / %d kv",
				route, first.Statements(), first.KV, i+1, e.Statements(), e.KV)
		}
	}
	b.measured[route] = first
	t.Logf("%s：库语句往返 %d，Valkey %d（状态 %d）", route, first.Statements(), first.KV, first.Status)
}

// Verify 与登记表比对：量过的路由预算必须相等（多了超预算，少了要把表改小），
// 表上填了预算的本网关路由必须都量过。
func (b *Budget) Verify() {
	t := b.t
	t.Helper()
	// 会话清理在归还协程里跑，稍等它落地再数
	time.Sleep(200 * time.Millisecond)
	if n := b.pool.SessionResets() - b.resets0; n != 0 {
		t.Errorf("%d session reset statement(s) ran on the pool while measuring %s routes "+
			"(a connection was released outside platform/db's scoped paths)", n, b.gateway)
	}
	for _, problem := range compareBudgets(b.gateway, b.routes, b.measured) {
		t.Error(problem)
	}
}

// compareBudgets 是 Verify 的比对本身（不碰测试框架，单测直接验）。按路由排序返回问题。
func compareBudgets(gateway string, routes map[string]Route, measured map[string]Entry) []string {
	var out []string
	keys := make([]string, 0, len(routes)+len(measured))
	for k := range routes {
		keys = append(keys, k)
	}
	for k := range measured {
		if _, ok := routes[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, key := range keys {
		r, registered := routes[key]
		e, ok := measured[key]
		switch {
		case !ok:
			if r.Budgeted {
				out = append(out, fmt.Sprintf("routes.txt:%d %s %s has a budget but no PG18 case measures it", r.Line, gateway, key))
			}
			continue
		case !registered:
			out = append(out, fmt.Sprintf("%s %s is measured but not registered in tools/routebudget/routes.txt", gateway, key))
			continue
		}
		got := Route{Gateway: r.Gateway, Method: r.Method, Pattern: r.Pattern, Budgeted: true, DB: e.Statements(), KV: e.KV}
		switch {
		case !r.Budgeted:
			out = append(out, fmt.Sprintf("routes.txt:%d has no budget yet; measured:\n%s", r.Line, Format(got)))
		case got.DB > r.DB || got.KV > r.KV:
			out = append(out, fmt.Sprintf("routes.txt:%d over budget: %s %s budget %d db / %d kv, measured %d db / %d kv",
				r.Line, gateway, key, r.DB, r.KV, got.DB, got.KV))
		case got.DB < r.DB || got.KV < r.KV:
			out = append(out, fmt.Sprintf("routes.txt:%d improved (budgets only go down): lower the row to\n%s", r.Line, Format(got)))
		}
	}
	return out
}

// Package roundtrip 按请求数数据库与 Valkey 的网络往返（往返记账器）。
//
// 每个 HTTP 请求在访问日志中间件里挂上一个 Counter（经 context 传递）；
// platform/db 的 pgx 追踪器与本包的 Valkey 钩子按 ctx 找到它、逐次加一。
// 后台循环、启动阶段这类不带计数器的 ctx 什么都不记。
//
// 它有两个用途：
//   - 访问日志的 db_rt、kv_rt 两个字段，生产常开，复测时按路由出分布；
//   - PG18 测试逐路由断言往返预算（panel/tools/routebudget 的登记表），
//     换栈每一步与每一路优化都靠它判断「有没有多出往返」。
//
// 记在谁头上：按发起往返的 ctx。进程内缓存的单飞合并读（同键并发未命中只读一次库）
// 由发起者用 context.WithoutCancel 跑加载，WithoutCancel 保留 context 里的值，所以那次
// 加载记在发起者头上；同时等结果的请求没碰库，记 0。逐路由预算串行发请求、量稳态，
// 不受这种并发差异影响；访问日志里同一路由在并发下 db_rt 可能 0 / N 不一。
//
// 开销：计数是几次原子加；Counter 与 context 节点合在一个 Scope 里，
// 挂计数器的那一层每请求只多一次分配（见 middleware 的访问日志）。
package roundtrip

import (
	"context"
	"sync/atomic"
)

// Counter 是一个请求的往返计数。并发安全：请求里的并行查询各自加。
type Counter struct {
	db      atomic.Int32
	prepare atomic.Int32
	ping    atomic.Int32
	reset   atomic.Int32
	kv      atomic.Int32
}

// Counts 是 Counter 某一刻的快照。
type Counts struct {
	// DB 是语句往返：一条语句、一个管线批次（含 BEGIN、COMMIT）各算 1，COPY 算 2。
	DB int
	// Prepare 是语句准备的往返：某条 SQL 第一次在某条连接上执行时多出的一次。
	// pgx 在批次里顺带准备的语句没有钩子，不在这里（只发生在连接上第一次见到它时）。
	Prepare int
	// Ping 是取连接时的存活探测（连接空闲超过 1 秒时 pgxpool 先探一次）。
	Ping int
	// Reset 是非受控归还：连接没经 platform/db 的受控路径归还，归还后连接池
	// 要多跑一条会话清理语句（在归还协程里，不在请求的关键路径上，但库要多执行一次）。
	Reset int
	// KV 是 Valkey 往返：一条命令或一个管线各算 1。
	KV int
}

// DBTotal 是这个请求让数据库付出的全部往返：语句、准备、探测与归还清理。
func (c Counts) DBTotal() int { return c.DB + c.Prepare + c.Ping + c.Reset }

// AddDB 记 n 次语句往返。
func (c *Counter) AddDB(n int32) { c.db.Add(n) }

// AddPrepare 记一次语句准备。
func (c *Counter) AddPrepare() { c.prepare.Add(1) }

// AddPing 记一次取连接时的存活探测。
func (c *Counter) AddPing() { c.ping.Add(1) }

// AddReset 记一次非受控归还。
func (c *Counter) AddReset() { c.reset.Add(1) }

// AddKV 记一次 Valkey 往返。
func (c *Counter) AddKV() { c.kv.Add(1) }

// Snapshot 取当前计数。
func (c *Counter) Snapshot() Counts {
	return Counts{DB: int(c.db.Load()), Prepare: int(c.prepare.Load()), Ping: int(c.ping.Load()),
		Reset: int(c.reset.Load()), KV: int(c.kv.Load())}
}

type ctxKey struct{}

// Scope 是挂着计数器的 context 节点：context 节点与计数器在同一次分配里。
// 把 *Scope 当 context 往下传，From 就能找到 &Scope.Counter。
//
// 零值不可用：先设 Context 为父 context（Init 或直接赋值）。可以内嵌进调用方
// 自己的每请求结构体，与别的每请求状态共用一次分配（见 middleware 的访问日志）。
type Scope struct {
	context.Context
	Counter Counter
}

// Init 把父 context 接上。
func (s *Scope) Init(parent context.Context) { s.Context = parent }

// Value 对计数器的键返回本节点的计数器，其余交给父 context。
func (s *Scope) Value(key any) any {
	if key == (ctxKey{}) {
		return &s.Counter
	}
	return s.Context.Value(key)
}

// WithCounter 返回挂着 c 的子 context。只在测试与非 HTTP 入口用；HTTP 请求由
// 访问日志中间件经 Scope 挂上。
func WithCounter(parent context.Context, c *Counter) context.Context {
	return context.WithValue(parent, ctxKey{}, c)
}

// From 取 ctx 上的计数器；没有挂时返回 nil。
func From(ctx context.Context) *Counter {
	c, _ := ctx.Value(ctxKey{}).(*Counter)
	return c
}

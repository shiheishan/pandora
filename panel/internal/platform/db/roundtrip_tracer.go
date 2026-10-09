package db

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/roundtrip"
)

// roundTripTracer 是 pgx 的追踪器：把每次网络往返记到 ctx 上的往返计数器
// （platform/roundtrip，访问日志中间件给每个请求挂一个）。ctx 上没有计数器的
// 调用（后台循环、启动、归还钩子）一律不记，只多一次 context 查找。
//
// 口径：
//   - 一条语句（Exec / Query / QueryRow，含 BEGIN、COMMIT 与「BEGIN + 注入」的
//     多语句简单协议）记 1；一个管线批次（QueryRowScoped / BatchScoped）整批记 1；
//     COPY FROM 记 2（先等 CopyInResponse，再等 CommandComplete）。
//   - 语句准备另记（Prepare）：某条 SQL 第一次在某条连接上走扩展协议时多出的那次。
//     pgx 在批次里顺带准备语句没有钩子，那一次不记——只在连接上第一次见到那条
//     SQL 时发生，连接寿命 2–4 小时，稳态为零；PG18 的预算也只比语句往返。
//   - 取连接时的存活探测另记（Ping，见 shouldPingCounted）。
//   - 非受控归还另记（Reset）：连接不经 releaseScoped 归还，afterRelease 会多跑
//     一条 sessionResetSQL。在 TraceRelease（同步、早于归还协程）按连接上的
//     受控标记判定，记到取这条连接的那个请求头上。
//   - 失败路径上 rollbackForCleanup 用独立的 ctx 回滚，那一次不记。
//
// pgxpool 从 ConnConfig.Tracer 上按类型断言取 AcquireTracer / ReleaseTracer，
// 所以一个值同时实现全部接口。
//
// 另有一个池级计数：真正执行了的 sessionResetSQL 条数（Pool.SessionResets），不管 ctx。
// 受控路径归还的连接上一条都不该有；测试据此断言，换栈接 database/sql 时也靠它核对。
type roundTripTracer struct {
	resets atomic.Int64
}

// rtOwnerKey 存在连接的 CustomData 里：取连接的请求的计数器，归还时取走。
const rtOwnerKey = "aegis.db.rt_owner"

func (t *roundTripTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if data.SQL == sessionResetSQL {
		t.resets.Add(1)
	}
	if c := roundtrip.From(ctx); c != nil {
		c.AddDB(1)
	}
	return ctx
}

func (*roundTripTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (*roundTripTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	if c := roundtrip.From(ctx); c != nil {
		c.AddDB(1)
	}
	return ctx
}

func (*roundTripTracer) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {}

func (*roundTripTracer) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

func (*roundTripTracer) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceCopyFromStartData) context.Context {
	if c := roundtrip.From(ctx); c != nil {
		c.AddDB(2)
	}
	return ctx
}

func (*roundTripTracer) TraceCopyFromEnd(context.Context, *pgx.Conn, pgx.TraceCopyFromEndData) {}

func (*roundTripTracer) TracePrepareStart(ctx context.Context, _ *pgx.Conn, _ pgx.TracePrepareStartData) context.Context {
	return ctx
}

// TracePrepareEnd 只在真的发了 Parse/Describe 时记（已准备过的直接返回，不算往返）。
func (*roundTripTracer) TracePrepareEnd(ctx context.Context, _ *pgx.Conn, data pgx.TracePrepareEndData) {
	if data.AlreadyPrepared {
		return
	}
	if c := roundtrip.From(ctx); c != nil {
		c.AddPrepare()
	}
}

func (*roundTripTracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	return ctx
}

// TraceAcquireEnd 把请求的计数器挂到连接上，归还时据此判定非受控归还记给谁。
func (*roundTripTracer) TraceAcquireEnd(ctx context.Context, _ *pgxpool.Pool, data pgxpool.TraceAcquireEndData) {
	if data.Conn == nil {
		return
	}
	if c := roundtrip.From(ctx); c != nil {
		data.Conn.PgConn().CustomData()[rtOwnerKey] = c
	}
}

// TraceRelease 在 pgxpool 归还的开头同步调用（早于 afterRelease 所在的协程）。
// 连接回到空闲、却没有受控标记的，归还后会多跑一条 sessionResetSQL（见 afterRelease）；
// 非空闲的连接 pgxpool 直接销毁，不清理也不记；恰好到寿命被销毁的空闲连接会多记一次
// （每条连接一生至多一次）。
func (*roundTripTracer) TraceRelease(_ *pgxpool.Pool, data pgxpool.TraceReleaseData) {
	if data.Conn == nil {
		return
	}
	pc := data.Conn.PgConn()
	custom := pc.CustomData()
	owner, ok := custom[rtOwnerKey].(*roundtrip.Counter)
	if !ok {
		return
	}
	delete(custom, rtOwnerKey)
	if custom[scopedReleaseKey] != true && !pc.IsClosed() && !pc.IsBusy() && pc.TxStatus() == 'I' {
		owner.AddReset()
	}
}

// SessionResets 是这个连接池开张以来真正执行过的会话清理条数（sessionResetSQL）：
// 每一条都对应一次非受控归还。
func (p *Pool) SessionResets() int64 {
	if p.rt == nil {
		return 0
	}
	return p.rt.resets.Load()
}

// pgxpoolPingIdle 与 pgxpool 缺省的 ShouldPing 同一个阈值：连接空闲超过它，
// 取出时先探一次存活。
const pgxpoolPingIdle = time.Second

// shouldPingCounted 是 pgxpool 缺省的 ShouldPing（空闲超过 1 秒就探），多做一件事：
// 探的那一次往返记到请求的计数器上。
func shouldPingCounted(ctx context.Context, p pgxpool.ShouldPingParams) bool {
	if p.IdleDuration <= pgxpoolPingIdle {
		return false
	}
	if c := roundtrip.From(ctx); c != nil {
		c.AddPing()
	}
	return true
}

package db

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/platform/roundtrip"
)

// 追踪器的计数口径（连库的端到端核对在 api/node 的 PG18 用例里，与线上 Sync 计数对照）。
func TestRoundTripTracerCounts(t *testing.T) {
	var c roundtrip.Counter
	ctx := roundtrip.WithCounter(context.Background(), &c)
	tr := &roundTripTracer{}

	tr.TraceQueryEnd(tr.TraceQueryStart(ctx, nil, pgx.TraceQueryStartData{SQL: "SELECT 1"}), nil, pgx.TraceQueryEndData{})
	bctx := tr.TraceBatchStart(ctx, nil, pgx.TraceBatchStartData{})
	tr.TraceBatchQuery(bctx, nil, pgx.TraceBatchQueryData{})
	tr.TraceBatchQuery(bctx, nil, pgx.TraceBatchQueryData{})
	tr.TraceBatchEnd(bctx, nil, pgx.TraceBatchEndData{})
	tr.TraceCopyFromEnd(tr.TraceCopyFromStart(ctx, nil, pgx.TraceCopyFromStartData{}), nil, pgx.TraceCopyFromEndData{})
	tr.TracePrepareEnd(tr.TracePrepareStart(ctx, nil, pgx.TracePrepareStartData{}), nil, pgx.TracePrepareEndData{})
	tr.TracePrepareEnd(ctx, nil, pgx.TracePrepareEndData{AlreadyPrepared: true})
	if !shouldPingCounted(ctx, pgxpool.ShouldPingParams{IdleDuration: 2 * time.Second}) {
		t.Fatal("idle over a second must still be pinged (pgxpool default)")
	}
	if shouldPingCounted(ctx, pgxpool.ShouldPingParams{IdleDuration: time.Second}) {
		t.Fatal("idle up to a second must not be pinged (pgxpool default)")
	}
	// 没有计数器的 ctx（后台循环、归还钩子）什么都不记，也照常放行探测
	bg := context.Background()
	tr.TraceQueryStart(bg, nil, pgx.TraceQueryStartData{})
	tr.TraceBatchStart(bg, nil, pgx.TraceBatchStartData{})
	if !shouldPingCounted(bg, pgxpool.ShouldPingParams{IdleDuration: time.Minute}) {
		t.Fatal("ping decision must not depend on the counter")
	}
	tr.TraceAcquireEnd(ctx, nil, pgxpool.TraceAcquireEndData{})
	tr.TraceRelease(nil, pgxpool.TraceReleaseData{})

	// 归还钩子的会话清理在池级计数，不论 ctx 上有没有计数器
	tr.TraceQueryStart(bg, nil, pgx.TraceQueryStartData{SQL: sessionResetSQL})
	if p := (&Pool{rt: tr}); p.SessionResets() != 1 || (&Pool{}).SessionResets() != 0 {
		t.Fatalf("session resets = %d, want 1", p.SessionResets())
	}

	// 语句 1 + 批次 1 + COPY 2；准备 1（已准备的不算）；探测 1
	want := roundtrip.Counts{DB: 4, Prepare: 1, Ping: 1}
	if got := c.Snapshot(); got != want {
		t.Fatalf("counts = %+v, want %+v", got, want)
	}
}

// 追踪器挂在全部连接上、生产常开：每次语句的代价是一次 context 查找加一次原子加，零分配。
func BenchmarkRoundTripTracerQuery(b *testing.B) {
	var s roundtrip.Scope
	s.Init(context.Background())
	var ctx context.Context = &s
	type k struct{}
	for i := 0; i < 8; i++ {
		ctx = context.WithValue(ctx, k{}, i)
	}
	tr := &roundTripTracer{}
	data := pgx.TraceQueryStartData{SQL: "SELECT 1"}
	b.ReportAllocs()
	for b.Loop() {
		qctx := tr.TraceQueryStart(ctx, nil, data)
		tr.TraceQueryEnd(qctx, nil, pgx.TraceQueryEndData{})
	}
}

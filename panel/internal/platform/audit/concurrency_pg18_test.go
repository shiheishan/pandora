package audit

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// 并发写审计（w9audit）：下单、续费、换套餐都在 SERIALIZABLE 事务里写审计。
// 序列化事务的快照在第一条语句就定了，早于取链尾；并发时读到的链尾是旧的，
// 写进去就撞 (tenant_id, chain_seq) 唯一约束回 500。这里同租户一起压：
// 序列化写全部成功（或经 40001 重试成功），链完整、chain_seq 连续，并打出
// 不争用与争用下的耗时分位，供报告对照。
func TestAuditConcurrentSerializablePG18(t *testing.T) {
	ctx, admin, _ := openAuditPG18(t)
	// pg18test 给的运行时池只有 8 条连接，压不出 16 路以上的并发；同一个已核过
	// 身份的 DSN 另开一个大池
	pool, err := db.OpenWithOptions(ctx, os.Getenv("AEGIS_AUDIT_PG18_DSN"), db.Options{MaxConns: 40})
	if err != nil {
		t.Fatalf("open wide aegis_app pool: %v", err)
	}
	t.Cleanup(pool.Close)

	const (
		baseTenant  = "85000000-0000-4000-8000-000000000004" // 不争用的基线
		burstTenant = "85000000-0000-4000-8000-000000000003" // 新租户一上来就并发
	)
	auditSeedTenant(t, ctx, admin, baseTenant, "audit-baseline-pg18")
	auditSeedTenant(t, ctx, admin, burstTenant, "audit-concurrency-pg18")

	// 序列化写：先做一段「业务」（3ms），再写审计，与下单的形状一致
	serial := func(tenant, action string) call {
		start, attempts := time.Now(), 0
		err := pool.InTxSerializableRetry(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
			attempts++
			if _, err := tx.Exec(ctx, `SELECT pg_sleep(0.003)`); err != nil {
				return err
			}
			return Write(ctx, tx, tenant, Entry{ActorKind: "system", Action: action})
		})
		return call{serial: true, dur: time.Since(start), attempts: attempts, err: err}
	}
	// 读已提交的写：登录、回调这类路径
	readCommitted := func(tenant, action string) call {
		start := time.Now()
		err := pool.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) error {
			return Write(ctx, tx, tenant, Entry{ActorKind: "system", Action: action})
		})
		return call{dur: time.Since(start), attempts: 1, err: err}
	}

	// 基线：同一租户顺序写 16 笔，没有任何争用
	var base []call
	for i := range 16 {
		base = append(base, serial(baseTenant, fmt.Sprintf("baseline.%d", i)))
	}
	logCalls(t, "baseline 16 sequential serializable", base)
	assertChain(t, ctx, pool, admin, baseTenant, 16)

	// 新租户一上来就 16 路并发序列化写（链头还不存在）
	burst := together(16, 1, func(g, r int) call { return serial(burstTenant, fmt.Sprintf("burst.%d", g)) })
	logCalls(t, "burst 16 concurrent serializable (fresh tenant)", burst)

	// 混合：16 路序列化 + 8 路读已提交，各 3 轮
	mixed := together(24, 3, func(g, r int) call {
		if g < 16 {
			return serial(burstTenant, fmt.Sprintf("mixed.s%d.%d", g, r))
		}
		return readCommitted(burstTenant, fmt.Sprintf("mixed.rc%d.%d", g, r))
	})
	logCalls(t, "mixed 16 serializable + 8 read-committed x3", mixed)
	assertChain(t, ctx, pool, admin, burstTenant, 16+24*3)

	var advisory int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM pg_locks
		WHERE locktype='advisory' AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`).Scan(&advisory); err != nil || advisory != 0 {
		t.Errorf("advisory locks left behind=%d err=%v", advisory, err)
	}
}

type call struct {
	serial   bool
	dur      time.Duration
	attempts int
	err      error
}

// together 让 n 个 goroutine 同时起跑，每个顺序做 rounds 次。
func together(n, rounds int, do func(g, r int) call) []call {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		out  []call
		gate = make(chan struct{})
	)
	for g := range n {
		wg.Go(func() {
			<-gate
			for r := range rounds {
				c := do(g, r)
				mu.Lock()
				out = append(out, c)
				mu.Unlock()
			}
		})
	}
	close(gate)
	wg.Wait()
	return out
}

// logCalls 打出耗时分位与重试次数；任何一笔失败都按 SQLSTATE 归类后判红。
func logCalls(t *testing.T, name string, calls []call) {
	t.Helper()
	for _, serial := range []bool{true, false} {
		var durs []time.Duration
		retried, maxAttempts := 0, 0
		for _, c := range calls {
			if c.serial != serial || c.err != nil {
				continue
			}
			durs = append(durs, c.dur)
			if c.attempts > 1 {
				retried++
			}
			maxAttempts = max(maxAttempts, c.attempts)
		}
		if len(durs) == 0 {
			continue
		}
		slices.Sort(durs)
		kind := "serializable"
		if !serial {
			kind = "read-committed"
		}
		t.Logf("%s [%s] ok=%d p50=%s p99=%s max=%s retried=%d max_attempts=%d", name, kind,
			len(durs), pct(durs, 50), pct(durs, 99), durs[len(durs)-1], retried, maxAttempts)
	}
	byCode := map[string]int{}
	var samples []string
	for _, c := range calls {
		if c.err == nil {
			continue
		}
		code := "go"
		var pe *pgconn.PgError
		if errors.As(c.err, &pe) {
			code = pe.Code + "/" + pe.ConstraintName
		}
		byCode[code]++
		if len(samples) < 3 {
			samples = append(samples, c.err.Error())
		}
	}
	if len(byCode) > 0 {
		keys := make([]string, 0, len(byCode))
		for k, v := range byCode {
			keys = append(keys, fmt.Sprintf("%s×%d", k, v))
		}
		sort.Strings(keys)
		t.Errorf("%s: failures %s; e.g. %s", name, strings.Join(keys, " "), strings.Join(samples, " | "))
	}
}

func pct(sorted []time.Duration, p int) time.Duration {
	i := (len(sorted)*p+99)/100 - 1
	return sorted[max(i, 0)]
}

// assertChain：运行时角色在 RLS 下校验整链完整，chain_seq 从 1 起连续无重复。
func assertChain(t *testing.T, ctx context.Context, app *db.Pool, admin interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, tenant string, want int) {
	t.Helper()
	var report ChainReport
	if err := app.InTx(ctx, db.Scope{TenantID: tenant}, func(tx pgx.Tx) (err error) {
		report, err = VerifyChain(ctx, tx, tenant)
		return err
	}); err != nil {
		t.Fatalf("verify chain: %v", err)
	}
	if report != (ChainReport{Rows: want}) {
		t.Errorf("tenant %s chain report=%+v, want %d intact rows", tenant, report, want)
	}
	var n, lo, hi, distinct int
	if err := admin.QueryRow(ctx, `SELECT count(*), coalesce(min(chain_seq),0), coalesce(max(chain_seq),0),
		count(DISTINCT chain_seq) FROM audit_events WHERE tenant_id=$1`, tenant).Scan(&n, &lo, &hi, &distinct); err != nil {
		t.Fatal(err)
	}
	if n != want || lo != 1 || hi != want || distinct != want {
		t.Errorf("tenant %s chain_seq count=%d min=%d max=%d distinct=%d, want 1..%d", tenant, n, lo, hi, distinct, want)
	}
}

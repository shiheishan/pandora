// [INPUT]: 依赖 domain/nodefabric 的 PublishConfig / ReportConfigApplied，依赖 pgxpool 与 pg_stat_activity 观测锁等待，依赖 node_config_legacy_pg18_test.go 的夹具与共用断言
// [OUTPUT]: 包内提供持锁者 nodeConfigPG18LockHolder / beginNodeConfigPG18LockHolder、等待与取消观测（waitNodeConfigPG18BlockedPID、awaitNodeConfigPG18Cancellation、assertNodeConfigPG18WaiterClean）、按 application_name 开池的 openNodeConfigPG18NamedPool，以及 runNodeConfigPG18CancellationRollbackBatch
// [POS]: TestNodeConfigLegacyPG18 的取消回滚批次（发布在咨询锁与期望配置锁上等待时取消、上报等待时取消），也是本组造锁等待的工具库，被 _lifecycle / _pool_delete / _materialize / _lock 复用
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type nodeConfigPG18LockHolder struct {
	conn     *pgxpool.Conn
	tx       pgx.Tx
	pid      int
	released bool
}

func beginNodeConfigPG18LockHolder(t *testing.T, ctx context.Context,
	appPool *platformdb.Pool, fx nodeConfigPG18Fixture) *nodeConfigPG18LockHolder {
	t.Helper()
	conn, err := appPool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire cancellation lock holder: %v", err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		t.Fatalf("begin cancellation lock holder: %v", err)
	}
	holder := &nodeConfigPG18LockHolder{conn: conn, tx: tx}
	ok := false
	defer func() {
		if !ok {
			holder.cleanup()
		}
	}()
	if _, err := tx.Exec(ctx,
		`SELECT set_config('app.tenant_id',$1,true),
		        set_config('app.actor_id',$2,true)`, fx.tenant, fx.actor); err != nil {
		t.Fatalf("scope cancellation lock holder: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holder.pid); err != nil {
		t.Fatalf("read cancellation lock holder PID: %v", err)
	}
	ok = true
	return holder
}

func (h *nodeConfigPG18LockHolder) cleanup() {
	if h == nil || h.released {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = h.tx.Rollback(ctx)
	h.conn.Release()
	h.released = true
}

func (h *nodeConfigPG18LockHolder) release(t *testing.T) {
	t.Helper()
	if h == nil || h.released {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	err := h.tx.Rollback(ctx)
	cancel()
	h.conn.Release()
	h.released = true
	if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("release cancellation lock holder: %v", err)
	}
}

func (h *nodeConfigPG18LockHolder) commit(t *testing.T) {
	t.Helper()
	if h == nil || h.released {
		t.Fatal("lock holder is already released")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := h.tx.Commit(ctx)
	h.conn.Release()
	h.released = true
	if err != nil {
		t.Fatalf("commit lock holder: %v", err)
	}
}

func waitNodeConfigPG18BlockedPID(t *testing.T, ctx context.Context,
	admin *pgx.Conn, holderPID int, queryLike string) int {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var pid int
		if err := admin.QueryRow(ctx, `SELECT coalesce((
			SELECT a.pid
			  FROM pg_catalog.pg_stat_activity AS a
			 WHERE a.datname=current_database()
			   AND a.usename='aegis_app'
			   -- 不看 state：等锁时 pg_stat_activity 的 state / query /
			   -- wait_event 不是原子快照，state 可能还停在上一条语句。
			   AND a.wait_event_type='Lock'
			   AND a.query LIKE $2
			   AND $1 = ANY(pg_catalog.pg_blocking_pids(a.pid))
			 ORDER BY a.query_start,a.pid
			 LIMIT 1
		),0)`, holderPID, queryLike).Scan(&pid); err != nil {
			t.Fatalf("observe blocked PostgreSQL operation: %v", err)
		}
		if pid != 0 {
			return pid
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("PostgreSQL lock wait matching %q was not observed;%s",
				queryLike, nodeConfigPG18BlockedReport(t, ctx, admin, holderPID))
		case <-ctx.Done():
			t.Fatalf("context ended while observing PostgreSQL lock wait: %v", ctx.Err())
		}
	}
}

// nodeConfigPG18BlockedReport 列出当前被 holderPID 挡住的所有连接。
// 观察超时的时候，光说「没等到」没法判断是根本没人等锁、还是等锁的人
// 卡在另一条语句上，这份报告直接给出真实的 state / wait / query。
func nodeConfigPG18BlockedReport(t *testing.T, ctx context.Context,
	admin *pgx.Conn, holderPID int) string {
	t.Helper()
	rows, err := admin.Query(ctx, `
		SELECT a.pid,coalesce(a.application_name,''),a.state,
		       coalesce(a.wait_event_type,''),coalesce(a.wait_event,''),
		       coalesce(left(regexp_replace(a.query,'\s+',' ','g'),140),''),
		       pg_catalog.pg_blocking_pids(a.pid)
		  FROM pg_catalog.pg_stat_activity AS a
		 WHERE a.datname=current_database() AND a.pid<>pg_backend_pid()
		 ORDER BY a.pid`)
	if err != nil {
		return fmt.Sprintf("\n  <诊断查询失败: %v>", err)
	}
	defer rows.Close()
	var report strings.Builder
	for rows.Next() {
		var pid int
		var name, state, waitType, waitEvent, query string
		var blockers []int32
		if err := rows.Scan(&pid, &name, &state, &waitType, &waitEvent,
			&query, &blockers); err != nil {
			return report.String() + fmt.Sprintf("\n  <扫描失败: %v>", err)
		}
		mark := " "
		for _, blocker := range blockers {
			if int(blocker) == holderPID {
				mark = "*"
			}
		}
		fmt.Fprintf(&report, "\n %s pid=%d app=%q state=%s wait=%s/%s blockers=%v q=%s",
			mark, pid, name, state, waitType, waitEvent, blockers, query)
	}
	if report.Len() == 0 {
		return "\n  <库里没有其它连接>"
	}
	return "  （* = 被持锁方 " + fmt.Sprint(holderPID) + " 挡住）" + report.String()
}

func awaitNodeConfigPG18Cancellation(t *testing.T, callCtx context.Context, result <-chan error) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	select {
	case err := <-result:
		if callCtx.Err() != context.Canceled {
			t.Fatalf("call context=%v, want canceled", callCtx.Err())
		}
		var he *httpx.Error
		if errors.As(err, &he) {
			t.Fatalf("cancellation was converted to http error: %v", err)
		}
		if errors.Is(err, context.Canceled) {
			return
		}
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "57014" {
			return
		}
		t.Fatalf("canceled operation returned %v", err)
	case <-deadline.C:
		t.Fatal("canceled operation did not return while its blocker remained held")
	}
}

func assertNodeConfigPG18WaiterClean(t *testing.T, ctx context.Context,
	admin *pgx.Conn, pid int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var residue int
		if err := admin.QueryRow(ctx, `SELECT count(*)
			FROM pg_catalog.pg_stat_activity AS a
			WHERE a.pid=$1 AND (
				a.xact_start IS NOT NULL
				OR a.wait_event_type='Lock'
				OR EXISTS (
					SELECT 1 FROM pg_catalog.pg_locks AS l
					WHERE l.pid=a.pid AND l.locktype='advisory'
				)
			)`, pid).Scan(&residue); err != nil {
			t.Fatalf("inspect canceled waiter cleanup: %v", err)
		}
		if residue == 0 {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("canceled waiter PID %d retained a transaction or lock", pid)
		case <-ctx.Done():
			t.Fatalf("context ended while verifying waiter cleanup: %v", ctx.Err())
		}
	}
}

func openNodeConfigPG18NamedPool(t *testing.T, ctx context.Context, dsn, applicationName string) *platformdb.Pool {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		t.Fatal("parse named PostgreSQL worker DSN failed")
	}
	q := u.Query()
	q.Set("application_name", applicationName)
	q.Set("statement_timeout", "12000")
	u.RawQuery = q.Encode()
	pool, err := platformdb.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open named PostgreSQL worker %q: %v", applicationName, err)
	}
	var got string
	if err := pool.QueryRow(ctx, `SHOW application_name`).Scan(&got); err != nil || got != applicationName {
		pool.Close()
		t.Fatalf("verify named PostgreSQL worker got=%q want=%q err=%v", got, applicationName, err)
	}
	return pool
}

func assertNodeConfigPG18ApplicationName(t *testing.T, ctx context.Context,
	admin *pgx.Conn, pid int, want string) {
	t.Helper()
	var got string
	if err := admin.QueryRow(ctx, `SELECT application_name FROM pg_catalog.pg_stat_activity WHERE pid=$1`, pid).
		Scan(&got); err != nil {
		t.Fatalf("read PostgreSQL application_name for pid %d: %v", pid, err)
	}
	if got != want {
		t.Fatalf("PostgreSQL pid %d application_name=%q want=%q", pid, got, want)
	}
}

func runNodeConfigPG18CancellationRollbackBatch(t *testing.T, ctx context.Context,
	admin *pgx.Conn, appPool *platformdb.Pool, signer *platformcrypto.Signer) {
	t.Helper()
	service := nodefabric.NewService(appPool, signer)

	t.Run("publish canceled behind release advisory lock rolls back", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		holder := beginNodeConfigPG18LockHolder(t, ctx, appPool, fx)
		defer holder.cleanup()
		if _, err := holder.tx.Exec(ctx, `SELECT pg_catalog.pg_advisory_xact_lock(
			pg_catalog.hashtextextended($1,0))`, "node-config-release/"+fx.tenant); err != nil {
			t.Fatalf("hold release advisory lock: %v", err)
		}
		opCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := service.PublishConfig(opCtx, fx.tenant, nodefabric.PublishInput{
				ActorID: fx.actor, Scope: "global",
				Payload: json.RawMessage(`{"rollback":"advisory-wait"}`),
			})
			result <- err
		}()
		waiterPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
			`%pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))%`)
		cancel()
		awaitNodeConfigPG18Cancellation(t, opCtx, result)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, waiterPID)
		holder.release(t)
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
			t.Fatal("advisory-wait cancellation changed tenant business state")
		}
		t.Log("marker=node_config_pg18_publish_advisory_cancel_rollback_ok")
	})

	t.Run("publish canceled behind desired projection update rolls back", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		nodeID := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19301, "desired-cancel")
		baseline, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
			ActorID: fx.actor, Scope: "global",
			Payload: json.RawMessage(`{"rollback":"baseline","winner":"global"}`),
		})
		if err != nil {
			t.Fatalf("publish desired-cancel baseline: %v", err)
		}
		before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		holder := beginNodeConfigPG18LockHolder(t, ctx, appPool, fx)
		defer holder.cleanup()
		var lockedNode string
		if err := holder.tx.QueryRow(ctx, `SELECT id::text FROM nodes
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, fx.tenant, nodeID).Scan(&lockedNode); err != nil {
			t.Fatalf("hold desired projection node lock: %v", err)
		}
		opCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		result := make(chan error, 1)
		go func() {
			_, err := service.PublishConfig(opCtx, fx.tenant, nodefabric.PublishInput{
				ActorID: fx.actor, Scope: "global",
				Payload: json.RawMessage(`{"rollback":"must-not-commit","winner":"global"}`),
			})
			result <- err
		}()
		waiterPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
			`%SELECT id FROM nodes%serving_status<>'retired'%`)
		cancel()
		awaitNodeConfigPG18Cancellation(t, opCtx, result)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, waiterPID)
		holder.release(t)
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
			t.Fatal("desired-update cancellation changed tenant business state")
		}
		assertNodeConfigPG18DesiredFetch(t, ctx, admin, service, signer, fx.tenant, nodeID,
			baseline.Version, []string{fmt.Sprintf("global@v%d", baseline.Version)}, "global", map[string]int{})
		t.Log("marker=node_config_pg18_publish_desired_wait_cancel_rollback_ok")
	})

	t.Run("report canceled behind node share lock writes no evidence", func(t *testing.T) {
		fx := seedNodeConfigPG18Fixture(t, ctx, admin)
		nodeID := createNodeConfigPG18Node(t, ctx, service, fx, fx.pool, 19302, "report-cancel")
		published, err := service.PublishConfig(ctx, fx.tenant, nodefabric.PublishInput{
			ActorID: fx.actor, Scope: "global", Payload: json.RawMessage(`{"report":"cancel-baseline"}`),
		})
		if err != nil {
			t.Fatalf("publish report-cancel baseline: %v", err)
		}
		applicationsBefore := nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeID)
		before := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant)
		holder := beginNodeConfigPG18LockHolder(t, ctx, appPool, fx)
		defer holder.cleanup()
		var lockedNode string
		if err := holder.tx.QueryRow(ctx, `SELECT id::text FROM nodes
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, fx.tenant, nodeID).Scan(&lockedNode); err != nil {
			t.Fatalf("hold report node lock: %v", err)
		}
		opCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		result := make(chan error, 1)
		go func() {
			result <- service.ReportConfigApplied(opCtx, fx.tenant, nodeID,
				published.Version, "verified", "must-not-commit")
		}()
		waiterPID := waitNodeConfigPG18BlockedPID(t, ctx, admin, holder.pid,
			`%SELECT pool_id::text FROM nodes%FOR SHARE%`)
		cancel()
		awaitNodeConfigPG18Cancellation(t, opCtx, result)
		assertNodeConfigPG18WaiterClean(t, ctx, admin, waiterPID)
		holder.release(t)
		if after := nodeConfigPG18BusinessSnapshot(t, ctx, admin, fx.tenant); after != before {
			t.Fatal("report-wait cancellation changed tenant business state")
		}
		if after := nodeConfigPG18ApplicationCount(t, ctx, admin, fx.tenant, nodeID); after != applicationsBefore {
			t.Fatalf("report-wait cancellation applications before=%d after=%d", applicationsBefore, after)
		}
		t.Log("marker=node_config_pg18_report_wait_cancel_rollback_ok")
	})
}

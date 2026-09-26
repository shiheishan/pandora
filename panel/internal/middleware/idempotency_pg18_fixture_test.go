// [INPUT]: 依赖 platform/db 的 Open（按 application_name 开池）、pg_stat_activity 观测锁等待，依赖 idempotency_recorder_test.go 的 commitTrackingWriter
// [OUTPUT]: 包内提供 pg18OpenPool、pg18AssertRuntimeRole、pg18Serve* 请求工具、pg18WaitForBlockedActivity 等锁等待观测、pg18InTx，以及照线上契约独立重写（不调用生产实现）的期望请求哈希与 actor 作用域
// [POS]: TestIdempotencyMiddlewarePG18 的夹具与工具，与门禁主体分开放
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func pg18OpenPool(t *testing.T, ctx context.Context, baseDSN, applicationName string) *platformdb.Pool {
	t.Helper()
	u, err := url.Parse(baseDSN)
	if err != nil {
		t.Fatalf("parse PG18 DSN: %v", err)
	}
	query := u.Query()
	query.Set("application_name", applicationName)
	u.RawQuery = query.Encode()
	pool, err := platformdb.Open(ctx, u.String())
	if err != nil {
		t.Fatalf("open %s pool: %v", applicationName, err)
	}
	return pool
}

func pg18AssertRuntimeRole(
	t *testing.T, ctx context.Context, pool *platformdb.Pool, wantApplicationName string,
) {
	t.Helper()
	var user, appName string
	var superuser, bypassRLS bool
	if err := pool.QueryRow(ctx, `
		SELECT current_user,current_setting('application_name'),rolsuper,rolbypassrls
		  FROM pg_roles WHERE rolname=current_user`,
	).Scan(&user, &appName, &superuser, &bypassRLS); err != nil {
		t.Fatalf("query runtime role: %v", err)
	}
	if user != "aegis_app" || appName != wantApplicationName || superuser || bypassRLS {
		t.Fatalf("unsafe runtime role user=%s app=%s super=%v bypass=%v",
			user, appName, superuser, bypassRLS)
	}
}

func pg18Entries(
	poolA, poolB *platformdb.Pool,
	scope string,
	log *slog.Logger,
	downstream http.Handler,
) (http.Handler, http.Handler) {
	return Idempotency(poolA, scope, log)(downstream),
		Idempotency(poolB, scope, log)(downstream)
}

func pg18Serve(
	handler http.Handler,
	tenantID, actorID, key, path string,
	body []byte,
	withPrincipal bool,
) *httptest.ResponseRecorder {
	return pg18ServeContext(
		context.Background(), handler, tenantID, actorID, key, path, body, withPrincipal,
	)
}

func pg18ServeContext(
	requestContext context.Context,
	handler http.Handler,
	tenantID, actorID, key, path string,
	body []byte,
	withPrincipal bool,
) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	ctx := httpx.WithTenantID(requestContext, tenantID)
	if withPrincipal {
		ctx = httpx.WithPrincipal(ctx, &httpx.Principal{
			Kind: "user", Audience: "public", UserID: actorID, TenantID: tenantID,
		})
	}
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

type pg18Queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type pg18Activity struct {
	applicationName string
	pid             int32
	state           string
	waitEventType   string
	waitEvent       string
	query           string
	blockingPIDs    []int32
}

func pg18WaitForBlockedActivity(
	t *testing.T,
	parent context.Context,
	queryer pg18Queryer,
	applicationName string,
	earlyResult <-chan *httptest.ResponseRecorder,
	accept func(pg18Activity) bool,
) pg18Activity {
	t.Helper()
	// 只用 deadline 计时，不给 parent 套可取消的子 context：这些查询跑在
	// 控制连接上，取消会连带把连接关掉，后续观察就全是空的。
	deadline := time.Now().Add(5 * time.Second)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var lastRound []pg18Activity

	for {
		select {
		case response := <-earlyResult:
			t.Fatalf("request returned before lock proof application=%s status=%d",
				applicationName, response.Code)
		default:
		}

		rows, err := queryer.Query(parent, `
			SELECT pid,state,coalesce(wait_event_type,''),coalesce(wait_event,''),
			       coalesce(query,''),pg_blocking_pids(pid)
			  FROM pg_catalog.pg_stat_activity
			 WHERE application_name=$1 AND pid<>pg_backend_pid()
			 ORDER BY pid`, applicationName)
		if err == nil {
			matches := make([]pg18Activity, 0, 1)
			round := make([]pg18Activity, 0, 4)
			for rows.Next() {
				activity := pg18Activity{applicationName: applicationName}
				if err := rows.Scan(&activity.pid, &activity.state,
					&activity.waitEventType, &activity.waitEvent,
					&activity.query, &activity.blockingPIDs); err != nil {
					rows.Close()
					t.Fatalf("activity scan failed application=%s: %v", applicationName, err)
				}
				round = append(round, activity)
				if accept(activity) {
					matches = append(matches, activity)
				}
			}
			rowsErr := rows.Err()
			rows.Close()
			lastRound = round
			if rowsErr != nil && parent.Err() == nil {
				t.Fatalf("activity rows failed application=%s: %v", applicationName, rowsErr)
			}
			if len(matches) > 1 {
				t.Fatalf("ambiguous lock proof application=%s matching_sessions=%d",
					applicationName, len(matches))
			}
			if len(matches) == 1 {
				return matches[0]
			}
		} else if parent.Err() == nil {
			t.Fatalf("activity poll failed application=%s: %v", applicationName, err)
		}

		if time.Now().After(deadline) {
			var report strings.Builder
			for _, activity := range lastRound {
				query := strings.Join(strings.Fields(activity.query), " ")
				if len(query) > 160 {
					query = query[:160] + "…"
				}
				fmt.Fprintf(&report, "\n  pid=%d state=%s wait=%s/%s category=%s blockers=%v q=%s",
					activity.pid, activity.state, activity.waitEventType, activity.waitEvent,
					pg18SQLCategory(activity.query), activity.blockingPIDs, query)
			}
			if report.Len() == 0 {
				report.WriteString("\n  <该 application_name 下没有任何连接>")
			}
			// 过滤后为空时无法区分「池还没连上」和「连接挂在别的名字下」，
			// 所以再扫一次全库。
			report.WriteString("\n  --- 全库连接 ---")
			allRows, allErr := queryer.Query(parent, `
				SELECT pid,coalesce(application_name,''),state,
				       coalesce(wait_event_type,''),coalesce(wait_event,''),
				       coalesce(left(regexp_replace(query,'\s+',' ','g'),120),''),
				       pg_blocking_pids(pid)
				  FROM pg_catalog.pg_stat_activity
				 WHERE datname=current_database() ORDER BY pid`)
			if allErr != nil {
				fmt.Fprintf(&report, "\n  <全库扫描失败: %v>", allErr)
			} else {
				for allRows.Next() {
					var pid int32
					var name, state, waitType, waitEvent, query string
					var blockers []int32
					if err := allRows.Scan(&pid, &name, &state, &waitType,
						&waitEvent, &query, &blockers); err != nil {
						fmt.Fprintf(&report, "\n  <扫描失败: %v>", err)
						break
					}
					fmt.Fprintf(&report, "\n  pid=%d app=%q state=%s wait=%s/%s blockers=%v q=%s",
						pid, name, state, waitType, waitEvent, blockers, query)
				}
				allRows.Close()
			}
			t.Fatalf("activity poll timeout application=%s sessions=%d%s",
				applicationName, len(lastRound), report.String())
		}
		<-ticker.C
	}
}

func pg18PIDListContains(pids []int32, want int32) bool {
	for _, pid := range pids {
		if pid == want {
			return true
		}
	}
	return false
}

func pg18SQLCategory(query string) string {
	normalized := strings.ToUpper(strings.Join(strings.Fields(query), " "))
	switch {
	case strings.Contains(normalized, "UPDATE IDEMPOTENCY_KEYS"):
		return "idempotency_update"
	case strings.Contains(normalized, "IDEMPOTENCY_KEYS") &&
		strings.Contains(normalized, "FOR UPDATE"):
		return "idempotency_select_for_update"
	case strings.Contains(normalized, "INSERT INTO IDEMPOTENCY_KEYS"):
		return "idempotency_insert"
	case normalized == "":
		return "none"
	default:
		return "other"
	}
}

func pg18ServeTracking(
	handler http.Handler,
	tenantID, actorID, key, path string,
	body []byte,
) *commitTrackingWriter {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", key)
	ctx := httpx.WithTenantID(req.Context(), tenantID)
	ctx = httpx.WithPrincipal(ctx, &httpx.Principal{
		Kind: "user", Audience: "public", UserID: actorID, TenantID: tenantID,
	})
	req = req.WithContext(ctx)
	w := newCommitTrackingWriter()
	handler.ServeHTTP(w, req)
	return w
}

func pg18InTx(
	t *testing.T,
	ctx context.Context,
	pool *platformdb.Pool,
	tenantID, actorID string,
	fn func(pgx.Tx),
) {
	t.Helper()
	err := pool.InTx(ctx, platformdb.Scope{TenantID: tenantID, ActorID: actorID},
		func(tx pgx.Tx) error {
			fn(tx)
			return nil
		})
	if err != nil {
		t.Fatalf("PG18 transaction: %v", err)
	}
}

// These expectations intentionally duplicate the wire contract rather than
// calling production idempotencyRequestHash/idempotencyActorScope helpers.
func pg18ExpectedRequestHash(method, path string, body []byte) [sha256.Size]byte {
	input := make([]byte, 0, len(method)+len(path)+len(body)+2)
	input = append(input, method...)
	input = append(input, '\n')
	input = append(input, path...)
	input = append(input, '\n')
	input = append(input, body...)
	return sha256.Sum256(input)
}

func pg18ExpectedActorScope(scope, actorID string) string {
	digest := sha256.Sum256([]byte(actorID))
	return fmt.Sprintf("%s:actor:%x", scope, digest[:12])
}

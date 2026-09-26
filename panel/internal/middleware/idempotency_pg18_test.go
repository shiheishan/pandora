// [INPUT]: 依赖 idempotency.go 的 Idempotency 与 IdempotencyClaimFrom，依赖 idempotency_pg18_fixture_test.go 的连接、请求与锁等待观测工具，依赖 deploy/fixtures/idempotency-pg18-seed.sql 与迁移 00037
// [OUTPUT]: 对外提供 TestIdempotencyMiddlewarePG18（run-pg18-gates.sh 的 idempotency 域）
// [POS]: 幂等中间件的 PG18 集成门禁：双连接池下的认领、真锁争用、过期不接管、哈希冲突、actor 隔离、完成代际、bytea 重放、旧格式转换与封锁、RLS 与列 ACL。整个文件只有一个 983 行的测试函数，子测试共享同一组连接与上下文，纯挪动拆不开，由行数守卫单独豁免
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	platformdb "github.com/aegispanel/aegis/internal/platform/db"
)

const (
	pg18TenantA = "91000000-0000-7000-8000-000000000001"
	pg18ActorA1 = "91000000-0000-7000-8000-000000000011"
	pg18ActorA2 = "91000000-0000-7000-8000-000000000012"
	pg18TenantB = "92000000-0000-7000-8000-000000000001"
	pg18ActorB1 = "92000000-0000-7000-8000-000000000011"
)

func TestIdempotencyMiddlewarePG18(t *testing.T) {
	baseDSN := os.Getenv("AEGIS_IDEMPOTENCY_PG18_DSN")
	if baseDSN == "" {
		t.Skip("AEGIS_IDEMPOTENCY_PG18_DSN is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	poolA := pg18OpenPool(t, ctx, baseDSN, "idempotency_pg18_a")
	defer poolA.Close()
	poolB := pg18OpenPool(t, ctx, baseDSN, "idempotency_pg18_b")
	defer poolB.Close()
	pg18AssertRuntimeRole(t, ctx, poolA, "idempotency_pg18_a")
	pg18AssertRuntimeRole(t, ctx, poolB, "idempotency_pg18_b")
	t.Log("marker=idempotency_pg18_runtime_role_and_two_pools_ok")

	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("new claim context and evidence", func(t *testing.T) {
		const scope = "pg18_new_claim"
		const key = "new-claim-key"
		path := "/pg18/new"
		body := []byte(`{"amount":101}`)
		var gotClaim IdempotencyClaim
		downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claim, ok := IdempotencyClaimFrom(r.Context())
			if !ok {
				t.Error("downstream did not receive owned claim")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			gotClaim = claim
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"created":true}`))
		})
		entryA, _ := pg18Entries(poolA, poolB, scope, log, downstream)
		response := pg18Serve(entryA, pg18TenantA, pg18ActorA1, key, path, body, true)
		if response.Code != http.StatusCreated {
			t.Fatalf("new claim status = %d, body=%s", response.Code, response.Body.String())
		}
		wantHash := pg18ExpectedRequestHash(http.MethodPost, path, body)
		if gotClaim.ID == "" || gotClaim.TenantID != pg18TenantA ||
			gotClaim.ActorID != pg18ActorA1 || gotClaim.Scope != scope ||
			gotClaim.StorageScope != pg18ExpectedActorScope(scope, pg18ActorA1) ||
			gotClaim.Key != key || gotClaim.Generation != 1 ||
			gotClaim.LockedUntil.IsZero() || !gotClaim.LockedUntil.After(time.Now()) ||
			gotClaim.RequestHash != wantHash {
			t.Fatalf("owned claim mismatch: %#v", gotClaim)
		}

		pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
			var id, status, actorID, responseFormat string
			var responseCode int
			var responsePayload []byte
			var responseContentType *string
			var legacyBodyNull, completed, unlocked bool
			var generation int64
			err := tx.QueryRow(ctx, `
				SELECT id::text,status,actor_id::text,response_code,
				       response_format,response_body IS NULL,response_payload,
				       response_content_type,claim_generation,
				       completed_at IS NOT NULL,locked_until IS NULL
				  FROM idempotency_keys
				 WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
				pg18TenantA, pg18ExpectedActorScope(scope, pg18ActorA1), key,
			).Scan(&id, &status, &actorID, &responseCode,
				&responseFormat, &legacyBodyNull, &responsePayload,
				&responseContentType, &generation, &completed, &unlocked)
			if err != nil {
				t.Fatalf("query completed evidence: %v", err)
			}
			if id != gotClaim.ID || status != "succeeded" || actorID != pg18ActorA1 ||
				responseCode != http.StatusCreated || responseFormat != "bytes" ||
				!legacyBodyNull || string(responsePayload) != `{"created":true}` ||
				responseContentType == nil || *responseContentType != "application/json" ||
				generation != 1 || !completed || !unlocked {
				t.Fatalf("completed evidence mismatch id=%s status=%s actor=%s code=%d format=%s legacy_null=%v payload=%q type=%v generation=%d completed=%v unlocked=%v",
					id, status, actorID, responseCode, responseFormat, legacyBodyNull,
					responsePayload, responseContentType, generation, completed, unlocked)
			}
		})
		t.Log("marker=idempotency_pg18_new_claim_context_evidence_ok")
	})

	t.Run("informational responses preserve final 201 database evidence", func(t *testing.T) {
		const scope = "pg18_informational_final"
		const key = "informational-final-key"
		const path = "/pg18/informational-final"
		body := []byte(`{"phase":"final"}`)
		downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusProcessing)
			w.WriteHeader(http.StatusEarlyHints)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"created":true}`))
		})
		entryA, _ := pg18Entries(poolA, poolB, scope, log, downstream)
		response := pg18ServeTracking(
			entryA, pg18TenantA, pg18ActorA1, key, path, body,
		)
		if len(response.informational) != 2 ||
			response.informational[0] != http.StatusProcessing ||
			response.informational[1] != http.StatusEarlyHints ||
			response.status != http.StatusCreated ||
			response.body.String() != `{"created":true}` {
			t.Fatalf("informational/final response = info=%v status=%d body=%q",
				response.informational, response.status, response.body.String())
		}

		pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
			var record idempotencyRecord
			if err := tx.QueryRow(ctx, `
				SELECT status,response_code,response_format,response_body,response_payload,
				       response_content_type,response_location,response_etag,
				       response_cache_control,response_content_language,
				       locked_until,claim_generation,resource_type,resource_id
				  FROM idempotency_keys
				 WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
				pg18TenantA, pg18ExpectedActorScope(scope, pg18ActorA1), key,
			).Scan(&record.status, &record.responseCode, &record.responseFormat,
				&record.responseBody, &record.responsePayload,
				&record.headers.ContentType, &record.headers.Location,
				&record.headers.ETag, &record.headers.CacheControl,
				&record.headers.ContentLanguage, &record.lockedUntil,
				&record.generation, &record.resourceType, &record.resourceID); err != nil {
				t.Fatalf("query informational evidence: %v", err)
			}
			decision, err := decideIdempotencyRecord(record)
			if err != nil || !decision.replay || record.responseCode == nil ||
				*record.responseCode != http.StatusCreated ||
				decision.headers.ContentType == nil ||
				*decision.headers.ContentType != "application/json" ||
				string(decision.body) != `{"created":true}` {
				t.Fatalf("informational DB evidence = record=%#v replay=%v body=%q err=%v",
					record, decision.replay, decision.body, err)
			}
		})
		t.Log("marker=idempotency_pg18_informational_final_201_evidence_ok")
	})

	t.Run("failed row true lock contention has one owner and exact replay", func(t *testing.T) {
		const scope = "pg18_failed_lock_race"
		const key = "failed-lock-race-key"
		path := "/pg18/failed-lock-race"
		body := []byte(`{"attempt":"same"}`)
		wantSuccess := []byte{0x00, 0xff, 'o', 'k'}

		seed := Idempotency(poolA, scope, log)(http.HandlerFunc(
			func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte("temporary"))
			},
		))
		first := pg18Serve(seed, pg18TenantA, pg18ActorA1, key, path, body, true)
		if first.Code != http.StatusServiceUnavailable {
			t.Fatalf("initial failed status = %d", first.Code)
		}
		pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
			var status string
			if err := tx.QueryRow(ctx, `SELECT status FROM idempotency_keys
				WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
				pg18TenantA, pg18ExpectedActorScope(scope, pg18ActorA1), key,
			).Scan(&status); err != nil || status != "failed" {
				t.Fatalf("seeded failed row status/error = %q/%v", status, err)
			}
		})

		controlPool := pg18OpenPool(t, ctx, baseDSN, "idempotency_pg18_control")
		defer controlPool.Close()
		controlConn, err := controlPool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire control connection: %v", err)
		}
		defer controlConn.Release()

		var currentUser, sessionUser, rowSecurity string
		var controllerPID int32
		if err := controlConn.QueryRow(ctx, `
			SELECT current_user,session_user,current_setting('row_security'),
			       pg_backend_pid()`,
		).Scan(&currentUser, &sessionUser, &rowSecurity, &controllerPID); err != nil {
			t.Fatalf("inspect control session: %v", err)
		}
		if currentUser != "aegis_app" || sessionUser != currentUser || rowSecurity != "on" {
			t.Fatalf("unsafe control session current=%s session=%s row_security=%s",
				currentUser, sessionUser, rowSecurity)
		}
		// 控制连接在事务里锁住那条 failed 行。重试路径要把它从 failed 抬回
		// in_flight，必然撞在这把行锁上——这才是中间件真正用来互斥的东西。
		// （之前这里拿的是一把 advisory lock，中间件从不申请它，所以谁也挡不住。）
		if _, err := controlConn.Exec(ctx, `BEGIN`); err != nil {
			t.Fatalf("begin controller tx: %v", err)
		}
		if _, err := controlConn.Exec(ctx,
			`SELECT set_config('app.tenant_id',$1,true),set_config('app.actor_id',$2,true)`,
			pg18TenantA, pg18ActorA1,
		); err != nil {
			t.Fatalf("scope controller tx: %v", err)
		}
		var lockedRowID string
		if err := controlConn.QueryRow(ctx, `
			SELECT id::text FROM idempotency_keys
			 WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3
			 FOR UPDATE`,
			pg18TenantA, pg18ExpectedActorScope(scope, pg18ActorA1), key,
		).Scan(&lockedRowID); err != nil {
			t.Fatalf("controller lock failed row: %v", err)
		}

		var unlockOnce sync.Once
		var unlockOK bool
		var unlockErr error
		unlockController := func() (bool, error) {
			unlockOnce.Do(func() {
				unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				// 回滚即释放行锁；这个事务只用来持锁，没有要保留的写入。
				_, unlockErr = controlConn.Exec(unlockCtx, `ROLLBACK`)
				unlockOK = unlockErr == nil
			})
			return unlockOK, unlockErr
		}
		defer func() { _, _ = unlockController() }()

		var calls atomic.Int32
		started := make(chan struct{})
		release := make(chan struct{})
		var startOnce sync.Once
		var releaseOnce sync.Once
		releaseDownstream := func() { releaseOnce.Do(func() { close(release) }) }
		defer releaseDownstream()
		downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			startOnce.Do(func() { close(started) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(wantSuccess)
		})
		entryA, entryB := pg18Entries(poolA, poolB, scope, log, downstream)

		winnerCtx, winnerCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer winnerCancel()
		loserCtx, loserCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer loserCancel()
		winnerResult := make(chan *httptest.ResponseRecorder, 1)
		winnerRequestStarted := make(chan struct{})
		go func() {
			close(winnerRequestStarted)
			winnerResult <- pg18ServeContext(
				winnerCtx, entryA, pg18TenantA, pg18ActorA1, key, path, body, true,
			)
		}()
		<-winnerRequestStarted

		winnerActivity := pg18WaitForBlockedActivity(
			t, ctx, controlConn, "idempotency_pg18_a", winnerResult,
			func(activity pg18Activity) bool {
				query := strings.ToUpper(activity.query)
				lockWait := strings.EqualFold(activity.waitEvent, "transactionid") ||
					strings.EqualFold(activity.waitEvent, "tuple")
				// 不看 state：等锁时 pg_stat_activity 的 state / query / wait_event
				// 未必同步，认「在等锁且被控制连接挡住」这个事实。
				return activity.waitEventType == "Lock" && lockWait &&
					strings.Contains(query, "IDEMPOTENCY_KEYS") &&
					strings.Contains(query, "FOR UPDATE") &&
					pg18PIDListContains(activity.blockingPIDs, controllerPID)
			},
		)
		if calls.Load() != 0 {
			t.Fatalf("winner reached downstream while advisory-blocked, calls=%d", calls.Load())
		}

		loserResult := make(chan *httptest.ResponseRecorder, 1)
		loserRequestStarted := make(chan struct{})
		go func() {
			close(loserRequestStarted)
			loserResult <- pg18ServeContext(
				loserCtx, entryB, pg18TenantA, pg18ActorA1, key, path, body, true,
			)
		}()
		<-loserRequestStarted
		_ = pg18WaitForBlockedActivity(
			t, ctx, controlConn, "idempotency_pg18_b", loserResult,
			func(activity pg18Activity) bool {
				lockWait := strings.EqualFold(activity.waitEvent, "tuple") ||
					strings.EqualFold(activity.waitEvent, "transactionid")
				// loser 这边连 query 都不能信：实测它的 wait_event 已经是
				// Lock/tuple、blockers 已指向 winner，而 query 还停在连接归还时
				// 那条 set_config。锁链才是要证明的东西。
				return activity.waitEventType == "Lock" && lockWait &&
					pg18PIDListContains(activity.blockingPIDs, winnerActivity.pid)
			},
		)
		if calls.Load() != 0 {
			t.Fatalf("downstream ran before lock chain proof, calls=%d", calls.Load())
		}
		t.Log("marker=idempotency_pg18_failed_row_true_lock_chain_ok")

		if ok, err := unlockController(); err != nil || !ok {
			t.Fatalf("controller row-lock release = %v/%v", ok, err)
		}
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("retry winner did not enter downstream after controller released the row lock")
		}
		if calls.Load() != 1 {
			t.Fatalf("downstream calls after winner entry = %d", calls.Load())
		}

		var loser *httptest.ResponseRecorder
		select {
		case loser = <-loserResult:
		case <-time.After(5 * time.Second):
			t.Fatal("retry loser did not resume after winner takeover commit")
		}
		if loser.Code != http.StatusConflict {
			t.Fatalf("retry loser status = %d, want 409", loser.Code)
		}
		if calls.Load() != 1 {
			t.Fatalf("retry loser entered downstream, calls=%d", calls.Load())
		}

		releaseDownstream()
		var winner *httptest.ResponseRecorder
		select {
		case winner = <-winnerResult:
		case <-time.After(5 * time.Second):
			t.Fatal("retry winner did not finish after downstream release")
		}
		if winner.Code != http.StatusCreated || !bytes.Equal(winner.Body.Bytes(), wantSuccess) {
			t.Fatalf("retry winner status/body = %d/%v", winner.Code, winner.Body.Bytes())
		}
		if winner.Header().Get("Content-Type") != "application/octet-stream" {
			t.Fatalf("retry winner content type = %q", winner.Header().Get("Content-Type"))
		}

		replay := pg18Serve(entryB, pg18TenantA, pg18ActorA1, key, path, body, true)
		if replay.Code != http.StatusCreated || !bytes.Equal(replay.Body.Bytes(), wantSuccess) ||
			replay.Header().Get("Idempotency-Replayed") != "true" ||
			replay.Header().Get("Content-Type") != "application/octet-stream" {
			t.Fatalf("third replay mismatch status=%d body=%v headers=%v",
				replay.Code, replay.Body.Bytes(), replay.Header())
		}
		if calls.Load() != 1 {
			t.Fatalf("replay invoked downstream, calls=%d", calls.Load())
		}
		pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
			var generation int64
			var format string
			var payload []byte
			if err := tx.QueryRow(ctx, `
				SELECT claim_generation,response_format,response_payload
				  FROM idempotency_keys
				 WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
				pg18TenantA, pg18ExpectedActorScope(scope, pg18ActorA1), key,
			).Scan(&generation, &format, &payload); err != nil {
				t.Fatalf("query takeover generation: %v", err)
			}
			if generation != 2 || format != "bytes" || !bytes.Equal(payload, wantSuccess) {
				t.Fatalf("takeover evidence generation/format/payload = %d/%s/%v",
					generation, format, payload)
			}
		})
		t.Log("marker=idempotency_pg18_failed_takeover_generation_increment_once_ok")
		t.Log("marker=idempotency_pg18_failed_row_single_owner_409_replay_ok")
	})

	t.Run("expired in flight is never taken over", func(t *testing.T) {
		const scope = "pg18_expired_inflight"
		const key = "expired-inflight-key"
		path := "/pg18/expired"
		body := []byte(`{"same":true}`)
		hash := pg18ExpectedRequestHash(http.MethodPost, path, body)
		pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
			_, err := tx.Exec(ctx, `
				INSERT INTO idempotency_keys
				  (tenant_id,scope,idempotency_key,request_hash,actor_id,locked_until)
				VALUES ($1,$2,$3,$4,$5,now()-interval '1 minute')`,
				pg18TenantA, pg18ExpectedActorScope(scope, pg18ActorA1), key,
				hash[:], pg18ActorA1)
			if err != nil {
				t.Fatalf("seed expired in-flight: %v", err)
			}
		})
		var calls atomic.Int32
		downstream := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) })
		entryA, entryB := pg18Entries(poolA, poolB, scope, log, downstream)
		results := make(chan int, 2)
		var wg sync.WaitGroup
		for _, entry := range []http.Handler{entryA, entryB} {
			wg.Add(1)
			go func(h http.Handler) {
				defer wg.Done()
				results <- pg18Serve(h, pg18TenantA, pg18ActorA1, key, path, body, true).Code
			}(entry)
		}
		wg.Wait()
		close(results)
		for status := range results {
			if status != http.StatusConflict {
				t.Fatalf("expired in-flight status = %d, want 409", status)
			}
		}
		if calls.Load() != 0 {
			t.Fatalf("expired in-flight invoked downstream %d times", calls.Load())
		}
		t.Log("marker=idempotency_pg18_expired_inflight_double_conflict_ok")
	})

	t.Run("different request hash conflicts", func(t *testing.T) {
		const scope = "pg18_hash_conflict"
		const key = "hash-conflict-key"
		const path = "/pg18/hash"
		var calls atomic.Int32
		downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		})
		entryA, _ := pg18Entries(poolA, poolB, scope, log, downstream)
		if got := pg18Serve(entryA, pg18TenantA, pg18ActorA1, key, path,
			[]byte(`{"v":1}`), true); got.Code != http.StatusNoContent {
			t.Fatalf("initial hash request = %d", got.Code)
		}
		if got := pg18Serve(entryA, pg18TenantA, pg18ActorA1, key, path,
			[]byte(`{"v":2}`), true); got.Code != http.StatusConflict {
			t.Fatalf("different hash status = %d, want 409", got.Code)
		}
		if calls.Load() != 1 {
			t.Fatalf("different hash invoked downstream, calls=%d", calls.Load())
		}
		t.Log("marker=idempotency_pg18_different_hash_conflict_ok")
	})

	t.Run("same key and body with different raw query conflicts", func(t *testing.T) {
		const scope = "pg18_query_conflict"
		const key = "query-conflict-key"
		body := []byte(`{"same":true}`)
		var calls atomic.Int32
		downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		})
		entryA, entryB := pg18Entries(poolA, poolB, scope, log, downstream)
		first := pg18Serve(entryA, pg18TenantA, pg18ActorA1, key,
			"/pg18/query/a%2Fb?mode=full&cursor=%2F", body, true)
		conflict := pg18Serve(entryB, pg18TenantA, pg18ActorA1, key,
			"/pg18/query/a%2Fb?mode=partial&cursor=%2F", body, true)
		if first.Code != http.StatusNoContent || conflict.Code != http.StatusConflict ||
			calls.Load() != 1 {
			t.Fatalf("query conflict status/calls = %d/%d/%d",
				first.Code, conflict.Code, calls.Load())
		}
		t.Log("marker=idempotency_pg18_same_key_different_raw_query_conflict_ok")
	})

	t.Run("same tenant actors have isolated keys", func(t *testing.T) {
		const scope = "pg18_actor_isolation"
		const key = "same-raw-key"
		const path = "/pg18/actors"
		body := []byte(`{"same":true}`)
		var calls atomic.Int32
		downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		})
		entryA, entryB := pg18Entries(poolA, poolB, scope, log, downstream)
		for _, tc := range []struct {
			entry http.Handler
			actor string
		}{{entryA, pg18ActorA1}, {entryB, pg18ActorA2}} {
			if got := pg18Serve(tc.entry, pg18TenantA, tc.actor, key, path, body, true); got.Code != http.StatusNoContent {
				t.Fatalf("actor %s status = %d", tc.actor, got.Code)
			}
		}
		if calls.Load() != 2 {
			t.Fatalf("isolated actors downstream calls = %d", calls.Load())
		}
		for _, tc := range []struct {
			pool    *platformdb.Pool
			actorID string
		}{{poolA, pg18ActorA1}, {poolB, pg18ActorA2}} {
			pg18InTx(t, ctx, tc.pool, pg18TenantA, tc.actorID, func(tx pgx.Tx) {
				var count int
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys
					WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
					pg18TenantA, pg18ExpectedActorScope(scope, tc.actorID), key,
				).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Fatalf("actor %s visible canonical row count = %d, want 1",
						tc.actorID, count)
				}
			})
		}
		t.Log("marker=idempotency_pg18_same_tenant_actor_isolation_ok")
	})

	t.Run("completion generation mismatch updates zero rows", func(t *testing.T) {
		const scope = "pg18_completion_generation"
		const key = "generation-key"
		path := "/pg18/generation"
		body := []byte(`{"generation":1}`)
		hash := pg18ExpectedRequestHash(http.MethodPost, path, body)
		actorScope := pg18ExpectedActorScope(scope, pg18ActorA1)
		pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
			var id string
			var lease time.Time
			var generation int64
			if err := tx.QueryRow(ctx, `
				INSERT INTO idempotency_keys
				  (tenant_id,scope,idempotency_key,request_hash,actor_id,locked_until)
				VALUES ($1,$2,$3,$4,$5,now()+interval '5 minutes')
				RETURNING id::text,locked_until,claim_generation`,
				pg18TenantA, actorScope, key, hash[:], pg18ActorA1,
			).Scan(&id, &lease, &generation); err != nil {
				t.Fatalf("seed generation claim: %v", err)
			}
			var completedID string
			err := tx.QueryRow(ctx, completeIdempotencyClaimSQL,
				id, pg18TenantA, actorScope, key, pg18ActorA1, hash[:],
				generation+1, "succeeded", http.StatusOK, "bytes", []byte(`{"ok":true}`),
				nil, nil, nil, nil, nil, lease,
			).Scan(&completedID)
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("wrong generation completion error = %v, want no rows", err)
			}
			var status string
			var observedGeneration int64
			if err := tx.QueryRow(ctx, `SELECT status,claim_generation FROM idempotency_keys WHERE id=$1`, id).
				Scan(&status, &observedGeneration); err != nil || status != "in_flight" ||
				observedGeneration != generation {
				t.Fatalf("generation row status/generation/error = %s/%d/%v",
					status, observedGeneration, err)
			}
		})
		t.Log("marker=idempotency_pg18_generation_aba_stale_complete_zero_rows_ok")
	})

	t.Run("one MiB binary survives modern bytea replay", func(t *testing.T) {
		const scope = "pg18_binary_1mib"
		const key = "binary-1mib-key"
		const path = "/pg18/binary"
		payload := make([]byte, maxIdempotencyResponseBytes)
		for i := range payload {
			payload[i] = byte(i % 251)
		}
		payload[0], payload[1] = 0x00, 0xff
		var calls atomic.Int32
		downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/octet-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
		})
		entryA, entryB := pg18Entries(poolA, poolB, scope, log, downstream)
		first := pg18Serve(entryA, pg18TenantA, pg18ActorA1, key, path, nil, true)
		replay := pg18Serve(entryB, pg18TenantA, pg18ActorA1, key, path, nil, true)
		for name, got := range map[string]*httptest.ResponseRecorder{"first": first, "replay": replay} {
			if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), payload) ||
				got.Header().Get("Content-Type") != "application/octet-stream" {
				t.Fatalf("%s 1MiB status/len/type = %d/%d/%q", name, got.Code,
					got.Body.Len(), got.Header().Get("Content-Type"))
			}
		}
		if replay.Header().Get("Idempotency-Replayed") != "true" || calls.Load() != 1 {
			t.Fatalf("1MiB replay header/calls = %q/%d",
				replay.Header().Get("Idempotency-Replayed"), calls.Load())
		}
		pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
			var format string
			var payloadBytes int
			var legacyBodyNull bool
			if err := tx.QueryRow(ctx, `
				SELECT response_format,octet_length(response_payload),response_body IS NULL
				  FROM idempotency_keys
				 WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
				pg18TenantA, pg18ExpectedActorScope(scope, pg18ActorA1), key,
			).Scan(&format, &payloadBytes, &legacyBodyNull); err != nil {
				t.Fatalf("query modern 1MiB evidence: %v", err)
			}
			if format != "bytes" || payloadBytes != len(payload) || !legacyBodyNull {
				t.Fatalf("modern 1MiB evidence = %s/%d/legacy-null=%v",
					format, payloadBytes, legacyBodyNull)
			}
		})
		t.Log("marker=idempotency_pg18_1mib_binary_bytea_replay_ok")
	})

	t.Run("modern empty bytea preserves null versus explicit empty content type", func(t *testing.T) {
		for _, tc := range []struct {
			name                string
			scope               string
			key                 string
			explicitContentType bool
		}{
			{name: "null", scope: "pg18_empty_ct_null", key: "empty-ct-null-key"},
			{name: "explicit empty", scope: "pg18_empty_ct_empty", key: "empty-ct-empty-key", explicitContentType: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				var calls atomic.Int32
				downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					if tc.explicitContentType {
						w.Header()["Content-Type"] = []string{""}
					}
					w.WriteHeader(http.StatusNoContent)
				})
				entryA, entryB := pg18Entries(poolA, poolB, tc.scope, log, downstream)
				path := "/pg18/empty-content-type/" + tc.key
				first := pg18Serve(entryA, pg18TenantA, pg18ActorA1, tc.key, path, nil, true)
				replay := pg18Serve(entryB, pg18TenantA, pg18ActorA1, tc.key, path, nil, true)
				if first.Code != http.StatusNoContent || replay.Code != http.StatusNoContent ||
					replay.Body.Len() != 0 || replay.Header().Get("Idempotency-Replayed") != "true" ||
					calls.Load() != 1 {
					t.Fatalf("empty modern replay status/body/header/calls = %d/%d/%d/%q/%d",
						first.Code, replay.Code, replay.Body.Len(),
						replay.Header().Get("Idempotency-Replayed"), calls.Load())
				}
				for responseName, response := range map[string]*httptest.ResponseRecorder{
					"first": first, "replay": replay,
				} {
					values, present := response.Header()["Content-Type"]
					if present != tc.explicitContentType ||
						(present && (len(values) != 1 || values[0] != "")) {
						t.Fatalf("%s content-type presence/value = %v/%q, explicit=%v",
							responseName, present, values, tc.explicitContentType)
					}
				}
				pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
					var format string
					var payloadPresent bool
					var payloadLength int
					var contentType *string
					if err := tx.QueryRow(ctx, `
						SELECT response_format,response_payload IS NOT NULL,
						       octet_length(response_payload),response_content_type
						  FROM idempotency_keys
						 WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
						pg18TenantA, pg18ExpectedActorScope(tc.scope, pg18ActorA1), tc.key,
					).Scan(&format, &payloadPresent, &payloadLength, &contentType); err != nil {
						t.Fatalf("query empty modern evidence: %v", err)
					}
					if format != "bytes" || !payloadPresent || payloadLength != 0 ||
						(contentType != nil) != tc.explicitContentType ||
						(contentType != nil && *contentType != "") {
						t.Fatalf("empty modern DB evidence = %s/%v/%d/%v",
							format, payloadPresent, payloadLength, contentType)
					}
				})
			})
		}
		t.Log("marker=idempotency_pg18_modern_empty_bytea_ct_null_explicit_empty_ok")
	})

	t.Run("schema36 actor-bound legacy JSON is replayed after conversion", func(t *testing.T) {
		const scope = "pg18_legacy_actor_replay"
		const key = "legacy-actor-replay-key"
		const path = "/pg18/legacy-actor?variant=1"
		body := []byte(`{"legacy":"request"}`)
		var calls atomic.Int32
		entryA, _ := pg18Entries(poolA, poolB, scope, log,
			http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		response := pg18Serve(entryA, pg18TenantA, pg18ActorA1, key, path, body, true)
		if response.Code != http.StatusCreated ||
			response.Header().Get("Idempotency-Replayed") != "true" || calls.Load() != 0 {
			t.Fatalf("legacy actor replay status/header/calls = %d/%q/%d",
				response.Code, response.Header().Get("Idempotency-Replayed"), calls.Load())
		}
		var replayed map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &replayed); err != nil ||
			replayed["legacy"] != true || replayed["source"] != "schema36" {
			t.Fatalf("legacy actor replay body = %q / %#v / %v",
				response.Body.Bytes(), replayed, err)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
			t.Fatalf("legacy actor replay content type = %q", got)
		}
		pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
			var format, actorID string
			var legacyPresent, payloadNull bool
			if err := tx.QueryRow(ctx, `
				SELECT response_format,actor_id::text,response_body IS NOT NULL,
				       response_payload IS NULL
				  FROM idempotency_keys
				 WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
				pg18TenantA, pg18ExpectedActorScope(scope, pg18ActorA1), key,
			).Scan(&format, &actorID, &legacyPresent, &payloadNull); err != nil {
				t.Fatalf("query converted legacy evidence: %v", err)
			}
			if format != "legacy_json" || actorID != pg18ActorA1 || !legacyPresent || !payloadNull {
				t.Fatalf("converted legacy evidence = %s/%s/%v/%v",
					format, actorID, legacyPresent, payloadNull)
			}
		})
		t.Log("marker=idempotency_pg18_actor_bound_legacy_real_replay_ok")
	})

	t.Run("all raw legacy states are permanently blocked and tenant isolated", func(t *testing.T) {
		const scope = "pg18_legacy_probe"
		var calls atomic.Int32
		entryA, entryB := pg18Entries(poolA, poolB, scope, log,
			http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		keys := []string{
			"legacy-probe-ancient-inflight",
			"legacy-probe-recent-inflight",
			"legacy-probe-ancient-terminal",
			"legacy-probe-recent-terminal",
		}
		for _, key := range keys {
			for _, actorID := range []string{pg18ActorA1, pg18ActorA2} {
				response := pg18Serve(
					entryA, pg18TenantA, actorID, key, "/pg18/legacy?key="+key, nil, true,
				)
				if response.Code != http.StatusConflict || calls.Load() != 0 {
					t.Fatalf("legacy probe key/actor/status/calls = %s/%s/%d/%d",
						key, actorID, response.Code, calls.Load())
				}
			}
		}
		pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
			var canonicalRows int
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM idempotency_keys
				 WHERE tenant_id=$1 AND scope IN ($2,$3)`,
				pg18TenantA, pg18ExpectedActorScope(scope, pg18ActorA1),
				pg18ExpectedActorScope(scope, pg18ActorA2),
			).Scan(&canonicalRows); err != nil {
				t.Fatalf("query canonical rows after legacy block: %v", err)
			}
			if canonicalRows != 0 {
				t.Fatalf("legacy probe allowed %d canonical writes", canonicalRows)
			}
		})

		isolatedKey := keys[0]
		isolated := pg18Serve(
			entryB, pg18TenantB, pg18ActorB1, isolatedKey,
			"/pg18/legacy?key="+isolatedKey, nil, true,
		)
		if isolated.Code != http.StatusOK || calls.Load() != 1 {
			t.Fatalf("cross-tenant legacy isolation status/calls = %d/%d",
				isolated.Code, calls.Load())
		}
		pg18InTx(t, ctx, poolB, pg18TenantB, pg18ActorB1, func(tx pgx.Tx) {
			var canonicalRows int
			if err := tx.QueryRow(ctx, `
				SELECT count(*) FROM idempotency_keys
				 WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
				pg18TenantB, pg18ExpectedActorScope(scope, pg18ActorB1), isolatedKey,
			).Scan(&canonicalRows); err != nil {
				t.Fatalf("query cross-tenant canonical row: %v", err)
			}
			if canonicalRows != 1 {
				t.Fatalf("cross-tenant canonical rows = %d, want 1", canonicalRows)
			}
		})
		t.Log("marker=idempotency_pg18_legacy_state_actor_tenant_matrix_ok")
	})

	t.Run("UUID representation variants share one canonical actor claim", func(t *testing.T) {
		const scope = "pg18_uuid_canonical"
		const key = "uuid-canonical-key"
		const path = "/pg18/uuid-canonical"
		var calls atomic.Int32
		downstream := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusNoContent)
		})
		entryA, entryB := pg18Entries(poolA, poolB, scope, log, downstream)
		variant := "{" + pg18ActorA1 + "}"
		first := pg18Serve(entryA, pg18TenantA, variant, key, path, nil, true)
		replay := pg18Serve(entryB, pg18TenantA, pg18ActorA1, key, path, nil, true)
		if first.Code != http.StatusNoContent || replay.Code != http.StatusNoContent ||
			replay.Header().Get("Idempotency-Replayed") != "true" || calls.Load() != 1 {
			t.Fatalf("UUID variant status/header/calls = %d/%d/%q/%d",
				first.Code, replay.Code, replay.Header().Get("Idempotency-Replayed"), calls.Load())
		}
		pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
			var count int
			var actorID string
			if err := tx.QueryRow(ctx, `
				SELECT count(*),min(actor_id::text) FROM idempotency_keys
				 WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
				pg18TenantA, pg18ExpectedActorScope(scope, pg18ActorA1), key,
			).Scan(&count, &actorID); err != nil {
				t.Fatalf("query canonical actor row: %v", err)
			}
			if count != 1 || actorID != pg18ActorA1 {
				t.Fatalf("canonical actor row count/id = %d/%s", count, actorID)
			}
		})
		t.Log("marker=idempotency_pg18_uuid_representation_canonicalized_ok")
	})

	t.Run("resource-bound in-flight window returns conflict without takeover", func(t *testing.T) {
		const scope = "pg18_resource_bound_window"
		const key = "resource-bound-window-key"
		const path = "/pg18/resource-bound-window"
		const resourceID = "91000000-0000-7000-8000-000000009999"
		var calls atomic.Int32
		bound := make(chan struct{})
		release := make(chan struct{})
		var boundOnce sync.Once
		var releaseOnce sync.Once
		releaseDownstream := func() { releaseOnce.Do(func() { close(release) }) }
		defer releaseDownstream()
		downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			claim, ok := IdempotencyClaimFrom(r.Context())
			if !ok {
				t.Error("resource-bound handler has no claim")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
				result, err := tx.Exec(ctx, `
					UPDATE idempotency_keys
					   SET resource_type='middleware_test_fixture',resource_id=$2
					 WHERE id=$1 AND status='in_flight'
					   AND resource_type IS NULL AND resource_id IS NULL`,
					claim.ID, resourceID)
				if err != nil {
					t.Fatalf("bind in-flight resource: %v", err)
				}
				if result.RowsAffected() != 1 {
					t.Fatalf("bind in-flight resource rows = %d", result.RowsAffected())
				}
			})
			boundOnce.Do(func() { close(bound) })
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"created":true}`))
		})
		entryA, entryB := pg18Entries(poolA, poolB, scope, log, downstream)
		firstResult := make(chan *httptest.ResponseRecorder, 1)
		firstCtx, firstCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer firstCancel()
		go func() {
			firstResult <- pg18ServeContext(
				firstCtx, entryA, pg18TenantA, pg18ActorA1, key, path, nil, true,
			)
		}()
		select {
		case <-bound:
		case <-time.After(5 * time.Second):
			t.Fatal("first request did not bind its in-flight resource")
		}
		second := pg18Serve(entryB, pg18TenantA, pg18ActorA1, key, path, nil, true)
		if second.Code != http.StatusConflict || calls.Load() != 1 {
			t.Fatalf("resource-bound window status/calls = %d/%d", second.Code, calls.Load())
		}
		pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
			var status, resourceType, observedResourceID string
			var generation int64
			if err := tx.QueryRow(ctx, `
				SELECT status,resource_type,resource_id::text,claim_generation
				  FROM idempotency_keys
				 WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
				pg18TenantA, pg18ExpectedActorScope(scope, pg18ActorA1), key,
			).Scan(&status, &resourceType, &observedResourceID, &generation); err != nil {
				t.Fatalf("query resource-bound in-flight row: %v", err)
			}
			if status != "in_flight" || resourceType != "middleware_test_fixture" ||
				observedResourceID != resourceID || generation != 1 {
				t.Fatalf("resource-bound in-flight row = %s/%s/%s/%d",
					status, resourceType, observedResourceID, generation)
			}
		})
		releaseDownstream()
		select {
		case first := <-firstResult:
			if first.Code != http.StatusCreated {
				t.Fatalf("resource-bound owner status = %d", first.Code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("resource-bound owner did not complete")
		}
		if calls.Load() != 1 {
			t.Fatalf("resource-bound window invoked downstream %d times", calls.Load())
		}
		replay := pg18Serve(entryB, pg18TenantA, pg18ActorA1, key, path, nil, true)
		if replay.Code != http.StatusCreated ||
			replay.Header().Get("Idempotency-Replayed") != "true" ||
			replay.Body.String() != `{"created":true}` || calls.Load() != 1 {
			t.Fatalf("resource-bound replay status/header/body/calls = %d/%q/%q/%d",
				replay.Code, replay.Header().Get("Idempotency-Replayed"),
				replay.Body.String(), calls.Load())
		}
		pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
			var status, resourceType, observedResourceID, responseFormat string
			var generation int64
			if err := tx.QueryRow(ctx, `
				SELECT status,resource_type,resource_id::text,response_format,claim_generation
				  FROM idempotency_keys
				 WHERE tenant_id=$1 AND scope=$2 AND idempotency_key=$3`,
				pg18TenantA, pg18ExpectedActorScope(scope, pg18ActorA1), key,
			).Scan(&status, &resourceType, &observedResourceID, &responseFormat, &generation); err != nil {
				t.Fatalf("query resource-bound terminal row: %v", err)
			}
			if status != "succeeded" || resourceType != "middleware_test_fixture" ||
				observedResourceID != resourceID || responseFormat != "bytes" || generation != 1 {
				t.Fatalf("resource-bound terminal row = %s/%s/%s/%s/%d",
					status, resourceType, observedResourceID, responseFormat, generation)
			}
		})
		t.Log("marker=idempotency_pg18_resource_bound_inflight_409_terminal_replay_preserves_binding_ok")
	})

	t.Run("missing principal writes no row", func(t *testing.T) {
		const scope = "pg18_no_principal"
		const key = "no-principal-key"
		const path = "/pg18/no-principal"
		var calls atomic.Int32
		entryA, _ := pg18Entries(poolA, poolB, scope, log,
			http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		response := pg18Serve(entryA, pg18TenantA, "", key, path, nil, false)
		if response.Code != http.StatusUnauthorized || calls.Load() != 0 {
			t.Fatalf("missing principal status/calls = %d/%d", response.Code, calls.Load())
		}
		pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM idempotency_keys
				WHERE tenant_id=$1 AND idempotency_key=$2`, pg18TenantA, key).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("missing principal wrote %d rows", count)
			}
		})
		t.Log("marker=idempotency_pg18_missing_principal_no_write_ok")
	})

	t.Run("RLS and column ACL", func(t *testing.T) {
		const scope = "pg18_rls_acl"
		const key = "tenant-b-key"
		const path = "/pg18/rls"
		var tenantBClaim IdempotencyClaim
		downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantBClaim, _ = IdempotencyClaimFrom(r.Context())
			w.WriteHeader(http.StatusNoContent)
		})
		_, entryB := pg18Entries(poolA, poolB, scope, log, downstream)
		if got := pg18Serve(entryB, pg18TenantB, pg18ActorB1, key, path, nil, true); got.Code != http.StatusNoContent {
			t.Fatalf("tenant B seed status = %d", got.Code)
		}
		pg18InTx(t, ctx, poolA, pg18TenantA, pg18ActorA1, func(tx pgx.Tx) {
			var id string
			err := tx.QueryRow(ctx, `SELECT id::text FROM idempotency_keys WHERE id=$1`,
				tenantBClaim.ID).Scan(&id)
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("tenant A observed tenant B row: id=%s err=%v", id, err)
			}
		})
		aclErr := poolB.InTx(ctx,
			platformdb.Scope{TenantID: pg18TenantB, ActorID: pg18ActorB1},
			func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE idempotency_keys
					SET idempotency_key='forbidden-identity-rewrite' WHERE id=$1`,
					tenantBClaim.ID)
				return err
			})
		// 身份列改不动这件事有两条防线：列级 GRANT（42501）和 00036 的
		// 证据守卫（23514）。列级白名单已整体放弃——它要求每加一列就同步
		// 两处清单，已经因为漏列害我们下不了单一次——所以现在实际拦住的
		// 是守卫。断言认「被拒绝」，不认拒绝来自哪一层。
		if !platformdb.IsInsufficientPrivilege(aclErr) && !platformdb.IsCheckViolation(aclErr) {
			t.Fatalf("identity column update error = %v, want 42501 or 23514", aclErr)
		}
		t.Log("marker=idempotency_pg18_rls_column_acl_ok")
	})
}

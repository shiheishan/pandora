// [INPUT]: 依赖 platform/db 的租户事务与 idempotency_keys 表（00037 运行期加固），依赖 platform/httpx 的 Principal、错误模型与 PreparedResponse
// [OUTPUT]: 对外提供 Idempotency 中间件、IdempotencyClaim 与 IdempotencyClaimFrom / ValidateIdempotencyClaim、CompleteSuccessJSONInTx、ErrIdempotencyClaimLost
// [POS]: middleware 的幂等键：数据库持有的执行认领（INSERT 新占、SELECT FOR UPDATE 加条件 UPDATE 接管可重试的旧认领），业务处理器用 CompleteSuccessJSONInTx 把业务写入与认领完成放进同一事务；作用域按主体隔离；重放判定与响应头在 idempotency_replay.go，响应录制在 idempotency_recorder.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

const (
	maxIdempotencyBodyBytes       = 1 << 20
	maxIdempotencyResponseBytes   = maxIdempotencyBodyBytes
	storedResponseEnvelopeVersion = "aegis-idempotency-response-v1"
)

const legacyIdempotencyLookupSQL = `
	SELECT 1
	  FROM app.lookup_legacy_idempotency_key($1, $2, $3)`

const insertIdempotencyClaimSQL = `
	INSERT INTO idempotency_keys
		(tenant_id, scope, idempotency_key, request_hash, actor_id, locked_until)
	VALUES ($1, $2, $3, $4, $5, now() + interval '5 minutes')
	ON CONFLICT (tenant_id, scope, idempotency_key) DO NOTHING
	RETURNING id, locked_until, claim_generation`

// The row lock is the ownership boundary for retries under READ COMMITTED.
const selectIdempotencyClaimForUpdateSQL = `
	SELECT id, request_hash, status, actor_id, response_code,
	       response_format, response_body, response_payload,
	       response_content_type, response_location, response_etag,
	       response_cache_control, response_content_language,
	       locked_until, claim_generation, resource_type, resource_id
	  FROM idempotency_keys
	 WHERE tenant_id = $1 AND scope = $2 AND idempotency_key = $3
	 FOR UPDATE`

const takeoverIdempotencyClaimSQL = `
	UPDATE idempotency_keys
	   SET status = 'in_flight',
	       claim_generation = claim_generation + 1,
	       response_code = NULL,
	       response_format = 'none',
	       response_payload = NULL,
	       response_content_type = NULL,
	       response_location = NULL,
	       response_etag = NULL,
	       response_cache_control = NULL,
	       response_content_language = NULL,
	       completed_at = NULL,
	       locked_until = now() + interval '5 minutes'
	 WHERE id = $1 AND tenant_id = $2 AND scope = $3 AND idempotency_key = $4
	   AND actor_id = $5 AND request_hash = $6
	   AND status = 'failed' AND claim_generation = $7
	   AND response_body IS NULL AND response_format IN ('legacy_empty', 'bytes')
	   AND resource_type IS NULL AND resource_id IS NULL
	 RETURNING id, locked_until, claim_generation`

// Generation and lease together prevent an ABA completion from an old owner.
const completeIdempotencyClaimSQL = `
	UPDATE idempotency_keys
	   SET status = $8, response_code = $9, response_format = $10,
	       response_payload = $11,
	       response_content_type = $12, response_location = $13,
	       response_etag = $14, response_cache_control = $15,
	       response_content_language = $16,
	       completed_at = now(), locked_until = NULL
	 WHERE id = $1 AND tenant_id = $2 AND scope = $3 AND idempotency_key = $4
	   AND actor_id = $5 AND request_hash = $6
	   AND app.current_tenant_id() = $2::uuid
	   AND app.current_actor_id() = $5::uuid
	   AND status = 'in_flight' AND claim_generation = $7 AND locked_until = $17
	 RETURNING id`

const probeIdempotencyClaimSQL = `
	SELECT status, response_code, response_format, response_body, response_payload,
	       response_content_type, response_location, response_etag,
	       response_cache_control, response_content_language,
	       locked_until, claim_generation, resource_type, resource_id
	  FROM idempotency_keys
	 WHERE id = $1 AND tenant_id = $2 AND scope = $3 AND idempotency_key = $4
	   AND actor_id = $5 AND request_hash = $6`

type idempotencyClaimContextKey struct{}

// IdempotencyClaim is the logical claim exposed to a business handler. Scope
// is the base scope, not the actor-derived storage scope.
type IdempotencyClaim struct {
	ID           string
	TenantID     string
	ActorID      string
	Scope        string
	StorageScope string
	Key          string
	Generation   int64
	LockedUntil  time.Time
	RequestHash  [sha256.Size]byte
}

var ErrIdempotencyClaimLost = errors.New("idempotency: transaction completion claim lost")

// CompleteSuccessJSONInTx stores the exact successful response in the same
// transaction as the business mutation. It intentionally does not bind a
// business resource: only orders and refunds are accepted by the current
// database resource-binding function. The full claim tuple and lease still
// provide the same stale-owner/ABA protection as the middleware completion.
func CompleteSuccessJSONInTx(
	ctx context.Context,
	tx pgx.Tx,
	claim IdempotencyClaim,
	response httpx.PreparedResponse,
) error {
	if err := ValidateIdempotencyClaim(
		claim, claim.TenantID, claim.ActorID, claim.Scope,
	); err != nil {
		return err
	}
	if response.StatusCode() < http.StatusOK || response.StatusCode() >= http.StatusMultipleChoices {
		return errors.New("idempotency: transaction response must be successful")
	}
	if response.ContentType() != "application/json; charset=utf-8" {
		return errors.New("idempotency: transaction response must be prepared JSON")
	}
	body := response.BodyBytes()
	if len(body) > maxIdempotencyResponseBytes {
		return errors.New("idempotency: transaction response exceeds replay limit")
	}

	var completedID string
	err := tx.QueryRow(ctx, completeIdempotencyClaimSQL,
		claim.ID, claim.TenantID, claim.StorageScope, claim.Key, claim.ActorID,
		claim.RequestHash[:], claim.Generation, "succeeded", response.StatusCode(),
		"bytes", body, response.ContentType(), nil, nil, nil, nil, claim.LockedUntil,
	).Scan(&completedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrIdempotencyClaimLost
	}
	if err != nil {
		return err
	}
	if completedID != claim.ID {
		return errors.New("idempotency: transaction completion returned an unexpected claim id")
	}
	return nil
}

type ownedIdempotencyClaim struct {
	claim IdempotencyClaim
}

func IdempotencyClaimFrom(ctx context.Context) (IdempotencyClaim, bool) {
	claim, ok := ctx.Value(idempotencyClaimContextKey{}).(IdempotencyClaim)
	return claim, ok
}

// ValidateIdempotencyClaim rejects a partial, transplanted, or differently
// scoped claim before a business transaction starts. The database still owns
// the final full-tuple CAS; this check makes accidental handler/service misuse
// fail closed earlier.
func ValidateIdempotencyClaim(
	claim IdempotencyClaim,
	tenantID string,
	actorID string,
	baseScope string,
) error {
	claimID, err := uuid.Parse(claim.ID)
	if err != nil || claimID.String() != claim.ID {
		return errors.New("idempotency: claim id is missing or non-canonical")
	}
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil || tenantUUID.String() != tenantID || claim.TenantID != tenantID {
		return errors.New("idempotency: claim tenant does not match the request tenant")
	}
	actorUUID, err := uuid.Parse(actorID)
	if err != nil || actorUUID.String() != actorID || claim.ActorID != actorID {
		return errors.New("idempotency: claim actor does not match the request actor")
	}
	if err := validateIdempotencyBaseScope(baseScope); err != nil {
		return err
	}
	if claim.Scope != baseScope {
		return errors.New("idempotency: claim base scope does not match the operation")
	}
	wantStorageScope := idempotencyActorScopeCanonical(baseScope, actorID)
	if claim.StorageScope != wantStorageScope {
		return errors.New("idempotency: claim storage scope is incomplete or invalid")
	}
	if !validIdempotencyKey(claim.Key) {
		return errors.New("idempotency: claim key is incomplete or invalid")
	}
	if claim.Generation < 1 || claim.Generation >= 9223372036854775807 {
		return errors.New("idempotency: claim generation is incomplete or invalid")
	}
	if claim.LockedUntil.IsZero() {
		return errors.New("idempotency: claim lease is incomplete")
	}
	return nil
}

func withIdempotencyClaim(ctx context.Context, owned *ownedIdempotencyClaim) context.Context {
	if owned == nil {
		return ctx
	}
	return context.WithValue(ctx, idempotencyClaimContextKey{}, owned.claim)
}

type storedResponseEnvelope struct {
	Envelope           string `json:"_aegis_envelope"`
	BodyBase64         string `json:"body_base64"`
	ContentType        string `json:"content_type,omitempty"`
	ContentTypePresent bool   `json:"content_type_present,omitempty"`
	CaptureComplete    bool   `json:"capture_complete"`
}

type idempotencyReplayHeaders struct {
	ContentType     *string
	Location        *string
	ETag            *string
	CacheControl    *string
	ContentLanguage *string
}

type idempotencyRecord struct {
	status          string
	responseCode    *int
	responseFormat  string
	responseBody    []byte
	responsePayload []byte
	headers         idempotencyReplayHeaders
	lockedUntil     *time.Time
	generation      int64
	resourceType    *string
	resourceID      *string
}

type idempotencyCompletion struct {
	status          string
	responseCode    int
	responseFormat  string
	responsePayload []byte
	headers         idempotencyReplayHeaders
}

// Idempotency implements database-owned execution claims. INSERT owns a new
// claim; SELECT FOR UPDATE plus a guarded UPDATE owns an eligible retry.
func Idempotency(pool *db.Pool, scope string, log *slog.Logger) func(http.Handler) http.Handler {
	if err := validateIdempotencyBaseScope(scope); err != nil {
		panic(err)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get("Idempotency-Key")
			if !validIdempotencyKey(key) {
				httpx.Fail(w, r, log, httpx.New(httpx.CodeBadRequest,
					"此接口要求有效的 Idempotency-Key 请求头"))
				return
			}

			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxIdempotencyBodyBytes))
			if err != nil {
				httpx.Fail(w, r, log, httpx.New(httpx.CodeBadRequest, "读取请求体失败").
					WithInternal(err))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			sum := idempotencyRequestHash(r.Method, idempotencyRequestTarget(r.URL), body)
			hash := sum[:]
			ctx := r.Context()
			tenantID := httpx.TenantIDFrom(ctx)
			principal := httpx.PrincipalFrom(ctx)
			if tenantID == "" || principal.IsAnonymous() || principal.UserID == "" ||
				principal.TenantID != tenantID {
				httpx.Fail(w, r, log, httpx.New(httpx.CodeUnauthorized,
					"该接口要求有效的已认证账号"))
				return
			}
			actorUUID, parseErr := uuid.Parse(principal.UserID)
			if parseErr != nil {
				httpx.Fail(w, r, log, httpx.Internal(fmt.Errorf(
					"idempotency: authenticated actor_id is not a UUID: %w", parseErr,
				)))
				return
			}
			actorID := actorUUID.String()
			storageScope := idempotencyActorScopeCanonical(scope, actorID)
			dbScope := db.Scope{TenantID: tenantID, ActorID: actorID}

			var (
				owned           *ownedIdempotencyClaim
				replayDecision  idempotencyReplayDecision
				conflict        bool
				inFlight        bool
				legacyBlocked   bool
				terminalBlocked bool
			)

			err = pool.InTx(ctx, dbScope, func(tx pgx.Tx) error {
				var legacyExists int
				legacyErr := tx.QueryRow(ctx, legacyIdempotencyLookupSQL,
					tenantID, scope, key).Scan(&legacyExists)
				if legacyErr == nil {
					legacyBlocked = true
					return nil
				}
				if legacyErr != nil && !errors.Is(legacyErr, pgx.ErrNoRows) {
					return legacyErr
				}

				var insertedID string
				var insertedLockedUntil time.Time
				var insertedGeneration int64
				insErr := tx.QueryRow(ctx, insertIdempotencyClaimSQL,
					tenantID, storageScope, key, hash, actorID,
				).Scan(&insertedID, &insertedLockedUntil, &insertedGeneration)
				if insErr == nil {
					owned = newOwnedIdempotencyClaim(
						insertedID, tenantID, actorID, scope, storageScope, key, sum,
						insertedLockedUntil, insertedGeneration,
					)
					return nil
				}
				if !errors.Is(insErr, pgx.ErrNoRows) {
					return insErr
				}

				var existingID string
				var existingHash []byte
				var existingActor *string
				var record idempotencyRecord
				if err := tx.QueryRow(ctx, selectIdempotencyClaimForUpdateSQL,
					tenantID, storageScope, key,
				).Scan(&existingID, &existingHash, &record.status, &existingActor,
					&record.responseCode, &record.responseFormat, &record.responseBody,
					&record.responsePayload, &record.headers.ContentType,
					&record.headers.Location, &record.headers.ETag,
					&record.headers.CacheControl, &record.headers.ContentLanguage,
					&record.lockedUntil, &record.generation, &record.resourceType,
					&record.resourceID); err != nil {
					return err
				}

				if !bytes.Equal(existingHash, hash) {
					conflict = true
					return nil
				}
				if existingActor == nil || *existingActor != actorID {
					return errors.New(
						"idempotency: actor-bound record has missing or mismatched actor_id",
					)
				}

				decision, decisionErr := decideIdempotencyRecord(record)
				if decisionErr != nil {
					return decisionErr
				}
				if decision.replay {
					replayDecision = decision
					return nil
				}
				if decision.terminalBlocked {
					terminalBlocked = true
					return nil
				}

				switch record.status {
				case "in_flight":
					// Expiry is a reconciliation signal, not proof that execution stopped.
					inFlight = true
					return nil
				case "failed":
					if !decision.retryable {
						terminalBlocked = true
						return nil
					}
				default:
					return fmt.Errorf("idempotency: unsupported record status %q", record.status)
				}

				var takeoverID string
				var takeoverLockedUntil time.Time
				var takeoverGeneration int64
				takeoverErr := tx.QueryRow(ctx, takeoverIdempotencyClaimSQL,
					existingID, tenantID, storageScope, key, actorID, hash,
					record.generation,
				).Scan(&takeoverID, &takeoverLockedUntil, &takeoverGeneration)
				if takeoverErr != nil {
					return takeoverErr
				}
				owned = newOwnedIdempotencyClaim(
					takeoverID, tenantID, actorID, scope, storageScope, key, sum,
					takeoverLockedUntil, takeoverGeneration,
				)
				return nil
			})

			if err != nil {
				httpx.Fail(w, r, log, httpx.Internal(err))
				return
			}
			if legacyBlocked {
				log.Warn("legacy idempotency key ownership is unknown",
					slog.String("scope", scope),
					slog.String("request_id", httpx.RequestIDFrom(ctx)))
				httpx.Fail(w, r, log, httpx.New(httpx.CodeIdempotencyReuse,
					"该 Idempotency-Key 属于旧版保护记录，请更换幂等键"))
				return
			}
			if conflict {
				log.Warn("idempotency key reused for a different request",
					slog.String("scope", storageScope),
					slog.String("request_id", httpx.RequestIDFrom(ctx)))
				httpx.Fail(w, r, log, httpx.New(httpx.CodeIdempotencyReuse,
					"该 Idempotency-Key 已用于内容不同的请求"))
				return
			}
			if inFlight || terminalBlocked {
				httpx.Fail(w, r, log, httpx.New(httpx.CodeConflict,
					"同一请求正在处理或需要人工核对，请稍后重试"))
				return
			}
			if replayDecision.replay {
				if err := serveIdempotencyReplayDecision(w, replayDecision); err != nil {
					httpx.Fail(w, r, log, httpx.Internal(err))
				}
				return
			}
			if owned == nil {
				httpx.Fail(w, r, log, httpx.Internal(
					errors.New("idempotency: request reached execution without an owned claim"),
				))
				return
			}

			rec := newIdempotencyRecorder(w)
			ownedCtx := withIdempotencyClaim(ctx, owned)
			next.ServeHTTP(rec, r.WithContext(ownedCtx))
			rec.finish()

			finalStatus := "failed"
			if rec.status >= http.StatusOK && rec.status < http.StatusMultipleChoices {
				finalStatus = "succeeded"
			}
			completion := rec.idempotencyCompletion(finalStatus)

			writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			completionClass := "completed"
			if err := pool.InTx(writeCtx, dbScope, func(tx pgx.Tx) error {
				var completedID string
				err := tx.QueryRow(writeCtx, completeIdempotencyClaimSQL,
					owned.claim.ID, tenantID, storageScope, key, actorID, hash,
					owned.claim.Generation, completion.status, completion.responseCode,
					completion.responseFormat, completion.responsePayload,
					completion.headers.ContentType, completion.headers.Location,
					completion.headers.ETag, completion.headers.CacheControl,
					completion.headers.ContentLanguage, owned.claim.LockedUntil,
				).Scan(&completedID)
				if err == nil {
					return nil
				}
				if !errors.Is(err, pgx.ErrNoRows) {
					return err
				}

				var observed idempotencyRecord
				if err := tx.QueryRow(writeCtx, probeIdempotencyClaimSQL,
					owned.claim.ID, tenantID, storageScope, key, actorID, hash,
				).Scan(&observed.status, &observed.responseCode,
					&observed.responseFormat, &observed.responseBody,
					&observed.responsePayload, &observed.headers.ContentType,
					&observed.headers.Location, &observed.headers.ETag,
					&observed.headers.CacheControl, &observed.headers.ContentLanguage,
					&observed.lockedUntil, &observed.generation,
					&observed.resourceType, &observed.resourceID); err != nil {
					return fmt.Errorf("idempotency completion probe: %w", err)
				}
				completionClass = classifyIdempotencyCompletionProbe(
					observed, owned.claim.Generation, completion,
				)
				if completionClass == "business_completed" {
					return nil
				}
				return fmt.Errorf("idempotency completion CAS rejected: %s", completionClass)
			}); err != nil {
				log.Error("complete idempotency claim failed",
					slog.String("scope", storageScope),
					slog.String("record_id", owned.claim.ID),
					slog.String("classification", completionClass),
					slog.String("error", err.Error()),
					slog.String("request_id", httpx.RequestIDFrom(ctx)))
			}
		})
	}
}

func newOwnedIdempotencyClaim(
	id, tenantID, actorID, scope, storageScope, key string,
	hash [sha256.Size]byte,
	lockedUntil time.Time,
	generation ...int64,
) *ownedIdempotencyClaim {
	claimGeneration := int64(1)
	if len(generation) > 0 {
		claimGeneration = generation[0]
	}
	return &ownedIdempotencyClaim{
		claim: IdempotencyClaim{
			ID: id, TenantID: tenantID, ActorID: actorID, Scope: scope,
			StorageScope: storageScope, Key: key, Generation: claimGeneration,
			LockedUntil: lockedUntil, RequestHash: hash,
		},
	}
}

func idempotencyStatusAllowsTakeover(status string) bool {
	return status == "failed"
}

func idempotencyActorScopeCanonical(scope, canonicalActorID string) string {
	sum := sha256.Sum256([]byte(canonicalActorID))
	return fmt.Sprintf("%s:actor:%x", scope, sum[:12])
}

// idempotencyActorScope remains available to the focused unit tests. Runtime
// callers must canonicalize the UUID before calling the canonical helper.
func idempotencyActorScope(scope string, principal *httpx.Principal) string {
	actor := "anonymous"
	if principal != nil && principal.UserID != "" {
		actor = principal.UserID
	}
	return idempotencyActorScopeCanonical(scope, actor)
}

func validateIdempotencyBaseScope(scope string) error {
	if len(scope) == 0 || len(scope) > 64 {
		return fmt.Errorf("idempotency: base scope must match [a-z0-9_]{1,64}: %q", scope)
	}
	for _, character := range scope {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '_' {
			return fmt.Errorf("idempotency: base scope must match [a-z0-9_]{1,64}: %q", scope)
		}
	}
	return nil
}

func validIdempotencyKey(key string) bool {
	if len(key) == 0 || len(key) > 255 {
		return false
	}
	for index := 0; index < len(key); index++ {
		if key[index] < 0x20 || key[index] == 0x7f {
			return false
		}
	}
	return true
}

func idempotencyRequestTarget(requestURL *url.URL) string {
	target := requestURL.EscapedPath()
	if target == "" {
		target = "/"
	}
	if requestURL.RawQuery != "" {
		target += "?" + requestURL.RawQuery
	}
	return target
}

func idempotencyRequestHash(method, path string, body []byte) [sha256.Size]byte {
	hashInput := make([]byte, 0, len(method)+len(path)+len(body)+2)
	hashInput = append(hashInput, method...)
	hashInput = append(hashInput, '\n')
	hashInput = append(hashInput, path...)
	hashInput = append(hashInput, '\n')
	hashInput = append(hashInput, body...)
	return sha256.Sum256(hashInput)
}

func classifyIdempotencyCompletionProbe(
	record idempotencyRecord,
	expectedGeneration int64,
	expected idempotencyCompletion,
) string {
	decision, err := decideIdempotencyRecord(record)
	if err != nil {
		return "malformed"
	}
	if record.status == "succeeded" && expected.status == "succeeded" &&
		record.generation == expectedGeneration && record.responseCode != nil &&
		*record.responseCode == expected.responseCode &&
		record.responseFormat == expected.responseFormat &&
		bytes.Equal(record.responsePayload, expected.responsePayload) &&
		businessHeadersEqual(record.headers, expected.headers) && decision.replay {
		return "business_completed"
	}
	if record.resourceType != nil || record.resourceID != nil {
		return "resource_bound"
	}
	if record.generation != expectedGeneration {
		return "stale_generation"
	}
	switch record.status {
	case "in_flight":
		return "current_inflight"
	case "failed":
		if record.responseFormat == "unavailable" {
			return "terminal_unavailable"
		}
		return "terminal_failed"
	case "succeeded":
		return "terminal_mismatch"
	default:
		return "malformed"
	}
}

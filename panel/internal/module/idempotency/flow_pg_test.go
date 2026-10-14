package idempotency_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/idempotency/handler"
	"github.com/aegispanel/aegis/internal/module/idempotency/repo"
	"github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type pgErrEnvelope struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func pgEngine(h *handler.Handler, actor uuid.UUID, biz gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(httpx.Errors(), httpx.SecurityHeaders(false))
	r.Use(func(c *gin.Context) {
		authn.Set(c, authn.Principal{UserID: actor})
		c.Next()
	})
	r.POST("/v1/things", h.Middleware("thing_create"), biz)
	return r
}

func pgPost(e *gin.Engine, path, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set(service.HeaderKey, key)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

func pgErrorCode(w *httptest.ResponseRecorder) string {
	var env pgErrEnvelope
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	return env.Error.Code
}

func TestFlowPGFullCycle(t *testing.T) {
	d := testkit.DB(t)
	testkit.Truncate(t, d, "idempotency_keys")
	svc := service.New(repo.New(d), tx.NewManager(d))
	h := handler.New(svc, testFPKey)
	actor := uuid.New()
	var bizCalls int
	e := pgEngine(h, actor, func(c *gin.Context) {
		bizCalls++
		c.JSON(201, gin.H{"id": "a"})
	})
	key := "flow-key-1"
	body := `{"n":1}`

	w := pgPost(e, "/v1/things", body, key)
	if w.Code != 201 || w.Body.String() != `{"id":"a"}` || w.Header().Get(service.HeaderReplayed) != "" {
		t.Fatalf("首次: %d %s replay=%q", w.Code, w.Body.String(), w.Header().Get(service.HeaderReplayed))
	}
	if bizCalls != 1 {
		t.Fatalf("业务调用 %d", bizCalls)
	}

	w = pgPost(e, "/v1/things", body, key)
	if w.Code != 201 || w.Body.String() != `{"id":"a"}` || w.Header().Get(service.HeaderReplayed) != "true" {
		t.Fatalf("重放: %d %s", w.Code, w.Body.String())
	}
	if bizCalls != 1 {
		t.Fatalf("重放后业务仍应 1 次，得到 %d", bizCalls)
	}

	w = pgPost(e, "/v1/things", `{"n":2}`, key)
	if w.Code != 409 || pgErrorCode(w) != "idempotency_key_reuse" {
		t.Fatalf("不同 body: %d %s", w.Code, w.Body.String())
	}

	var status string
	var generation int64
	if err := d.Base().Raw("SELECT status, generation FROM idempotency_keys WHERE idempotency_key = ?", key).
		Row().Scan(&status, &generation); err != nil {
		t.Fatalf("查行: %v", err)
	}
	if status != "succeeded" || generation != 1 {
		t.Fatalf("行 status=%q generation=%d", status, generation)
	}
}

func TestFlowPGRetryAfterFail(t *testing.T) {
	d := testkit.DB(t)
	testkit.Truncate(t, d, "idempotency_keys")
	svc := service.New(repo.New(d), tx.NewManager(d))
	h := handler.New(svc, testFPKey)
	actor := uuid.New()
	var bizCalls int
	e := pgEngine(h, actor, func(c *gin.Context) {
		bizCalls++
		if bizCalls == 1 {
			httpx.Fail(c, errs.New(errs.Conflict))
			return
		}
		c.JSON(201, gin.H{"id": "ok"})
	})
	key := "retry-key"
	body := `{"n":1}`

	w := pgPost(e, "/v1/things", body, key)
	if w.Code != 409 || pgErrorCode(w) != "conflict" {
		t.Fatalf("第一次: %d %s", w.Code, w.Body.String())
	}
	w = pgPost(e, "/v1/things", body, key)
	if w.Code != 201 {
		t.Fatalf("第二次: %d %s", w.Code, w.Body.String())
	}
	if bizCalls != 2 {
		t.Fatalf("业务应 2 次，得到 %d", bizCalls)
	}
	var status string
	var generation int64
	if err := d.Base().Raw("SELECT status, generation FROM idempotency_keys WHERE idempotency_key = ?", key).
		Row().Scan(&status, &generation); err != nil {
		t.Fatalf("查行: %v", err)
	}
	if status != "succeeded" || generation != 2 {
		t.Fatalf("行 status=%q generation=%d", status, generation)
	}
}

func TestFlowPGActorIsolation(t *testing.T) {
	d := testkit.DB(t)
	testkit.Truncate(t, d, "idempotency_keys")
	svc := service.New(repo.New(d), tx.NewManager(d))
	h := handler.New(svc, testFPKey)
	key := "shared-key"

	actors := []uuid.UUID{uuid.New(), uuid.New()}
	bodies := []string{`{"who":"a"}`, `{"who":"b"}`}
	for i := 0; i < 2; i++ {
		var bizCalls int
		e := pgEngine(h, actors[i], func(c *gin.Context) {
			bizCalls++
			c.JSON(201, gin.H{"ok": true})
		})
		w := pgPost(e, "/v1/things", bodies[i], key)
		if w.Code != 201 || bizCalls != 1 {
			t.Fatalf("actor %d: %d calls=%d", i, w.Code, bizCalls)
		}
	}
}

func TestFlowPGBusinessComplete(t *testing.T) {
	d := testkit.DB(t)
	testkit.Truncate(t, d, "idempotency_keys")
	svc := service.New(repo.New(d), tx.NewManager(d))
	h := handler.New(svc, testFPKey)
	actor := uuid.New()
	e := pgEngine(h, actor, func(c *gin.Context) {
		claim, ok := service.ClaimFrom(c.Request.Context())
		if !ok {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		resp, err := service.JSON(201, map[string]string{"id": "biz"})
		if err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		if err := svc.Complete(c.Request.Context(), claim, resp); err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		resp.Serve(c.Writer)
	})
	key := "biz-complete"
	w := pgPost(e, "/v1/things", "{}", key)
	if w.Code != 201 || w.Body.String() != `{"id":"biz"}` {
		t.Fatalf("首次: %d %s", w.Code, w.Body.String())
	}
	w = pgPost(e, "/v1/things", "{}", key)
	if w.Code != 201 || w.Body.String() != `{"id":"biz"}` || w.Header().Get(service.HeaderReplayed) != "true" {
		t.Fatalf("重放: %d %s", w.Code, w.Body.String())
	}
	var status string
	var generation int64
	if err := d.Base().Raw("SELECT status, generation FROM idempotency_keys WHERE idempotency_key = ?", key).
		Row().Scan(&status, &generation); err != nil {
		t.Fatalf("查行: %v", err)
	}
	if status != "succeeded" || generation != 1 {
		t.Fatalf("行 status=%q generation=%d", status, generation)
	}
}

func TestFlowPGConcurrent(t *testing.T) {
	d := testkit.DB(t)
	testkit.Truncate(t, d, "idempotency_keys")
	svc := service.New(repo.New(d), tx.NewManager(d))
	h := handler.New(svc, testFPKey)
	actor := uuid.New()
	var bizCalls int32
	e := pgEngine(h, actor, func(c *gin.Context) {
		atomic.AddInt32(&bizCalls, 1)
		time.Sleep(200 * time.Millisecond)
		c.JSON(201, gin.H{"id": "c"})
	})
	key := "concurrent-key"
	body := `{"n":1}`

	var wg sync.WaitGroup
	results := make([]*httptest.ResponseRecorder, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx] = pgPost(e, "/v1/things", body, key)
		}(i)
	}
	wg.Wait()

	if atomic.LoadInt32(&bizCalls) != 1 {
		t.Fatalf("业务应恰好 1 次，得到 %d", bizCalls)
	}
	var fresh201, replay201, conflict409 int
	for _, w := range results {
		if w == nil {
			t.Fatal("nil response")
		}
		switch {
		case w.Code == 201 && w.Header().Get(service.HeaderReplayed) != "true":
			fresh201++
		case w.Code == 201 && w.Header().Get(service.HeaderReplayed) == "true":
			replay201++
		case w.Code == 409 && pgErrorCode(w) == "conflict":
			conflict409++
		default:
			t.Fatalf("意外响应 %d body=%s code=%q replay=%q",
				w.Code, w.Body.String(), pgErrorCode(w), w.Header().Get(service.HeaderReplayed))
		}
	}
	if fresh201 != 1 {
		t.Fatalf("非重放 201 应 1 个，得到 fresh=%d replay=%d conflict=%d", fresh201, replay201, conflict409)
	}
	if replay201+conflict409 != 4 {
		t.Fatalf("其余应为重放或 conflict: fresh=%d replay=%d conflict=%d", fresh201, replay201, conflict409)
	}
}

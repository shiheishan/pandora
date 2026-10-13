package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func setupRealtimeEngine(h *Handler, auth bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(httpx.Errors())
	k := route.Kit{
		RequireAuth: func(c *gin.Context) {
			if auth {
				authn.Set(c, authn.Principal{
					UserID:    uuid.MustParse("018f0000-0000-7000-8000-000000000001"),
					SessionID: "sid-test",
					Audience:  authn.AudiencePublic,
				})
			}
			c.Next()
		},
	}
	h.RegisterPublic(e.Group("/v1"), k)
	return e
}

func errCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body.Error.Code
}

func TestPublicEventsUnauthorized(t *testing.T) {
	h := New(realtime.NewHub(nil, nil, 10), time.Minute)
	e := setupRealtimeEngine(h, false)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/events", nil))
	if rec.Code != http.StatusUnauthorized || errCode(t, rec) != "unauthorized" {
		t.Fatalf("status %d code %s", rec.Code, errCode(t, rec))
	}
}

func TestPublicEventsHubNil(t *testing.T) {
	h := New(nil, time.Minute)
	e := setupRealtimeEngine(h, true)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/events", nil))
	if rec.Code != http.StatusServiceUnavailable || errCode(t, rec) != "service_unavailable" {
		t.Fatalf("status %d code %s", rec.Code, errCode(t, rec))
	}
}

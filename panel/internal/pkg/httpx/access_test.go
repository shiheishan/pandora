package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/reqstats"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestAccessLogRequestStats(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(RequestID(zap.New(core)), AccessLog())
	e.GET("/x", func(c *gin.Context) {
		st := reqstats.From(c.Request.Context())
		st.AddSQL(2 * time.Millisecond)
		st.AddSQL(2 * time.Millisecond)
		st.AddRedis(3)
		OK(c, gin.H{})
	})

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	entries := logs.FilterMessage("access").All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 access log, got %d", len(entries))
	}
	m := entries[0].ContextMap()
	if m["sql_n"] != int64(2) {
		t.Fatalf("sql_n: got %v want 2", m["sql_n"])
	}
	if m["sql_ms"] != float64(4) {
		t.Fatalf("sql_ms: got %v want 4", m["sql_ms"])
	}
	if m["redis_n"] != int64(3) {
		t.Fatalf("redis_n: got %v want 3", m["redis_n"])
	}
}

func TestAccessLogRequestStatsZero(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.Use(RequestID(zap.New(core)), AccessLog())
	e.GET("/y", func(c *gin.Context) {
		OK(c, gin.H{})
	})

	req := httptest.NewRequest(http.MethodGet, "/y", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	entries := logs.FilterMessage("access").All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 access log, got %d", len(entries))
	}
	m := entries[0].ContextMap()
	if m["sql_n"] != int64(0) {
		t.Fatalf("sql_n: got %v want 0", m["sql_n"])
	}
	if m["sql_ms"] != float64(0) {
		t.Fatalf("sql_ms: got %v want 0", m["sql_ms"])
	}
	if m["redis_n"] != int64(0) {
		t.Fatalf("redis_n: got %v want 0", m["redis_n"])
	}
}

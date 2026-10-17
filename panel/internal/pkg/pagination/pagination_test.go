package pagination

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/gin-gonic/gin"
)

func parseQuery(t *testing.T, query string, def, max int) (limit, offset int, err error) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/?"+query, nil)
	return Parse(c, def, max)
}

func TestParseDefaults(t *testing.T) {
	limit, offset, err := parseQuery(t, "", 20, 100)
	if err != nil || limit != 20 || offset != 0 {
		t.Fatalf("limit=%d offset=%d err=%v", limit, offset, err)
	}
}

func TestParseClampMax(t *testing.T) {
	limit, _, err := parseQuery(t, "limit=500", 20, 100)
	if err != nil || limit != 100 {
		t.Fatalf("limit=%d err=%v", limit, err)
	}
}

func TestParseInvalidLimit(t *testing.T) {
	_, _, err := parseQuery(t, "limit=0", 20, 100)
	if err == nil {
		t.Fatal("应失败")
	}
	e := errs.As(err)
	if e.Code != errs.ValidationFailed || e.Fields["limit"] == "" {
		t.Fatalf("%v", e)
	}
}

func TestParseInvalidOffset(t *testing.T) {
	_, _, err := parseQuery(t, "offset=-1", 20, 100)
	if err == nil {
		t.Fatal("应失败")
	}
	e := errs.As(err)
	if e.Fields["offset"] == "" {
		t.Fatalf("%v", e)
	}
}

func TestParseNonInteger(t *testing.T) {
	_, _, err := parseQuery(t, "limit=abc", 20, 100)
	if err == nil {
		t.Fatal("应失败")
	}
}

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRobots(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for _, tc := range []struct {
		name string
		meta fakeMeta
		want string
	}{
		{
			name: "deny when not indexing",
			meta: fakeMeta{AllowIndexing: false},
			want: robotsDenyAll,
		},
		{
			name: "allow home when indexing",
			meta: fakeMeta{AllowIndexing: true},
			want: robotsAllowHome,
		},
	} {
		r := gin.New()
		r.GET("/robots.txt", Robots(tc.meta))
		req := httptest.NewRequest(http.MethodGet, "/robots.txt", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", tc.name, rec.Code)
		}
		ct := rec.Header().Get("Content-Type")
		if !strings.HasPrefix(ct, "text/plain") {
			t.Fatalf("%s: content-type %q", tc.name, ct)
		}
		body := rec.Body.String()
		if body != tc.want {
			t.Fatalf("%s: body\n got %q\nwant %q", tc.name, body, tc.want)
		}
		for _, forbidden := range []string{"admin", "sub"} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("%s: body must not contain %q: %q", tc.name, forbidden, body)
			}
		}
	}
}

func TestRobotsDenyAll(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/robots.txt", RobotsDenyAll())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/robots.txt", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "User-agent: *\nDisallow: /\n" {
		t.Fatalf("后台 robots.txt: %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("content-type %q", ct)
	}
}

func TestRobotsAllowHomeBody(t *testing.T) {
	if robotsAllowHome != "User-agent: *\nAllow: /$\nAllow: /assets/\nDisallow: /\n" {
		t.Fatalf("允许收录时的正文变了: %q", robotsAllowHome)
	}
}

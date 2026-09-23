package admin

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestDashboardTrafficQuery(t *testing.T) {
	for _, tc := range []struct {
		raw          string
		wantRange    string
		wantLimit    int
		wantSnapshot string
		wantCode     httpx.Code
	}{
		{"", "", 0, "", ""},
		{"?range=30d&limit=20&snapshot_at=2026-07-30T08%3A00%3A00.000000Z", "30d", 20, "2026-07-30T08:00:00.000000Z", ""},
		{"?limit=ten", "", 0, "", httpx.CodeValidationFailed},
	} {
		r := httptest.NewRequest("GET", "http://admin.invalid/v1/dashboard/traffic/nodes"+tc.raw, nil)
		got, err := dashboardTrafficQuery(r)
		if tc.wantCode != "" {
			he, ok := err.(*httpx.Error)
			if !ok || he.Code != tc.wantCode {
				t.Fatalf("%s: error=%v", tc.raw, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if got.Range != tc.wantRange || got.Limit != tc.wantLimit || got.SnapshotAt != tc.wantSnapshot {
			t.Fatalf("%s: %#v", tc.raw, got)
		}
	}
}

func TestDashboardUserTrafficRejectsIdentityParameter(t *testing.T) {
	for _, raw := range []string{"identity=full", "identity=masked", "identity="} {
		r := httptest.NewRequest("GET", "http://admin.invalid/v1/dashboard/traffic/users?"+raw, nil)
		w := httptest.NewRecorder()
		h := &handlers{d: Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
		h.dashboardUserTraffic(w, r)
		if w.Code != 422 {
			t.Fatalf("%s: status=%d body=%s", raw, w.Code, w.Body.String())
		}
		if got := w.Body.String(); !strings.Contains(got, string(httpx.CodeValidationFailed)) {
			t.Fatalf("%s: body=%s", raw, got)
		}
	}
}

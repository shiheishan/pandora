package node

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLegacyBootstrapFailsClosedWithoutTouchingNodeService(t *testing.T) {
	h := &handlers{d: Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	req := httptest.NewRequest(http.MethodPost, "https://panel.test/v1/nodes/bootstrap",
		strings.NewReader(`{"token":"must-not-be-consumed"}`))
	rec := httptest.NewRecorder()
	h.legacyBootstrapDisabled(rec, req)
	if rec.Code != http.StatusUpgradeRequired {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUpgradeRequired)
	}
	if !strings.Contains(rec.Body.String(), `"code":"upgrade_required"`) {
		t.Fatalf("response is not the httpx error envelope: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/v1/nodes/enrollments") {
		t.Fatalf("response does not identify the replacement contract: %s", rec.Body.String())
	}
}

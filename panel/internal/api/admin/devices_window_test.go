package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 设备识别窗口只收 5 / 10 / 30 / 60（R103）；其他值在碰库之前回 422 并标出字段。
func TestSetDeviceModeRejectsUnknownWindow(t *testing.T) {
	h := &handlers{d: Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	for _, window := range []string{"0", "4", "7", "15", "61", "-5"} {
		req := httptest.NewRequest(http.MethodPost, "/v1/settings/device-limit",
			strings.NewReader(`{"mode":"strict","window_minutes":`+window+`}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.setDeviceMode(w, req)
		var env struct {
			Error struct {
				Code   string            `json:"code"`
				Fields map[string]string `json:"fields"`
			} `json:"error"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &env)
		if w.Code != http.StatusUnprocessableEntity || env.Error.Fields["window_minutes"] == "" {
			t.Fatalf("window %s = %d %s, want 422 fields.window_minutes", window, w.Code, w.Body)
		}
	}
}

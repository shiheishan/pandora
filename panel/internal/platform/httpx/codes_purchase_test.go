package httpx

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// quote_changed 与 order_pending 都是 409、各有自己的码：前端按码决定是重新报价，
// 还是显示那张待付款单。漏登记状态映射的话 Fail 会写成 500。
func TestPurchaseConflictCodesAre409WithOwnCode(t *testing.T) {
	for _, code := range []Code{CodeQuoteChanged, CodeOrderPending} {
		w := httptest.NewRecorder()
		Fail(w, httptest.NewRequest(http.MethodPost, "/v1/x", nil),
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			New(code, "金额刚变了，请再确认一次"))
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), `"code":"`+string(code)+`"`) {
			t.Fatalf("%s: status=%d body=%s", code, w.Code, w.Body.String())
		}
	}
}

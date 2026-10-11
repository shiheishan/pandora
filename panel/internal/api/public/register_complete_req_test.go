package public

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRegisterCompleteRejectsInviteCode 钉住注册完成不再收 invite_code。
//
// httpx.DecodeJSON 开了 DisallowUnknownFields。字段从 registerCompleteReq 去掉之后，
// 请求体再带 invite_code 会在进 CompleteRegistration 之前被拒成 400。
// 口令故意不够长：不带该字段时解码通过，在进库之前被 422 拦住，用来和 400 区分。
func TestRegisterCompleteRejectsInviteCode(t *testing.T) {
	h := &handlers{d: Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	call := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/auth/register/complete", strings.NewReader(body))
		w := httptest.NewRecorder()
		h.registerComplete(w, req)
		return w
	}

	without := call(`{"registration_token":"tok","code":"123456","password":"short"}`)
	if without.Code != http.StatusUnprocessableEntity || !strings.Contains(without.Body.String(), `"password"`) {
		t.Fatalf("without invite_code: status=%d body=%s", without.Code, without.Body.String())
	}

	with := call(`{"registration_token":"tok","code":"123456","password":"short","invite_code":"INVITE"}`)
	if with.Code != http.StatusBadRequest || !strings.Contains(with.Body.String(), `"bad_request"`) {
		t.Fatalf("with invite_code: status=%d body=%s", with.Code, with.Body.String())
	}
}

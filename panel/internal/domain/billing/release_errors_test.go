// [INPUT]: 依赖 release.go 的 releaseConflict / releaseHTTPError 与三个释放哨兵错误，依赖 platform/httpx 的错误模型
// [OUTPUT]: 对外提供 TestReleaseHTTPErrorSpeaksChinese
// [POS]: billing 释放错误的单元测试（R95）：取消接口回中文 message 与原状态码，过期任务仍能用 errors.Is 认出冲突
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestReleaseHTTPErrorSpeaksChinese(t *testing.T) {
	for name, tc := range map[string]struct {
		err     error
		code    httpx.Code
		message string
	}{
		"already cancelled": {releaseConflict{releasedStatusMessage("cancelled")}, httpx.CodeConflict, "订单已取消"},
		"already expired":   {releaseConflict{releasedStatusMessage("expired")}, httpx.CodeConflict, "订单已过期"},
		"paid":              {fmt.Errorf("wrapped: %w", releaseConflict{"订单已支付，不能取消"}), httpx.CodeConflict, "订单已支付，不能取消"},
		"payment evidence":  {errOrderReleasePaymentEvidence, httpx.CodeConflict, "这张订单已有入账，不能取消"},
		"state changed":     {httpx.New(httpx.CodeConflict, "订单状态已变化，请刷新后再操作"), httpx.CodeConflict, "订单状态已变化，请刷新后再操作"},
		"not found":         {errOrderReleaseNotFound, httpx.CodeNotFound, ""},
	} {
		var he *httpx.Error
		if !errors.As(releaseHTTPError(tc.err), &he) || he.Code != tc.code ||
			(tc.message != "" && he.Message != tc.message) {
			t.Errorf("%s: got %+v, want %s %q", name, he, tc.code, tc.message)
		}
	}
	// 过期任务靠 errors.Is 把冲突当正常空转：中文化不能断了这条判断
	if !errors.Is(fmt.Errorf("expire: %w", releaseConflict{"订单已取消"}), errOrderReleaseConflict) {
		t.Fatal("releaseConflict is no longer recognised as errOrderReleaseConflict")
	}
	var he *httpx.Error
	if !errors.As(releaseHTTPError(errors.New("boom")), &he) || he.Code != httpx.CodeInternal {
		t.Fatalf("unknown error mapped to %+v, want 500", he)
	}
}

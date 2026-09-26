package billing

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/payment"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 缺陷 19：渠道被停用时发起支付回 500。停用是运营状态，要回可展示的 503。
func TestProviderLookupErrorTranslatesDisabledProvider(t *testing.T) {
	err := providerLookupError(fmt.Errorf("支付渠道 %q: %w", "epay", payment.ErrProviderDisabled))
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeUnavailable {
		t.Fatalf("disabled provider error=%v, want service_unavailable", err)
	}

	other := errors.New("未知的支付适配器")
	if got := providerLookupError(other); got != other {
		t.Fatalf("unrelated errors must pass through unchanged, got %v", got)
	}
	if providerLookupError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

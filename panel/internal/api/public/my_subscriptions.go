// [INPUT]: 依赖 domain/subscription 的 MySubscriptions（SQL 与行形状都在那里），依赖 platform/httpx
// [OUTPUT]: 对外提供 handlers.listSubscriptions
// [POS]: api/public 的「我的订阅」（契约门户-02 GET v1/me/subscriptions）：从 handlers.go 拆出，读模型在 subscription/my_subscriptions.go；订阅带设备、配额周期、重置、可续费与续费价、流量包余量
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"net/http"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type mySubscriptionsResponse struct {
	Subscriptions []subscription.MySubscription `json:"subscriptions"`
}

func (h *handlers) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpx.PrincipalFrom(ctx)
	out, err := h.d.Subscription.MySubscriptions(ctx, p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}

	httpx.OK(w, mySubscriptionsResponse{Subscriptions: out})
}

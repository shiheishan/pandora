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

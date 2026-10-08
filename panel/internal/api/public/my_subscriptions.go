package public

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type mySubscriptionsResponse struct {
	Subscriptions []subscription.MySubscription `json:"subscriptions"`
	// UnattachedPackBytes 是还没加到任何一份的流量包余量（购买模型统一 2.9）
	UnattachedPackBytes int64 `json:"unattached_pack_bytes"`
}

func (h *handlers) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := httpx.PrincipalFrom(ctx)
	out, err := h.d.Subscription.MySubscriptions(ctx, p.TenantID, p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}

	httpx.OK(w, mySubscriptionsResponse{Subscriptions: out.Subscriptions,
		UnattachedPackBytes: out.UnattachedPackBytes})
}

type renameSubscriptionRequest struct {
	// Label 为 null、空串或只有空白表示清除备注名
	Label *string `json:"label"`
}

type renameSubscriptionResponse struct {
	Label      *string `json:"label"`
	ClientName string  `json:"client_name"`
}

// renameSubscription 给本人的一份订阅起名、改名或清除名字（PATCH v1/me/subscriptions/{id}）。
//
// 名字规则只在 purchase.NormalizeLabel（422，字段 label）；与本人另一份重名回 409；
// 不是本人的回 404。改名不改节点名单、不推进下发纪元，所以不通知节点；门户其它标签页
// 经 subscriptions 表的变更推送刷新。
func (h *handlers) renameSubscription(w http.ResponseWriter, r *http.Request) {
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
		return
	}
	var req renameSubscriptionRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Subscription.SetLabel(r.Context(), p.TenantID, p.UserID, chi.URLParam(r, "id"), req.Label)
	if err != nil {
		if errors.Is(err, subscription.ErrNotFound) {
			httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
			return
		}
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, renameSubscriptionResponse{Label: out.Label, ClientName: out.ClientName})
}

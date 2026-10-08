package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 服务器级绑定的后台接口（合约 docs/server-binding-contract.md；P1）：生成绑定命令、
// 查看绑定状态、解除绑定。用例与事务都在 nodefabric，这里只解析请求与写响应。

// serverIssueBindingToken 给一台服务器生成一次性绑定令牌（1 小时）与绑定命令。
//
// 网关证书 SPKI 钉住属于 P4：在那之前不传钉住值，命令里不带 --pin，按系统 CA 校验。
func (h *handlers) serverIssueBindingToken(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := validateServerID(id); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	panelURL, err := h.d.Cfg.CanonicalPublicOrigin()
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	out, err := h.d.Node.IssueServerBindingToken(r.Context(), httpx.TenantIDFrom(r.Context()),
		nodefabric.IssueServerBindingTokenInput{
			ActorID:  httpx.PrincipalFrom(r.Context()).UserID,
			ServerID: id,
			PanelURL: panelURL,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.Created(w, out)
}

// serverBinding 读一台服务器的绑定状态：unbound / binding / bound / revoked。
func (h *handlers) serverBinding(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := validateServerID(id); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Node.ServerBinding(r.Context(), httpx.TenantIDFrom(r.Context()), id)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

// serverUnbind 解除绑定：吊销服务器身份，这台机器之后的服务器签名请求一律 401。
func (h *handlers) serverUnbind(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := validateServerID(id); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Node.UnbindServer(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, id)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

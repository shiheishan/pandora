package public

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 用户自助的四个小接口（对标 Xboard user/transfer、ticket/withdraw、
// getActiveSession、removeActiveSession）。
//
// 单独放一个文件：它们互相无关，只是都属于「用户自己能做的事」，
// 塞进 handlers.go 会让那个已经很长的文件更难找东西。

type transferCommissionReq struct {
	Amount int64 `json:"amount"` // 最小货币单位
}

// transferCommission 把可提现佣金转成余额。
//
// 提现要审批、要等打款、有最低门槛；而多数人只是想拿佣金续费。
// 这条路让那笔钱直接回到站内，省掉一圈银行。
func (h *handlers) transferCommission(w http.ResponseWriter, r *http.Request) {
	var req transferCommissionReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	txnID, err := h.d.Billing.TransferCommissionToBalance(r.Context(),
		httpx.TenantIDFrom(r.Context()), p.UserID, req.Amount)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"ledger_txn_id": txnID, "amount": req.Amount})
}

func (h *handlers) listMySessions(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	rows, err := h.d.Identity.ListActiveSessions(r.Context(),
		httpx.TenantIDFrom(r.Context()), p.UserID, p.SessionID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"sessions": rows})
}

func (h *handlers) revokeMySession(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Identity.RevokeSession(r.Context(),
		httpx.TenantIDFrom(r.Context()), p.UserID,
		chi.URLParam(r, "id"), p.SessionID); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"revoked": true})
}

type withdrawTicketReq struct {
	Reason string `json:"reason"`
}

// withdrawTicket 撤回自己提的工单。
//
// 与「关闭」的区别：关闭是「问题解决了」，撤回是「我不需要了，当我没提过」。
// 两者在统计上应当分开 —— 把撤回算进已解决，客服的解决率就是假的。
func (h *handlers) withdrawTicket(w http.ResponseWriter, r *http.Request) {
	var req withdrawTicketReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Support.WithdrawByUser(r.Context(),
		httpx.TenantIDFrom(r.Context()), p.UserID,
		chi.URLParam(r, "id"), req.Reason); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"withdrawn": true})
}

// issueQuickLogin 生成一条 60 秒有效的免密登录链接。
func (h *handlers) issueQuickLogin(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	out, err := h.d.Identity.IssueQuickLogin(r.Context(),
		httpx.TenantIDFrom(r.Context()), p.UserID, p.SessionID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

type quickLoginReq struct {
	Token string `json:"token"`
}

// quickLogin 用链接里的令牌换一套正常凭证。
//
// 这是免鉴权入口 —— 调用它的人本来就还没登录。
func (h *handlers) quickLogin(w http.ResponseWriter, r *http.Request) {
	var req quickLoginReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Identity.ConsumeQuickLogin(r.Context(),
		httpx.TenantIDFrom(r.Context()), req.Token,
		r.UserAgent(), crypto.HashIdentifier(h.d.Cfg.MasterKey, httpx.ClientIP(r)))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{
		"access_token":  out.AccessToken,
		"refresh_token": out.RefreshToken,
		"expires_in":    out.ExpiresIn,
	})
}

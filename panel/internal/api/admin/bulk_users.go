package admin

import (
	"encoding/csv"
	"net/http"
	"strconv"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 用户批量运营的 HTTP 层。
//
// 三个动作共用同一份筛选条件结构 —— 预览说的「会影响 300 人」
// 和导出/群发实际命中的必须是同一批人。

type bulkFilterReq struct {
	Status       string `json:"status"`
	GroupID      string `json:"group_id"`
	Query        string `json:"query"`
	HasActiveSub *bool  `json:"has_active_sub"`
}

func (r bulkFilterReq) toFilter() adminops.BulkFilter {
	return adminops.BulkFilter{
		Status: r.Status, GroupID: r.GroupID, Query: r.Query,
		HasActiveSub: r.HasActiveSub,
	}
}

// previewBulkUsers 先告诉管理员这一批是多少人、都有谁。
//
// 群发和导出都不可撤销：信发出去收不回来，导出的名单落到本地就不知道
// 会流去哪。所以两者之前都该先看清影响面。
func (h *handlers) previewBulkUsers(w http.ResponseWriter, r *http.Request) {
	var req bulkFilterReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	out, err := h.d.Ops.PreviewBulk(r.Context(), httpx.TenantIDFrom(r.Context()),
		req.toFilter())
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

// exportUsers 导出 CSV。
//
// 加 BOM 是因为 Excel 打开无 BOM 的 UTF-8 CSV 会把中文显示成乱码，
// 而运营几乎一定会用 Excel 打开它。
func (h *handlers) exportUsers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	var hasSub *bool
	if v := q.Get("has_active_sub"); v == "true" || v == "false" {
		b := v == "true"
		hasSub = &b
	}
	rows, err := h.d.Ops.ExportUsers(r.Context(), httpx.TenantIDFrom(r.Context()),
		adminops.BulkFilter{
			Status: q.Get("status"), GroupID: q.Get("group_id"),
			Query: q.Get("query"), HasActiveSub: hasSub,
		}, limit)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="users.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})

	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"邮箱", "状态", "分组", "生效订阅", "订单数",
		"累计实付", "注册时间", "最近登录"})
	for _, u := range rows {
		last := ""
		if u.LastLoginAt != nil {
			last = u.LastLoginAt.Format("2006-01-02 15:04")
		}
		_ = cw.Write([]string{
			u.Email, u.Status, u.GroupName,
			strconv.Itoa(u.ActiveSubs), strconv.Itoa(u.TotalOrders),
			strconv.FormatFloat(float64(u.PaidAmount)/100, 'f', 2, 64),
			u.CreatedAt.Format("2006-01-02 15:04"), last,
		})
	}
}

type generateUsersReq struct {
	Count       int    `json:"count"`
	EmailPrefix string `json:"email_prefix"`
	EmailDomain string `json:"email_domain"`
	GroupID     string `json:"group_id"`
	Reason      string `json:"reason"`
}

// generateUsers 批量造账号，供经销商或线下渠道预制交付。
//
// 响应里带明文口令，且只有这一次 —— 库里存的是哈希，之后无从取回。
func (h *handlers) generateUsers(w http.ResponseWriter, r *http.Request) {
	var req generateUsersReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	users, err := h.d.Ops.GenerateUsers(r.Context(), httpx.TenantIDFrom(r.Context()),
		adminops.GenerateUsersInput{
			Count: req.Count, EmailPrefix: req.EmailPrefix,
			EmailDomain: req.EmailDomain, GroupID: req.GroupID,
			Reason: req.Reason, ActorID: p.UserID,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{
		"count": len(users), "users": users,
		"warning": "口令只在这一次返回，关闭后无法再查。请立即保存。",
	})
}

type bulkMailReq struct {
	bulkFilterReq
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (h *handlers) sendBulkMail(w http.ResponseWriter, r *http.Request) {
	var req bulkMailReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	out, err := h.d.Ops.SendBulkMail(r.Context(), httpx.TenantIDFrom(r.Context()),
		adminops.BulkMailInput{
			Filter: req.bulkFilterReq.toFilter(), Subject: req.Subject,
			Body: req.Body, ActorID: p.UserID,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

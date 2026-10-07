package admin

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 用户批量运营的 HTTP 层。
//
// 三个动作共用同一份筛选条件结构 —— 预览说的「会影响 300 人」
// 和导出/群发实际命中的必须是同一批人。

type bulkFilterReq struct {
	Status            string `json:"status"`
	GroupID           string `json:"group_id"`
	Query             string `json:"query"`
	HasActiveSub      *bool  `json:"has_active_sub"`
	PlanID            string `json:"plan_id"`
	ExpiresWithinDays int    `json:"expires_within_days"`
	SubState          string `json:"sub_state"`
}

func (r bulkFilterReq) toFilter() adminops.BulkFilter {
	return adminops.BulkFilter{
		Status: r.Status, GroupID: r.GroupID, Query: r.Query,
		HasActiveSub: r.HasActiveSub, PlanID: r.PlanID,
		ExpiresWithinDays: r.ExpiresWithinDays, SubState: r.SubState,
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
	expires := 0
	if v := q.Get("expires_within_days"); v != "" {
		// 不是整数时按越界处理，交给同一处校验回 422
		n, err := strconv.Atoi(v)
		if err != nil {
			n = -1
		}
		expires = n
	}
	rows, err := h.d.Ops.ExportUsers(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, adminops.BulkFilter{
			Status: q.Get("status"), GroupID: q.Get("group_id"),
			Query: q.Get("query"), HasActiveSub: hasSub, PlanID: q.Get("plan_id"),
			ExpiresWithinDays: expires, SubState: q.Get("sub_state"),
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

// generateUsers 登记一个批量生成账号的后台任务：POST v1/users/bulk/generate，回 202 与任务。
//
// 口令由 aegis-admin 的 worker 逐个生成（一次只占 1 个 Argon2 名额），界面轮询
// GET v1/users/bulk/generate/jobs/{id} 看进度，完成后从 …/result 下载 CSV。
// 门槛在 router_users.go：iam.user.write → 近期重认证 → 幂等（中间件在响应后记下，
// 同一个键重放回同一个任务）。
func (h *handlers) generateUsers(w http.ResponseWriter, r *http.Request) {
	var req generateUsersReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	p := httpx.PrincipalFrom(r.Context())
	job, err := h.d.Ops.SubmitGenerateUsers(r.Context(), httpx.TenantIDFrom(r.Context()),
		adminops.GenerateUsersInput{
			Count: req.Count, EmailPrefix: req.EmailPrefix,
			EmailDomain: req.EmailDomain, GroupID: req.GroupID,
			Reason: req.Reason, ActorID: p.UserID,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.JSON(w, http.StatusAccepted, job)
}

type userGenerationJobsResponse struct {
	Jobs []adminops.UserGenerationJob `json:"jobs"`
}

// listUserGenerationJobs 列最近的批量生成任务（不含结果）。
func (h *handlers) listUserGenerationJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := h.d.Ops.ListUserGenerationJobs(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, userGenerationJobsResponse{Jobs: jobs})
}

// getUserGenerationJob 读一个任务的进度，界面据此画进度条。
func (h *handlers) getUserGenerationJob(w http.ResponseWriter, r *http.Request) {
	job, err := h.d.Ops.GetUserGenerationJob(r.Context(), httpx.TenantIDFrom(r.Context()), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, job)
}

// downloadUserGenerationResult 下载任务结果（邮箱 + 初始口令）的 CSV。
//
// 只有提交任务的管理员能下，要近期重认证；导出审计与读取密文在同一个事务里写。
// 密文按任务 id 作 AAD 在这里解开，明文只出现在这个响应里，不进日志、不缓存。
func (h *handlers) downloadUserGenerationResult(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "id")
	sealed, _, err := h.d.Ops.UserGenerationResult(r.Context(), httpx.TenantIDFrom(r.Context()),
		jobID, httpx.PrincipalFrom(r.Context()).UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if h.d.Envelope == nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(errors.New("envelope not configured")))
		return
	}
	plain, err := h.d.Envelope.Open(sealed, adminops.UserGenerationResultAAD(jobID))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(fmt.Errorf("open user generation result: %w", err)))
		return
	}
	var users []adminops.GeneratedUser
	if err := json.Unmarshal(plain, &users); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(fmt.Errorf("decode user generation result: %w", err)))
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="generated-users.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"邮箱", "初始密码"})
	for _, u := range users {
		_ = cw.Write([]string{u.Email, u.Password})
	}
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

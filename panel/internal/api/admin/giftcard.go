// [INPUT]: 依赖 domain/giftcard 的模板、生码、批次、导出、卡码与兑换记录用例，依赖 platform/httpx
// [OUTPUT]: 对包内提供礼品卡处理器：模板列表与保存、生码、批次列表、一次性导出、卡码列表与启停、按筛选导出掩码报表、统计、兑换记录
// [POS]: api/admin 后台-06 礼品卡 tab 的 HTTP 外壳；明文卡码只经生码样例与 exportGiftBatch 出站，exportGiftCodesReport 与列表同筛选、只出掩码；权限与重认证在 router_marketing.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"encoding/csv"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/giftcard"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (h *handlers) listGiftTemplates(w http.ResponseWriter, r *http.Request) {
	rows, err := h.d.GiftCard.ListTemplates(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"templates": rows})
}

type giftTemplateReq struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Type        string              `json:"type"`
	Status      string              `json:"status"`
	Rewards     giftcard.Rewards    `json:"rewards"`
	Conditions  giftcard.Conditions `json:"conditions"`
	Limits      giftcard.Limits     `json:"limits"`
	ThemeColor  string              `json:"theme_color"`
}

func (h *handlers) saveGiftTemplate(w http.ResponseWriter, r *http.Request) {
	var req giftTemplateReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	principal := httpx.PrincipalFrom(r.Context())
	t, err := h.d.GiftCard.SaveTemplate(r.Context(), httpx.TenantIDFrom(r.Context()),
		giftcard.SaveTemplateInput{
			ID: req.ID, Name: req.Name, Description: req.Description,
			Type: req.Type, Status: req.Status, Rewards: req.Rewards,
			Conditions: req.Conditions, Limits: req.Limits,
			ThemeColor: req.ThemeColor, ActorID: principal.UserID,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"template": t})
}

type generateCodesReq struct {
	Count     int    `json:"count"`
	Prefix    string `json:"prefix"`
	ExpiresAt string `json:"expires_at"`
}

func (h *handlers) generateGiftCodes(w http.ResponseWriter, r *http.Request) {
	var req generateCodesReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	var expires *time.Time
	if req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, req.ExpiresAt)
		if err != nil {
			httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
				"expires_at": "时间格式不正确"}))
			return
		}
		expires = &t
	}
	principal := httpx.PrincipalFrom(r.Context())
	out, err := h.d.GiftCard.GenerateCodes(r.Context(),
		httpx.TenantIDFrom(r.Context()), giftcard.GenerateInput{
			TemplateID: chi.URLParam(r, "id"), Count: req.Count,
			Prefix: req.Prefix, ExpiresAt: expires, ActorID: principal.UserID,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 只回前几张明文作样例：完整明文只能经一次性导出拿到。
	httpx.OK(w, out)
}

func (h *handlers) listGiftCodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	codes, total, err := h.d.GiftCard.ListCodes(r.Context(),
		httpx.TenantIDFrom(r.Context()), giftcard.ListCodesInput{
			CodeFilter: giftCodeFilter(q), Limit: limit, Offset: offset,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"codes": codes, "total": total})
}

// giftCodeFilter 从查询串取卡码筛选：列表与掩码报表导出共用，口径一致。
func giftCodeFilter(q url.Values) giftcard.CodeFilter {
	return giftcard.CodeFilter{
		TemplateID: q.Get("template_id"), Status: q.Get("status"), BatchID: q.Get("batch_id"),
	}
}

// exportGiftCodesReport 按列表的筛选导出卡码掩码报表（CSV），给运营对账。
//
// 只有掩码：完整卡码只在生码样例与批次的一次性导出里出现，这里不读明文
// （掩码在 SQL 里算好，见 domain/giftcard/codes_export.go）。整批读完、审计
// 写进同一事务之后才开始输出；超过上限在领域层回 422。
func (h *handlers) exportGiftCodesReport(w http.ResponseWriter, r *http.Request) {
	rows, err := h.d.GiftCard.ExportCodes(r.Context(), httpx.TenantIDFrom(r.Context()),
		httpx.PrincipalFrom(r.Context()).UserID, giftCodeFilter(r.URL.Query()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	writeGiftCodesReportCSV(w, rows, time.Now())
}

func writeGiftCodesReportCSV(w http.ResponseWriter, rows []giftcard.CodeReportRow, now time.Time) {
	name := "gift-codes-report-" + now.UTC().Format("20060102-150405") + ".csv"
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	// BOM：运营会用 Excel 打开，没有它中文是乱码（同批次导出与用户导出）
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})

	stamp := func(t *time.Time) string {
		if t == nil {
			return ""
		}
		return t.Format("2006-01-02 15:04")
	}
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"卡密（掩码）", "状态", "模板", "批次", "有效期", "生成时间", "兑换人", "兑换时间"})
	for _, row := range rows {
		batch := ""
		if row.BatchID != nil {
			batch = *row.BatchID
		}
		_ = cw.Write([]string{
			row.CodeMasked, row.Status, csvSafe(row.TemplateName), batch,
			stamp(row.ExpiresAt), stamp(&row.CreatedAt), csvSafe(row.UsedEmail), stamp(row.UsedAt),
		})
	}
}

func (h *handlers) listGiftBatches(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, err := h.d.GiftCard.ListBatches(r.Context(),
		httpx.TenantIDFrom(r.Context()), giftcard.ListBatchesInput{
			TemplateID: q.Get("template_id"), Limit: limit, Offset: offset,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"items": items, "total": total})
}

// exportGiftBatch 一次性导出一个批次的明文卡码（CSV）。
//
// 卡密要发给渠道或印在卡片上，复制粘贴几千行不现实。
// 加 BOM 是因为 Excel 打开无 BOM 的 UTF-8 CSV 会把中文显示成乱码 ——
// 而运营几乎一定会用 Excel 打开它。
//
// 每个批次只能导出一次（领域层在同一事务里打标记）；同一幂等键的重试由
// 幂等中间件原样重放第一次的 CSV。重放只带 Content-Type 与 Cache-Control，
// 不带 Content-Disposition，文件名由前端按批次号自行拼出。
func (h *handlers) exportGiftBatch(w http.ResponseWriter, r *http.Request) {
	var req struct{}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	principal := httpx.PrincipalFrom(r.Context())
	export, err := h.d.GiftCard.ExportBatch(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "id"), principal.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	writeGiftBatchCSV(w, export)
}

func writeGiftBatchCSV(w http.ResponseWriter, export *giftcard.BatchExport) {
	short := export.Batch.ID
	if len(short) > 8 {
		short = short[:8]
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="gift-codes-`+short+`.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})

	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"卡密", "状态", "有效期", "模板"})
	for _, row := range export.Rows {
		expires := ""
		if row.ExpiresAt != nil {
			expires = row.ExpiresAt.Format("2006-01-02 15:04")
		}
		_ = cw.Write([]string{row.Code, row.Status, expires, row.TemplateName})
	}
}

type toggleCodeReq struct {
	Disabled bool `json:"disabled"`
}

func (h *handlers) toggleGiftCode(w http.ResponseWriter, r *http.Request) {
	var req toggleCodeReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	principal := httpx.PrincipalFrom(r.Context())
	if err := h.d.GiftCard.ToggleCode(r.Context(), httpx.TenantIDFrom(r.Context()),
		chi.URLParam(r, "id"), req.Disabled, principal.UserID); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"disabled": req.Disabled})
}

func (h *handlers) giftCardStats(w http.ResponseWriter, r *http.Request) {
	stats, err := h.d.GiftCard.Stats(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, stats)
}

func (h *handlers) listGiftUsages(w http.ResponseWriter, r *http.Request) {
	rows, err := h.d.GiftCard.ListUsages(r.Context(), httpx.TenantIDFrom(r.Context()),
		r.URL.Query().Get("template_id"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"usages": rows})
}

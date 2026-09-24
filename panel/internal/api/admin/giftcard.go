// [INPUT]: 依赖 domain/giftcard 的模板、生码、批次、导出、卡码与兑换记录用例，依赖 platform/httpx
// [OUTPUT]: 对包内提供礼品卡处理器：模板列表与保存、生码、批次列表、一次性导出、卡码列表与启停、统计、兑换记录
// [POS]: api/admin 后台-06 礼品卡 tab 的 HTTP 外壳；明文卡码只经生码样例与 exportGiftBatch 出站，权限与重认证在 router.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"encoding/csv"
	"net/http"
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
			TemplateID: q.Get("template_id"), Status: q.Get("status"),
			BatchID: q.Get("batch_id"), Limit: limit, Offset: offset,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"codes": codes, "total": total})
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

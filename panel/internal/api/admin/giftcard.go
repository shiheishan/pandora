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
	batchID, codes, err := h.d.GiftCard.GenerateCodes(r.Context(),
		httpx.TenantIDFrom(r.Context()), giftcard.GenerateInput{
			TemplateID: chi.URLParam(r, "id"), Count: req.Count,
			Prefix: req.Prefix, ExpiresAt: expires, ActorID: principal.UserID,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"batch_id": batchID, "count": len(codes), "codes": codes})
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

// exportGiftCodes 导出 CSV。
//
// 卡密要发给渠道或印在卡片上，复制粘贴几千行不现实。
// 加 BOM 是因为 Excel 打开无 BOM 的 UTF-8 CSV 会把中文显示成乱码 ——
// 而运营几乎一定会用 Excel 打开它。
func (h *handlers) exportGiftCodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	codes, _, err := h.d.GiftCard.ListCodes(r.Context(),
		httpx.TenantIDFrom(r.Context()), giftcard.ListCodesInput{
			TemplateID: q.Get("template_id"), Status: q.Get("status"),
			BatchID: q.Get("batch_id"), Limit: 5000,
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="gift-codes.csv"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})

	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"卡密", "状态", "有效期", "使用者", "使用时间"})
	for _, c := range codes {
		expires, used := "", ""
		if c.ExpiresAt != nil {
			expires = c.ExpiresAt.Format("2006-01-02 15:04")
		}
		if c.UsedAt != nil {
			used = c.UsedAt.Format("2006-01-02 15:04")
		}
		_ = cw.Write([]string{c.Code, c.Status, expires, c.UsedEmail, used})
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

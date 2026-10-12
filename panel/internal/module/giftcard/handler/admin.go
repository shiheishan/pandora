package handler

import (
	"encoding/csv"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/giftcard/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/pagination"
)

// csvTime 是 CSV 里的时间格式（UTC）。
const csvTime = "2006-01-02 15:04"

// utf8BOM 让 Excel 按 UTF-8 打开 CSV：运营几乎一定会用 Excel 打开，没有它中文是乱码。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// listTemplates 后台模板列表。
func (h *Handler) listTemplates(c *gin.Context) {
	rows, err := h.svc.ListTemplates(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var cc cents
	out := templatesResponse{Templates: make([]templateDTO, 0, len(rows))}
	for _, t := range rows {
		out.Templates = append(out.Templates, templateFrom(t, &cc))
	}
	if cc.err != nil {
		httpx.Fail(c, cc.err)
		return
	}
	httpx.OK(c, out)
}

// saveTemplate 新建或修改模板（id 空串为新建）。
func (h *Handler) saveTemplate(c *gin.Context) {
	var req templateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in, err := req.input()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	t, err := h.svc.SaveTemplate(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var cc cents
	out := templateResponse{Template: templateFrom(t, &cc)}
	if cc.err != nil {
		httpx.Fail(c, cc.err)
		return
	}
	httpx.OK(c, out)
}

// generateCodes 给模板批量生码；响应只带前几张明文作样例，完整明文只能经一次性导出拿到。
func (h *Handler) generateCodes(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req generateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	expires, err := req.expires()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	g, err := h.svc.GenerateCodes(c.Request.Context(), service.GenerateInput{
		TemplateID: id, Count: req.Count, Prefix: req.Prefix, ExpiresAt: expires, Reply: generatedReply,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, generatedFrom(g))
}

// listBatches 后台批次列表。
func (h *Handler) listBatches(c *gin.Context) {
	limit, offset, err := pagination.Parse(c, service.BatchListDefault, service.BatchListMax)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	tid, err := queryID(c, "template_id")
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, total, err := h.svc.ListBatches(c.Request.Context(), service.BatchFilter{TemplateID: tid, Limit: limit, Offset: offset})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out := batchesResponse{Items: make([]batchDTO, 0, len(rows)), Total: total}
	for _, b := range rows {
		out.Items = append(out.Items, batchFrom(b))
	}
	httpx.OK(c, out)
}

// exportBatch 一次性导出一个批次的明文卡密（CSV）。第二次回 409；同一幂等键的重试也回 409
// （CSV 不进幂等表，那是明文的第二个落点）。
func (h *Handler) exportBatch(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	exp, err := h.svc.ExportBatch(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	writeBatchCSV(c, exp)
}

// listCodes 后台卡密列表（只有掩码）。
func (h *Handler) listCodes(c *gin.Context) {
	limit, offset, err := pagination.Parse(c, service.CodeListDefault, service.CodeListMax)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	f, err := codeFilter(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, total, err := h.svc.ListCodes(c.Request.Context(), f, limit, offset)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, codesFrom(rows, total))
}

// exportCodesReport 按卡密列表的筛选导出掩码报表（CSV），给运营对账。
func (h *Handler) exportCodesReport(c *gin.Context) {
	f, err := codeFilter(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := h.svc.ExportCodes(c.Request.Context(), f)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	writeReportCSV(c, rows, time.Now())
}

// toggleCode 停用或恢复一个卡密。
func (h *Handler) toggleCode(c *gin.Context) {
	id, ok := pathID(c)
	if !ok {
		return
	}
	var req toggleRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.ToggleCode(c.Request.Context(), id, req.Disabled); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, toggleResponse(req))
}

// stats 后台礼品卡统计。
func (h *Handler) stats(c *gin.Context) {
	s, err := h.svc.Stats(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := statsFrom(s)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// listUsages 后台使用明细（最近 200 条）。
func (h *Handler) listUsages(c *gin.Context) {
	tid, err := queryID(c, "template_id")
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := h.svc.ListUsages(c.Request.Context(), tid)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := usagesFrom(rows)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

// codeFilter 从查询串取卡密筛选：列表与掩码报表共用，口径一致。
func codeFilter(c *gin.Context) (service.CodeFilter, error) {
	tid, err := queryID(c, "template_id")
	if err != nil {
		return service.CodeFilter{}, err
	}
	bid, err := queryID(c, "batch_id")
	if err != nil {
		return service.CodeFilter{}, err
	}
	return service.CodeFilter{TemplateID: tid, BatchID: bid, Status: c.Query("status")}, nil
}

// csvHeaders 写 CSV 下载的响应头与 BOM。
func csvHeaders(c *gin.Context, filename string) {
	h := c.Writer.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	h.Set("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(utf8BOM)
}

// csvSafe 防 CSV 公式注入：用户可控的文本以 = + - @ 制表符 回车开头时前面加单引号。
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// stamp 把可空时间写成 CSV 里的 UTC 字符串。
func stamp(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(csvTime)
}

// writeBatchCSV 写一次性导出的明文卡密文件。
func writeBatchCSV(c *gin.Context, exp service.BatchExport) {
	short := exp.Batch.ID.String()[:8]
	csvHeaders(c, "gift-codes-"+short+".csv")
	cw := csv.NewWriter(c.Writer)
	defer cw.Flush()
	_ = cw.Write([]string{"卡密", "状态", "有效期", "模板"})
	for _, r := range exp.Rows {
		_ = cw.Write([]string{r.Code, r.Status, stamp(r.ExpiresAt), csvSafe(r.TemplateName)})
	}
}

// writeReportCSV 写掩码报表。
func writeReportCSV(c *gin.Context, rows []service.CodeReportRow, now time.Time) {
	csvHeaders(c, "gift-codes-report-"+now.UTC().Format("20060102-150405")+".csv")
	cw := csv.NewWriter(c.Writer)
	defer cw.Flush()
	_ = cw.Write([]string{"卡密（掩码）", "状态", "模板", "批次", "有效期", "生成时间", "兑换人", "兑换时间"})
	for _, r := range rows {
		created := r.CreatedAt
		_ = cw.Write([]string{
			r.CodeMasked, r.Status, csvSafe(r.TemplateName), r.BatchID.String(),
			stamp(r.ExpiresAt), stamp(&created), csvSafe(r.UsedEmail), stamp(r.UsedAt),
		})
	}
}

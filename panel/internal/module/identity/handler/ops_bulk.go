package handler

import (
	"encoding/csv"
	"net/http"
	"strconv"

	"github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

func (h *Handler) bulkPreview(c *gin.Context) {
	var req bulkFilterRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	f, err := req.toFilter()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := h.svc.PreviewBulk(c.Request.Context(), f)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, previewFrom(out))
}

// bulkExport 导出用户 CSV。
func (h *Handler) bulkExport(c *gin.Context) {
	var q bulkExportQuery
	if err := httpx.BindQuery(c, &q); err != nil {
		httpx.Fail(c, err)
		return
	}
	f, limit, err := q.toFilter()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := h.svc.ExportUsers(c.Request.Context(), f, limit)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	writeCSVHead(c, "users.csv")
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(c.Writer)
	_ = cw.Write([]string{"邮箱", "状态", "分组", "生效订阅", "订单数", "累计实付", "注册时间", "最近登录"})
	for _, u := range rows {
		last := ""
		if u.LastLoginAt != nil {
			last = u.LastLoginAt.UTC().Format("2006-01-02 15:04")
		}
		_ = cw.Write([]string{
			service.CSVCell(u.Email),
			u.Status,
			service.CSVCell(u.GroupName),
			strconv.Itoa(u.ActiveSubs),
			strconv.FormatInt(u.Orders, 10),
			u.Paid,
			u.CreatedAt.UTC().Format("2006-01-02 15:04"),
			last,
		})
	}
	cw.Flush() // csv.Writer.Flush 不回错误，写失败从 Error 取
	if err := cw.Error(); err != nil {
		plog.From(c.Request.Context()).Error("users export csv flush", zap.Error(err))
	}
}

func (h *Handler) bulkGenerate(c *gin.Context) {
	var req generateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in, err := req.toInput()
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	job, err := h.svc.SubmitGeneration(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.JSON(http.StatusAccepted, generationJobFrom(job))
}

func (h *Handler) listGenerationJobs(c *gin.Context) {
	jobs, err := h.svc.ListGenerationJobs(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, generationJobsFrom(jobs))
}

func (h *Handler) getGenerationJob(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	job, err := h.svc.GetGenerationJob(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, generationJobFrom(job))
}

// generationResult 下载批量生成结果 CSV。明文口令只出现在这个响应里，不进日志。
func (h *Handler) generationResult(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	users, err := h.svc.GenerationResult(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}

	writeCSVHead(c, "generated-users.csv")
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(c.Writer)
	_ = cw.Write([]string{"邮箱", "初始密码"})
	for _, u := range users {
		// 只转义邮箱；口令原样给出（转义会改掉口令本身，生成的口令首字符固定是大写字母，不会被当成公式）
		_ = cw.Write([]string{service.CSVCell(u.Email), u.Password})
	}
	cw.Flush() // csv.Writer.Flush 不回错误，写失败从 Error 取
	if err := cw.Error(); err != nil {
		plog.From(c.Request.Context()).Error("generation result csv flush", zap.Error(err))
	}
}

// writeCSVHead 设置 CSV 下载响应头并标记 200（BOM 由调用方写入）。
func writeCSVHead(c *gin.Context, filename string) {
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
}

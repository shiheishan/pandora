package handler

import (
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// 本文件是 CSV 下载的非 JSON 出口，forbidigo 按文件放行（白名单在 tools/depguardgen）；
// 同包别的文件不直接写响应，经 httpx。

// startCSV 写审计导出的 CSV 响应头并标记 200，返回正文的写入口；service 写第一行前才调，
// 之前出错仍可回信封。
func startCSV(c *gin.Context) io.Writer {
	name := "audit-" + time.Now().UTC().Format("20060102-150405") + ".csv"
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	return c.Writer
}

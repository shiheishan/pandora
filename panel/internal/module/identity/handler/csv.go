package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 本文件是 CSV 下载的非 JSON 出口，forbidigo 按文件放行（白名单在 tools/depguardgen）；
// 同包别的文件不直接写响应，经 httpx。

// writeCSVHead 设置 CSV 下载响应头、标记 200，并写 UTF-8 BOM（Excel 按 UTF-8 打开，没有它中文是乱码）。
func writeCSVHead(c *gin.Context, filename string) {
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Header("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
}

package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 本文件是 CSV 下载的非 JSON 出口，forbidigo 按文件放行（白名单在 tools/depguardgen）；
// 同包别的文件不直接写响应，经 httpx。

// utf8BOM 让 Excel 按 UTF-8 打开 CSV：运营几乎一定会用 Excel 打开，没有它中文是乱码。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// csvHeaders 写 CSV 下载的响应头与 BOM。
func csvHeaders(c *gin.Context, filename string) {
	h := c.Writer.Header()
	h.Set("Content-Type", "text/csv; charset=utf-8")
	h.Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	h.Set("Cache-Control", "no-store")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(utf8BOM)
}

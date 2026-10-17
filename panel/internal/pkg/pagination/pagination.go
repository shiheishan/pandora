// Package pagination 解析列表查询的分页参数（limit、offset）。
package pagination

import (
	"strconv"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/gin-gonic/gin"
)

// Parse 从查询参数读取 limit、offset；非法值返回 422。
func Parse(c *gin.Context, def, max int) (limit, offset int, err error) {
	limit = def
	offset = 0

	if raw := c.Query("limit"); raw != "" {
		v, e := strconv.Atoi(raw)
		if e != nil || v < 1 {
			return 0, 0, errs.Invalid(map[string]string{"limit": "格式不正确"})
		}
		limit = v
	}

	if raw := c.Query("offset"); raw != "" {
		v, e := strconv.Atoi(raw)
		if e != nil || v < 0 {
			return 0, 0, errs.Invalid(map[string]string{"offset": "格式不正确"})
		}
		offset = v
	}

	if limit > max {
		limit = max
	}
	return limit, offset, nil
}

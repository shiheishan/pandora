// Package pagination 解析列表查询的分页参数并构造统一分页响应。
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

// Page 是分页列表的 JSON 形状。
type Page[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
}

// NewPage 构造分页响应；items 为 nil 时输出空数组。
func NewPage[T any](items []T, total int64) Page[T] {
	if items == nil {
		items = []T{}
	}
	return Page[T]{Items: items, Total: total}
}

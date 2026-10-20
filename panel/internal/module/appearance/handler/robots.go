package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const (
	robotsAllowHome = "User-agent: *\nAllow: /$\nDisallow: /\n"
	robotsDenyAll   = "User-agent: *\nDisallow: /\n"
)

// Robots 回门户的 /robots.txt（纯文本）。门户引擎上只有首页与静态资源（其余路径是接口或 404），只有首页值得收录：
// 允许收录时只放行首页（Allow: /$，其余 Disallow: /），不允许时整站 Disallow。
// 内容固定、不含任何路径前缀（后台前缀、订阅前缀都不写进来：robots.txt 是公开的，写进去等于告诉所有人）。
// 收录开关取 PageMeta 快照（与入口页同一份，后台保存后立即生效）。
func Robots(src PageMetaSource) gin.HandlerFunc {
	return func(c *gin.Context) {
		m := src.PageMeta(c.Request.Context())
		body := robotsDenyAll
		if m.AllowIndexing {
			body = robotsAllowHome
		}
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(body))
	}
}

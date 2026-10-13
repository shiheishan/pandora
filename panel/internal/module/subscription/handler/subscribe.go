package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// 订阅拉取（GET /:prefix/:token）的 HTTP 外壳：认证、渲染、限流、记日志都在 service.Pull。
//
// 失败路径与「路径不存在」无法区分：前缀或令牌不对、凭据失效、订阅停用、主人停用，以及认证后读库出错，
// 一律回同一个伪装 404（出错的那种在日志里留 ERROR，日志不带令牌、前缀与路径）。
// 唯一的例外是令牌有效、订阅过期：回 200 与一条到期提示节点。超限回 429 纯文本；分发依赖没装配回 503。

// decoyPage 是所有失败请求看到的静态 404 页（与旧 subscribe 一字不差）。
const decoyPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>404 Not Found</title></head>
<body><h1>Not Found</h1><p>The requested URL was not found on this server.</p></body></html>
`

// writeDecoy 回伪装 404，不加自定义头。
func writeDecoy(c *gin.Context) {
	c.Data(http.StatusNotFound, "text/html; charset=utf-8", []byte(decoyPage))
}

// formatUserinfo 生成 Subscription-Userinfo 头。
func formatUserinfo(u service.Userinfo) string {
	var b strings.Builder
	b.WriteString("upload=" + strconv.FormatInt(u.Upload, 10))
	b.WriteString("; download=" + strconv.FormatInt(u.Download, 10))
	b.WriteString("; total=" + strconv.FormatInt(u.Total, 10))
	if u.Expire > 0 {
		b.WriteString("; expire=" + strconv.FormatInt(u.Expire, 10))
	}
	return b.String()
}

// subscribeTarget 读查询参数 target、flag、type 里第一个非空值。
func subscribeTarget(c *gin.Context) string {
	for _, key := range []string{"target", "flag", "type"} {
		if v := strings.TrimSpace(c.Query(key)); v != "" {
			return v
		}
	}
	return ""
}

// subscribe 是公开订阅拉取 GET /:prefix/:token。
func (h *Handler) subscribe(c *gin.Context) {
	ctx := c.Request.Context()
	res, err := h.svc.Pull(ctx, service.PullRequest{
		Prefix: c.Param("prefix"),
		Token:  c.Param("token"),
		Target: subscribeTarget(c),
	})
	if err != nil {
		if errors.Is(err, service.ErrPullDenied) {
			writeDecoy(c)
			return
		}
		var he *errs.Error
		if errors.As(err, &he) && he.Code == errs.RateLimited {
			sec := he.RetryAfterSeconds
			if sec < 1 {
				sec = 1
			}
			c.Header("Retry-After", strconv.Itoa(sec))
			c.Data(http.StatusTooManyRequests, "text/plain; charset=utf-8", []byte("too many requests\n"))
			return
		}
		// 只有「分发没装配」回 503；认证之后别的 Unavailable（读库、取线路）也回伪装页，否则 503 就成了「令牌有效」的标志
		if errors.Is(err, service.ErrDeliveryUnavailable) {
			httpx.Fail(c, err)
			return
		}
		plog.From(ctx).Error("订阅拉取失败", zap.Error(err))
		writeDecoy(c)
		return
	}
	c.Header("Subscription-Userinfo", formatUserinfo(res.Userinfo))
	c.Header("Profile-Update-Interval", strconv.Itoa(res.UpdateIntervalHours))
	if res.WebPageURL != "" {
		c.Header("Profile-Web-Page-Url", res.WebPageURL)
	}
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
	if res.ContentDisposition != "" {
		c.Header("Content-Disposition", res.ContentDisposition)
	}
	c.Data(http.StatusOK, res.ContentType, res.Body)
}

package handler

import (
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
)

// minRotateReason 是后台换链接原因的最少字数。
const minRotateReason = 5

// mySubscriptionLinks 是门户订阅链接列表。
func (h *Handler) mySubscriptionLinks(c *gin.Context) {
	links, err := h.svc.MyLinks(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, toLinksResponse(links))
}

// rotateSubscriptionLink 是门户换链接：旧链接立即失效，回新链接。
func (h *Handler) rotateSubscriptionLink(c *gin.Context) {
	id, ok := subID(c)
	if !ok {
		return
	}
	url, err := h.svc.MyRotate(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, rotateLinkResponse{URL: url})
}

// mySubscriptionNodes 是门户线路预览（只有名称、协议与倍率）。
func (h *Handler) mySubscriptionNodes(c *gin.Context) {
	id, ok := subID(c)
	if !ok {
		return
	}
	nodes, err := h.svc.MyNodes(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, toNodesResponse(nodes))
}

// mySubscriptionUsage 是门户按日用量；days 不传时由 service 取缺省窗口。
func (h *Handler) mySubscriptionUsage(c *gin.Context) {
	id, ok := subID(c)
	if !ok {
		return
	}
	days := 0
	if raw := c.Query("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > service.MaxUsageDays {
			httpx.Fail(c, errs.Invalid(map[string]string{"days": "必须是 1 到 93 之间的整数"}))
			return
		}
		days = n
	}
	u, err := h.svc.MyUsage(c.Request.Context(), id, days)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	httpx.OK(c, toUsageResponse(u))
}

// adminRotateLink 是后台替用户换链接：要写原因（进审计），回应里没有新链接。
func (h *Handler) adminRotateLink(c *gin.Context) {
	id, ok := subID(c)
	if !ok {
		return
	}
	var req adminRotateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if utf8.RuneCountInString(reason) < minRotateReason {
		httpx.Fail(c, errs.Invalid(map[string]string{"reason": "请写清为什么要换这条订阅链接，至少 5 个字。这条会进审计"}))
		return
	}
	res, err := h.svc.AdminRotate(c.Request.Context(), id, reason)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, adminRotateResponse{OldRevoked: true, UserEmail: res.UserEmail})
}

// 门户换链接的限频（用户 2026-10-07 定，按份）：每份两次之间至少 10 分钟、每份每天 5 次；另按账号每天 20 次兜底
// （每次换链接都触发节点名单重算）。被前面的维度拦下就不再检查后面的维度。后台替用户换不限频（有审计）。
var rotateRules = []struct {
	rule   ratelimit.Rule
	perSub bool
}{
	{ratelimit.Cooldown("sub_rotate_gap", 10*time.Minute), true},
	{ratelimit.PerWindow("sub_rotate_day", 5, 24*time.Hour), true},
	{ratelimit.PerWindow("sub_rotate_acct_day", 20, 24*time.Hour), false},
}

// rotateLimit 依次检查三条规则；超限回 429，文案写明还要等几分钟（N = RetryAfterSeconds 向上取整到分钟，至少 1）。
// 没有 Limiter（单测）或取不到登录主体时放行。
func (h *Handler) rotateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if h.lim == nil {
			c.Next()
			return
		}
		p, ok := authn.FromGin(c)
		if !ok {
			c.Next()
			return
		}
		user := p.UserID.String()
		for _, r := range rotateRules {
			key := user
			if r.perSub {
				key = user + ":" + c.Param("id")
			}
			err := h.lim.Allow(c.Request.Context(), r.rule, key)
			if err == nil {
				continue
			}
			var e *errs.Error
			if errors.As(err, &e) && e.Code == errs.RateLimited {
				cp := *e
				mins := (cp.RetryAfterSeconds + 59) / 60
				if mins < 1 {
					mins = 1
				}
				cp.Message = "操作太频繁，请 " + strconv.Itoa(mins) + " 分钟后再试"
				httpx.Fail(c, &cp)
				return
			}
			httpx.Fail(c, err)
			return
		}
		c.Next()
	}
}

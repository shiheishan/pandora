// [INPUT]: 依赖 domain/subscription 的 DailyUsage / MaxUsageDays / ErrNotFound，依赖 platform/httpx、chi 的路径参数
// [OUTPUT]: 对包内提供 meSubscriptionUsage 处理器
// [POS]: api/public 门户-02 按日用量（概览「本期用量」柱状图）的 HTTP 外壳：只读、不要幂等键，订阅不属于本人与不存在同一个 404，路由在 router.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package public

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// parseUsageDays 解析 ?days=：缺省为 0（覆盖当前流量周期），给了就必须是
// 1..MaxUsageDays 的整数。
func parseUsageDays(raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > subscription.MaxUsageDays {
		return 0, httpx.Invalid(map[string]string{
			"days": "必须是 1 到 " + strconv.Itoa(subscription.MaxUsageDays) + " 之间的整数",
		})
	}
	return days, nil
}

// meSubscriptionUsage 返回本人一条订阅的按日计费流量。
func (h *handlers) meSubscriptionUsage(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if p == nil || p.UserID == "" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeUnauthorized, "需要登录"))
		return
	}
	days, err := parseUsageDays(r.URL.Query().Get("days"))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	usage, err := h.d.Subscription.DailyUsage(r.Context(), p.TenantID, p.UserID, chi.URLParam(r, "id"), days)
	if err != nil {
		if errors.Is(err, subscription.ErrNotFound) {
			httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
			return
		}
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	type dayView struct {
		Date  string `json:"date"`
		Bytes int64  `json:"bytes"`
	}
	out := make([]dayView, 0, len(usage.Days))
	for _, d := range usage.Days {
		out = append(out, dayView{Date: d.Date, Bytes: d.Bytes})
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.OK(w, map[string]any{
		"timezone":        usage.Timezone,
		"period_start":    usage.PeriodStart,
		"period_end":      usage.PeriodEnd,
		"days":            out,
		"today_bytes":     usage.TodayBytes,
		"avg_daily_bytes": usage.AvgDailyBytes,
	})
}

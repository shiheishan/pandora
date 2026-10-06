package public

import (
	"errors"
	"net/http"
	"strconv"
	"time"

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
	p, ok := httpx.RequireUser(w, r, h.d.Log)
	if !ok {
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
	type usageResponse struct {
		Timezone      string     `json:"timezone"`
		PeriodStart   time.Time  `json:"period_start"`
		PeriodEnd     *time.Time `json:"period_end"`
		Days          []dayView  `json:"days"`
		TodayBytes    int64      `json:"today_bytes"`
		AvgDailyBytes int64      `json:"avg_daily_bytes"`
	}
	out := make([]dayView, 0, len(usage.Days))
	for _, d := range usage.Days {
		out = append(out, dayView{Date: d.Date, Bytes: d.Bytes})
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.OK(w, usageResponse{
		Timezone:      usage.Timezone,
		PeriodStart:   usage.PeriodStart,
		PeriodEnd:     usage.PeriodEnd,
		Days:          out,
		TodayBytes:    usage.TodayBytes,
		AvgDailyBytes: usage.AvgDailyBytes,
	})
}

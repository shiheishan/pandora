package handler

import (
	"time"

	"github.com/aegispanel/aegis/internal/module/subscription/service"
)

// subscriptionLinkResponse 是门户链接列表里的一条。
type subscriptionLinkResponse struct {
	SubscriptionID  string     `json:"subscription_id"`
	URL             string     `json:"url"`
	FetchCount      int64      `json:"fetch_count"`
	LastFetchedAt   *time.Time `json:"last_fetched_at"`
	DistinctSources int        `json:"distinct_sources_24h"`
	Expired         bool       `json:"expired"`
}

// subscriptionLinksResponse 是门户链接列表。
type subscriptionLinksResponse struct {
	Links []subscriptionLinkResponse `json:"links"`
}

// rotateLinkResponse 是门户换链接的结果：新链接。
type rotateLinkResponse struct {
	URL string `json:"url"`
}

// nodePreviewResponse 是门户线路预览里的一条。
type nodePreviewResponse struct {
	Name        string  `json:"name"`
	Protocol    string  `json:"protocol"`
	TrafficRate float64 `json:"traffic_rate"`
}

// nodesResponse 是门户线路预览。
type nodesResponse struct {
	Count int                   `json:"count"`
	Nodes []nodePreviewResponse `json:"nodes"`
}

// usageDayResponse 是按日用量里的一天。
type usageDayResponse struct {
	Date  string `json:"date"`
	Bytes int64  `json:"bytes"`
}

// usageResponse 是门户按日用量。
type usageResponse struct {
	Timezone      string             `json:"timezone"`
	PeriodStart   time.Time          `json:"period_start"`
	PeriodEnd     *time.Time         `json:"period_end"`
	Days          []usageDayResponse `json:"days"`
	TodayBytes    int64              `json:"today_bytes"`
	AvgDailyBytes int64              `json:"avg_daily_bytes"`
}

// adminRotateRequest 是后台换链接的请求：原因进审计。
type adminRotateRequest struct {
	Reason string `json:"reason"`
}

// adminRotateResponse 刻意没有令牌与链接：后台看不到用户的订阅地址。
type adminRotateResponse struct {
	OldRevoked bool   `json:"old_revoked"`
	UserEmail  string `json:"user_email"`
}

// toLinksResponse 映射链接列表；空时输出 []。
func toLinksResponse(in []service.Link) subscriptionLinksResponse {
	out := subscriptionLinksResponse{Links: make([]subscriptionLinkResponse, 0, len(in))}
	for _, l := range in {
		out.Links = append(out.Links, subscriptionLinkResponse{
			SubscriptionID:  l.SubscriptionID.String(),
			URL:             l.URL,
			FetchCount:      l.FetchCount,
			LastFetchedAt:   utcPtr(l.LastFetchedAt),
			DistinctSources: l.DistinctSources24h,
			Expired:         l.Expired,
		})
	}
	return out
}

// toNodesResponse 映射线路预览；空时输出 []。
func toNodesResponse(in []service.NodePreview) nodesResponse {
	out := nodesResponse{Count: len(in), Nodes: make([]nodePreviewResponse, 0, len(in))}
	for _, n := range in {
		out.Nodes = append(out.Nodes, nodePreviewResponse{Name: n.Name, Protocol: n.Protocol, TrafficRate: n.TrafficRate})
	}
	return out
}

// toUsageResponse 映射按日用量；空时输出 []。
func toUsageResponse(u service.DailyUsage) usageResponse {
	out := usageResponse{
		Timezone:      u.Timezone,
		PeriodStart:   utc(u.PeriodStart),
		PeriodEnd:     utcPtr(u.PeriodEnd),
		Days:          make([]usageDayResponse, 0, len(u.Days)),
		TodayBytes:    u.TodayBytes,
		AvgDailyBytes: u.AvgDailyBytes,
	}
	for _, d := range u.Days {
		out.Days = append(out.Days, usageDayResponse{Date: d.Date, Bytes: d.Bytes})
	}
	return out
}

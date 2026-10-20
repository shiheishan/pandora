package handler

import (
	"time"

	"github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/gin-gonic/gin"
)

// traceQuery 是溯源总览列表接口的查询参数；取值（window、sort）由 service 校验与规整。
type traceQuery struct {
	Window string `form:"window" binding:"max=8"`
	Sort   string `form:"sort" binding:"max=32"`
}

// traceRevealQuery 是「显示」接口的查询参数。
type traceRevealQuery struct {
	Token string `form:"token" binding:"max=512"`
}

func (h *Handler) traceIPs(c *gin.Context) {
	var q traceQuery
	if err := httpx.BindQuery(c, &q); err != nil {
		httpx.Fail(c, err)
		return
	}
	r, err := h.svc.TraceIPs(c.Request.Context(), q.Window)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, traceIPsFrom(r))
}

func (h *Handler) traceSubscribers(c *gin.Context) {
	var q traceQuery
	if err := httpx.BindQuery(c, &q); err != nil {
		httpx.Fail(c, err)
		return
	}
	r, err := h.svc.TraceSubscribers(c.Request.Context(), q.Window, q.Sort)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, traceSubscribersFrom(r))
}

func (h *Handler) traceAccounts(c *gin.Context) {
	var q traceQuery
	if err := httpx.BindQuery(c, &q); err != nil {
		httpx.Fail(c, err)
		return
	}
	r, err := h.svc.TraceAccounts(c.Request.Context(), q.Window, q.Sort)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, traceAccountsFrom(r))
}

func (h *Handler) traceReveal(c *gin.Context) {
	var q traceRevealQuery
	if err := httpx.BindQuery(c, &q); err != nil {
		httpx.Fail(c, err)
		return
	}
	r, err := h.svc.TraceReveal(c.Request.Context(), q.Token)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, traceRevealFrom(r))
}

// ---- 响应 DTO ----

// traceIPsResponse 是概览与 IP 访问排行的响应。
type traceIPsResponse struct {
	Window   string                `json:"window"`
	Since    time.Time             `json:"since"`
	Overview traceOverviewResponse `json:"overview"`
	IPs      []traceIPResponse     `json:"ips"`
}

// traceOverviewResponse 是时间窗内的概览。
type traceOverviewResponse struct {
	DistinctIPs       int                   `json:"distinct_ips"`
	DistinctIPsApprox bool                  `json:"distinct_ips_approx"`
	Fetches           int                   `json:"fetches"`
	FetchFailures     int                   `json:"fetch_failures"`
	Logins            int                   `json:"logins"`
	LoginFailures     int                   `json:"login_failures"`
	AnalyzedIPs       int                   `json:"analyzed_ips"`
	DatacenterIPs     int                   `json:"datacenter_ips"`
	TopRegions        []traceRegionResponse `json:"top_regions"`
}

// traceRegionResponse 是一个来源地区。
type traceRegionResponse struct {
	Region string `json:"region"`
	IPs    int    `json:"ips"`
	Hits   int    `json:"hits"`
}

// traceIPResponse 是 IP 访问排行的一行（明文 IP 经 token 另取）。
type traceIPResponse struct {
	Token          string                 `json:"token"`
	IPMasked       string                 `json:"ip_masked"`
	Geo            string                 `json:"geo"`
	NetworkKind    string                 `json:"network_kind"`
	Hits           int                    `json:"hits"`
	Fetches        int                    `json:"fetches"`
	Events         int                    `json:"events"`
	Accounts       int                    `json:"accounts"`
	AccountsCapped bool                   `json:"accounts_capped"`
	UAKinds        int                    `json:"ua_kinds"`
	LastSeen       time.Time              `json:"last_seen"`
	Users          []traceUserRefResponse `json:"users"`
}

// traceUserRefResponse 是排行里的一个账号。
type traceUserRefResponse struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Status string `json:"status"`
}

// traceSubscribersResponse 是订阅更新排行的响应。
type traceSubscribersResponse struct {
	Window      string                    `json:"window"`
	Since       time.Time                 `json:"since"`
	Sort        string                    `json:"sort"`
	Subscribers []traceSubscriberResponse `json:"subscribers"`
}

// traceSubscriberResponse 是订阅更新排行的一行。
type traceSubscriberResponse struct {
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Status    string    `json:"status"`
	Fetches   int       `json:"fetches"`
	Failures  int       `json:"failures"`
	IPs       int       `json:"ips"`
	UAKinds   int       `json:"ua_kinds"`
	Families  []string  `json:"families"`
	Regions   int       `json:"regions"`
	LastFetch time.Time `json:"last_fetch"`
}

// traceAccountsResponse 是账号综合排行的响应。
type traceAccountsResponse struct {
	Window   string                 `json:"window"`
	Since    time.Time              `json:"since"`
	Sort     string                 `json:"sort"`
	Accounts []traceAccountResponse `json:"accounts"`
}

// traceAccountResponse 是账号综合排行的一行。
type traceAccountResponse struct {
	UserID         string    `json:"user_id"`
	Email          string    `json:"email"`
	Status         string    `json:"status"`
	LinkedIPs      int       `json:"linked_ips"`
	Logins         int       `json:"logins"`
	LoginIPs       int       `json:"login_ips"`
	FetchIPs       int       `json:"fetch_ips"`
	SharedIPs      int       `json:"shared_ips"`
	UAKinds        int       `json:"ua_kinds"`
	DatacenterHits int       `json:"datacenter_hits"`
	LastSeen       time.Time `json:"last_seen"`
}

// traceRevealResponse 是「显示」解出的明文 IP 与归属地。
type traceRevealResponse struct {
	IP          string `json:"ip"`
	Geo         string `json:"geo"`
	NetworkKind string `json:"network_kind"`
}

// ---- 转换 ----

func traceIPsFrom(r service.TraceIPReport) traceIPsResponse {
	return traceIPsResponse{
		Window:   r.Window,
		Since:    r.Since,
		Overview: traceOverviewFrom(r.Overview),
		IPs:      traceIPRowsFrom(r.IPs),
	}
}

func traceOverviewFrom(o service.TraceOverview) traceOverviewResponse {
	regions := make([]traceRegionResponse, 0, len(o.TopRegions))
	for _, rg := range o.TopRegions {
		regions = append(regions, traceRegionResponse{Region: rg.Region, IPs: rg.IPs, Hits: rg.Hits})
	}
	return traceOverviewResponse{
		DistinctIPs:       o.DistinctIPs,
		DistinctIPsApprox: o.DistinctIPsApprox,
		Fetches:           o.Fetches,
		FetchFailures:     o.FetchFailures,
		Logins:            o.Logins,
		LoginFailures:     o.LoginFailures,
		AnalyzedIPs:       o.AnalyzedIPs,
		DatacenterIPs:     o.DatacenterIPs,
		TopRegions:        regions,
	}
}

func traceIPRowsFrom(rows []service.TraceIPRow) []traceIPResponse {
	out := make([]traceIPResponse, 0, len(rows))
	for _, r := range rows {
		users := make([]traceUserRefResponse, 0, len(r.Users))
		for _, u := range r.Users {
			users = append(users, traceUserRefResponse{ID: u.ID.String(), Email: u.Email, Status: u.Status})
		}
		out = append(out, traceIPResponse{
			Token:          r.Token,
			IPMasked:       r.IPMasked,
			Geo:            r.Geo,
			NetworkKind:    r.NetworkKind,
			Hits:           r.Hits,
			Fetches:        r.Fetches,
			Events:         r.Events,
			Accounts:       r.Accounts,
			AccountsCapped: r.AccountsCapped,
			UAKinds:        r.UAKinds,
			LastSeen:       r.Last,
			Users:          users,
		})
	}
	return out
}

func traceSubscribersFrom(r service.TraceSubscriberReport) traceSubscribersResponse {
	rows := make([]traceSubscriberResponse, 0, len(r.Rows))
	for _, row := range r.Rows {
		families := row.Families
		if families == nil {
			families = []string{}
		}
		rows = append(rows, traceSubscriberResponse{
			UserID:    row.UserID.String(),
			Email:     row.Email,
			Status:    row.Status,
			Fetches:   row.Fetches,
			Failures:  row.Failures,
			IPs:       row.IPs,
			UAKinds:   row.UAKinds,
			Families:  families,
			Regions:   row.Regions,
			LastFetch: row.Last,
		})
	}
	return traceSubscribersResponse{Window: r.Window, Since: r.Since, Sort: r.Sort, Subscribers: rows}
}

func traceAccountsFrom(r service.TraceAccountReport) traceAccountsResponse {
	rows := make([]traceAccountResponse, 0, len(r.Rows))
	for _, row := range r.Rows {
		rows = append(rows, traceAccountResponse{
			UserID:         row.UserID.String(),
			Email:          row.Email,
			Status:         row.Status,
			LinkedIPs:      row.LinkedIPs,
			Logins:         row.Logins,
			LoginIPs:       row.LoginIPs,
			FetchIPs:       row.FetchIPs,
			SharedIPs:      row.SharedIPs,
			UAKinds:        row.UAKinds,
			DatacenterHits: row.DatacenterHits,
			LastSeen:       row.Last,
		})
	}
	return traceAccountsResponse{Window: r.Window, Since: r.Since, Sort: r.Sort, Accounts: rows}
}

func traceRevealFrom(r service.TraceReveal) traceRevealResponse {
	return traceRevealResponse{IP: r.IP, Geo: r.Geo, NetworkKind: r.NetworkKind}
}

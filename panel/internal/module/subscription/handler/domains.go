package handler

import (
	"context"
	"math"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

const (
	// probeTicketsPerHour 是每个账号每小时能领的探测票据数。
	probeTicketsPerHour = 6
	// probeFramePerMinute、probeReportPerMinute 是探测页与上报按来源 IP 每分钟的上限。
	probeFramePerMinute  = 30
	probeReportPerMinute = 30
	// subProbePerMinute 是公开探测接口按来源 IP 每分钟的上限。
	subProbePerMinute = 60
	// probeTicketMaxLen 是票据串的长度上限（超过直接拒，不进 service）。
	probeTicketMaxLen = 512
)

// DomainService 是订阅域名分组与探测用到的 service 方法。
type DomainService interface {
	AdminDomainConfig(ctx context.Context) (service.DomainConfigView, error)
	AdminSaveDomainConfig(ctx context.Context, in []service.DomainGroupInput) (service.DomainConfigView, error)
	IssueProbeTicket(ctx context.Context) (string, error)
	ProbeFrameDomains(ctx context.Context, ticket string) ([]string, error)
	ReportProbeResults(ctx context.Context, ticket string, results []service.ProbeResult) error
	PortalOrigin() string
}

type domainItem struct {
	BaseURL         string  `json:"base_url"`
	Weight          int     `json:"weight"`
	Score           float64 `json:"score"`
	EffectiveWeight float64 `json:"effective_weight"`
}

type domainGroupItem struct {
	ID           uuid.UUID    `json:"id"`
	Name         string       `json:"name"`
	IsDefault    bool         `json:"is_default"`
	Domains      []domainItem `json:"domains"`
	UserGroupIDs []uuid.UUID  `json:"user_group_ids"`
}

type domainLimits struct {
	Groups          int `json:"groups"`
	DomainsPerGroup int `json:"domains_per_group"`
	WeightMax       int `json:"weight_max"`
}

type domainConfigResponse struct {
	Groups          []domainGroupItem `json:"groups"`
	ScoresAvailable bool              `json:"scores_available"`
	Limits          domainLimits      `json:"limits"`
}

type saveDomainInput struct {
	BaseURL string `json:"base_url"`
	Weight  int    `json:"weight"`
}

type saveDomainGroup struct {
	Name         string            `json:"name"`
	IsDefault    bool              `json:"is_default"`
	Domains      []saveDomainInput `json:"domains"`
	UserGroupIDs []string          `json:"user_group_ids"`
}

type saveDomainsRequest struct {
	Groups []saveDomainGroup `json:"groups"`
}

func roundTwoDecimals(x float64) float64 {
	return math.Round(x*100) / 100
}

func toDomainConfigResponse(v service.DomainConfigView) domainConfigResponse {
	groups := make([]domainGroupItem, 0, len(v.Groups))
	for _, g := range v.Groups {
		doms := make([]domainItem, 0, len(g.Domains))
		for _, d := range g.Domains {
			doms = append(doms, domainItem{
				BaseURL:         d.BaseURL,
				Weight:          d.Weight,
				Score:           roundTwoDecimals(d.Score),
				EffectiveWeight: roundTwoDecimals(d.EffectiveWeight),
			})
		}
		ugIDs := g.UserGroupIDs
		if ugIDs == nil {
			ugIDs = []uuid.UUID{}
		}
		groups = append(groups, domainGroupItem{
			ID:           g.ID,
			Name:         g.Name,
			IsDefault:    g.IsDefault,
			Domains:      doms,
			UserGroupIDs: ugIDs,
		})
	}
	return domainConfigResponse{
		Groups:          groups,
		ScoresAvailable: v.ScoresAvailable,
		Limits: domainLimits{
			Groups:          service.DomainGroupsMax,
			DomainsPerGroup: service.DomainsPerGroupMax,
			WeightMax:       service.DomainWeightMax,
		},
	}
}

// adminDomains 是后台获取订阅域名分组配置 GET /v1/subscription-domains。
func (h *Handler) adminDomains(c *gin.Context) {
	cfg, err := h.svc.AdminDomainConfig(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, toDomainConfigResponse(cfg))
}

// adminSaveDomains 是后台整份保存订阅域名分组配置 PUT /v1/subscription-domains。
func (h *Handler) adminSaveDomains(c *gin.Context) {
	var req saveDomainsRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in := make([]service.DomainGroupInput, 0, len(req.Groups))
	for _, g := range req.Groups {
		userGroupIDs := make([]uuid.UUID, 0, len(g.UserGroupIDs))
		for _, rawID := range g.UserGroupIDs {
			parsed, err := uuid.Parse(rawID)
			if err != nil {
				httpx.Fail(c, errs.Invalid(map[string]string{"groups": "用户组 id 格式不正确"}))
				return
			}
			userGroupIDs = append(userGroupIDs, parsed)
		}
		doms := make([]service.Domain, 0, len(g.Domains))
		for _, d := range g.Domains {
			doms = append(doms, service.Domain{
				BaseURL: d.BaseURL,
				Weight:  d.Weight,
			})
		}
		in = append(in, service.DomainGroupInput{
			Name:         g.Name,
			IsDefault:    g.IsDefault,
			Domains:      doms,
			UserGroupIDs: userGroupIDs,
		})
	}
	cfg, err := h.svc.AdminSaveDomainConfig(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, toDomainConfigResponse(cfg))
}

type probeTicketResponse struct {
	Ticket string `json:"ticket"`
}

type probeResultItem struct {
	BaseURL string `json:"base_url"`
	OK      bool   `json:"ok"`
}

type probeReportRequest struct {
	Ticket  string            `json:"ticket"`
	Results []probeResultItem `json:"results"`
}

// probeTicket 是门户领取探测票据 POST /v1/sub-probe/ticket。
func (h *Handler) probeTicket(c *gin.Context) {
	t, err := h.svc.IssueProbeTicket(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, probeTicketResponse{Ticket: t})
}

// probeFrame 是探测页 GET /v1/sub-probe/frame。
func (h *Handler) probeFrame(c *gin.Context) {
	t := c.Query("t")
	if t == "" || len(t) > probeTicketMaxLen {
		httpx.Fail(c, errs.Newf(errs.Unauthorized, "探测票据无效或已过期"))
		return
	}
	domains, err := h.svc.ProbeFrameDomains(c.Request.Context(), t)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	writeProbeFrame(c, domains)
}

// probeReport 是探测页上报结果 POST /v1/sub-probe/report。
func (h *Handler) probeReport(c *gin.Context) {
	var req probeReportRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if req.Ticket == "" || len(req.Ticket) > probeTicketMaxLen {
		httpx.Fail(c, errs.Newf(errs.Unauthorized, "探测票据无效或已过期"))
		return
	}
	if len(req.Results) > service.DomainsPerGroupMax {
		httpx.Fail(c, errs.Invalid(map[string]string{"results": "一次最多报告 10 个域名"}))
		return
	}
	results := make([]service.ProbeResult, 0, len(req.Results))
	for _, r := range req.Results {
		results = append(results, service.ProbeResult{BaseURL: r.BaseURL, OK: r.OK})
	}
	if err := h.svc.ReportProbeResults(c.Request.Context(), req.Ticket, results); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.NoContent(c)
}

// probeCORS 给公开探测接口写 CORS 头：只在请求的 Origin 恰好是门户源时回 ACAO（别的来源拿不到「这个订阅域名属于哪个门户」）；
// 挂在限流之前，429 也带上，探测页才能把「被限流」和「连不上」分开。
func (h *Handler) probeCORS(c *gin.Context) {
	c.Header("Vary", "Origin")
	if o := h.svc.PortalOrigin(); o != "" && c.GetHeader("Origin") == o {
		c.Header("Access-Control-Allow-Origin", o)
	}
	c.Next()
}

// subProbe 是公开探测接口 GET/HEAD /v1/sub-probe：只回 204、不读不写任何数据、不带凭据头。
func (h *Handler) subProbe(c *gin.Context) {
	httpx.NoContent(c)
}

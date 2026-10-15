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

// 限流与校验常量
const (
	probeReportsPerHour = 6
	subProbePerMinute   = 60
	probeReportMax      = 10
)

// DomainService 是订阅域名分组与门户探测用到的 service 方法。
type DomainService interface {
	AdminDomainConfig(ctx context.Context) (service.DomainConfigView, error)
	AdminSaveDomainConfig(ctx context.Context, in []service.DomainGroupInput) (service.DomainConfigView, error)
	MyProbeDomains(ctx context.Context) ([]string, error)
	ReportDomainProbes(ctx context.Context, results []service.ProbeResult) error
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

type probeDomainsResponse struct {
	Domains []string `json:"domains"`
}

type probeResultItem struct {
	BaseURL string `json:"base_url"`
	OK      bool   `json:"ok"`
}

type probeReportRequest struct {
	Results []probeResultItem `json:"results"`
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

// myProbeDomains 是门户获取本人所在组的探测域名 GET /v1/me/subscription-domains。
func (h *Handler) myProbeDomains(c *gin.Context) {
	domains, err := h.svc.MyProbeDomains(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if domains == nil {
		domains = []string{}
	}
	httpx.OK(c, probeDomainsResponse{Domains: domains})
}

// reportDomainProbes 是门户上报探测结果 POST /v1/me/subscription-domains/probe。
func (h *Handler) reportDomainProbes(c *gin.Context) {
	var req probeReportRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if len(req.Results) > probeReportMax {
		httpx.Fail(c, errs.Invalid(map[string]string{"results": "一次最多报告 10 个域名"}))
		return
	}
	results := make([]service.ProbeResult, 0, len(req.Results))
	for _, r := range req.Results {
		results = append(results, service.ProbeResult{
			BaseURL: r.BaseURL,
			OK:      r.OK,
		})
	}
	if err := h.svc.ReportDomainProbes(c.Request.Context(), results); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.NoContent(c)
}

// subProbe 是公开探测接口：浏览器跨源 GET，只回 204；按 IP 每分钟 60 次。
// 只回 204、不读不写任何用户数据、不带凭据头；给门户在浏览器里探测订阅域名能不能连上。
func (h *Handler) subProbe(c *gin.Context) {
	origin := h.svc.PortalOrigin()
	if origin != "" {
		c.Header("Access-Control-Allow-Origin", origin)
	}
	c.Header("Vary", "Origin")
	httpx.NoContent(c)
}

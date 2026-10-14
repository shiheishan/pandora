package handler

// 出站、分流规则与路由组接口的请求与响应。
// 形状对照前端 panel/frontend/src/admin/screens/nodes/schemas.ts 的路由与路由组 schema；切片一律输出 []，不输出 null。

import (
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

type outboundDTO struct {
	Tag      string          `json:"tag"`
	Type     string          `json:"type"`
	Settings json.RawMessage `json:"settings"`
}

type routeDTO struct {
	Priority    int             `json:"priority"`
	Matcher     json.RawMessage `json:"matcher"`
	OutboundTag string          `json:"outbound_tag"`
	Enabled     bool            `json:"enabled"`
	Note        string          `json:"note"`
}

type routeGroupRefDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SortOrder int    `json:"sort_order"`
}

type routingSourceDTO struct {
	Scope     string `json:"scope"`
	GroupID   string `json:"group_id,omitempty"`
	GroupName string `json:"group_name,omitempty"`
}

type effectiveOutboundDTO struct {
	Tag      string           `json:"tag"`
	Type     string           `json:"type"`
	Settings json.RawMessage  `json:"settings"`
	Source   routingSourceDTO `json:"source"`
}

type effectiveRouteDTO struct {
	Matcher  json.RawMessage  `json:"matcher"`
	Outbound string           `json:"outbound"`
	Source   routingSourceDTO `json:"source"`
}

// ---- 请求 ----

type globalRoutingRequest struct {
	ExpectedRevision string        `json:"expected_revision"`
	Outbounds        []outboundDTO `json:"outbounds"`
	Routes           []routeDTO    `json:"routes"`
}

type nodeRoutingRequest struct {
	RowVersion int64         `json:"row_version"`
	Outbounds  []outboundDTO `json:"outbounds"`
	Routes     []routeDTO    `json:"routes"`
}

type groupRoutingRequest struct {
	RowVersion int64         `json:"row_version"`
	Outbounds  []outboundDTO `json:"outbounds"`
	Routes     []routeDTO    `json:"routes"`
}

type routeGroupCreateRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SortOrder   int    `json:"sort_order"`
}

type routeGroupUpdateRequest struct {
	RowVersion  int64   `json:"row_version"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	SortOrder   *int    `json:"sort_order"`
}

type routeGroupDeleteRequest struct {
	RowVersion int64 `json:"row_version"`
}

type routeGroupMembersRequest struct {
	RowVersion int64    `json:"row_version"`
	NodeIDs    []string `json:"node_ids"`
}

type nodeRouteGroupsRequest struct {
	RowVersion int64    `json:"row_version"`
	GroupIDs   []string `json:"group_ids"`
}

// ---- 响应 ----

type globalRoutingResponse struct {
	Revision    string        `json:"revision"`
	Outbounds   []outboundDTO `json:"outbounds"`
	Routes      []routeDTO    `json:"routes"`
	OnlineNodes int64         `json:"online_nodes"`
}

type globalRoutingSavedResponse struct {
	OK            bool   `json:"ok"`
	Revision      string `json:"revision"`
	AffectedNodes int    `json:"affected_nodes"`
}

type nodeRoutingResponse struct {
	RowVersion int64              `json:"row_version"`
	Groups     []routeGroupRefDTO `json:"groups"`
	Outbounds  []outboundDTO      `json:"outbounds"`
	Routes     []routeDTO         `json:"routes"`
}

type nodeRoutingSavedResponse struct {
	OK         bool  `json:"ok"`
	RowVersion int64 `json:"row_version"`
}

type nodeRouteGroupsSavedResponse struct {
	OK         bool  `json:"ok"`
	RowVersion int64 `json:"row_version"`
}

type effectiveRoutingResponse struct {
	Groups    []routeGroupRefDTO     `json:"groups"`
	Outbounds []effectiveOutboundDTO `json:"outbounds"`
	Routes    []effectiveRouteDTO    `json:"routes"`
}

type routeGroupMemberDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type routeGroupDTO struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	Description   string                `json:"description"`
	SortOrder     int                   `json:"sort_order"`
	RowVersion    int64                 `json:"row_version"`
	OutboundCount int                   `json:"outbound_count"`
	RuleCount     int                   `json:"rule_count"`
	Members       []routeGroupMemberDTO `json:"members"`
	CreatedAt     time.Time             `json:"created_at"`
	UpdatedAt     time.Time             `json:"updated_at"`
}

type routeGroupsResponse struct {
	Groups []routeGroupDTO `json:"groups"`
}

type routeGroupUpdatedResponse struct {
	Group         routeGroupDTO `json:"group"`
	AffectedNodes int           `json:"affected_nodes"`
}

type routeGroupDeletedResponse struct {
	Deleted       bool `json:"deleted"`
	AffectedNodes int  `json:"affected_nodes"`
}

type groupRoutingResponse struct {
	RowVersion int64         `json:"row_version"`
	Outbounds  []outboundDTO `json:"outbounds"`
	Routes     []routeDTO    `json:"routes"`
}

type groupRoutingSavedResponse struct {
	OK            bool  `json:"ok"`
	RowVersion    int64 `json:"row_version"`
	AffectedNodes int   `json:"affected_nodes"`
}

type groupMembersSavedResponse struct {
	OK            bool  `json:"ok"`
	RowVersion    int64 `json:"row_version"`
	AffectedNodes int   `json:"affected_nodes"`
}

// ---- 转换 ----

// emptyObject 是 settings / matcher 缺省时的输出（前端 schema 要对象）。
var emptyObject = json.RawMessage("{}")

func rawOrEmpty(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return emptyObject
	}
	return raw
}

func outboundsFromService(in []service.RoutingOutbound) []outboundDTO {
	out := make([]outboundDTO, 0, len(in))
	for _, o := range in {
		out = append(out, outboundDTO{Tag: o.Tag, Type: o.Type, Settings: rawOrEmpty(o.Settings)})
	}
	return out
}

func routesFromService(in []service.RoutingRule) []routeDTO {
	out := make([]routeDTO, 0, len(in))
	for _, r := range in {
		out = append(out, routeDTO{
			Priority: r.Priority, Matcher: rawOrEmpty(r.Matcher), OutboundTag: r.OutboundTag,
			Enabled: r.Enabled, Note: r.Note,
		})
	}
	return out
}

func groupRefsFromService(in []service.RouteGroupRef) []routeGroupRefDTO {
	out := make([]routeGroupRefDTO, 0, len(in))
	for _, g := range in {
		out = append(out, routeGroupRefDTO{ID: g.ID.String(), Name: g.Name, SortOrder: g.SortOrder})
	}
	return out
}

func sourceFromService(s service.RoutingSource) routingSourceDTO {
	d := routingSourceDTO{Scope: s.Scope}
	if s.GroupID != nil {
		d.GroupID = s.GroupID.String()
		d.GroupName = s.GroupName
	}
	return d
}

func effectiveFromService(in service.EffectiveRouting) effectiveRoutingResponse {
	out := effectiveRoutingResponse{
		Groups:    groupRefsFromService(in.Groups),
		Outbounds: make([]effectiveOutboundDTO, 0, len(in.Outbounds)),
		Routes:    make([]effectiveRouteDTO, 0, len(in.Routes)),
	}
	for _, o := range in.Outbounds {
		out.Outbounds = append(out.Outbounds, effectiveOutboundDTO{
			Tag: o.Tag, Type: o.Type, Settings: rawOrEmpty(o.Settings), Source: sourceFromService(o.Source),
		})
	}
	for _, r := range in.Routes {
		out.Routes = append(out.Routes, effectiveRouteDTO{
			Matcher: rawOrEmpty(r.Matcher), Outbound: r.Outbound, Source: sourceFromService(r.Source),
		})
	}
	return out
}

func routeGroupFromService(g service.RouteGroup) routeGroupDTO {
	members := make([]routeGroupMemberDTO, 0, len(g.Members))
	for _, m := range g.Members {
		members = append(members, routeGroupMemberDTO{ID: m.ID.String(), Name: m.Name})
	}
	return routeGroupDTO{
		ID: g.ID.String(), Name: g.Name, Description: g.Description, SortOrder: g.SortOrder,
		RowVersion: g.RowVersion, OutboundCount: g.OutboundCount, RuleCount: g.RuleCount,
		Members: members, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt,
	}
}

func routingInputFromDTO(outs []outboundDTO, routes []routeDTO) service.RoutingInput {
	in := service.RoutingInput{
		Outbounds: make([]service.RoutingOutbound, 0, len(outs)),
		Routes:    make([]service.RoutingRule, 0, len(routes)),
	}
	for _, o := range outs {
		in.Outbounds = append(in.Outbounds, service.RoutingOutbound{Tag: o.Tag, Type: o.Type, Settings: o.Settings})
	}
	for _, r := range routes {
		in.Routes = append(in.Routes, service.RoutingRule{
			Priority: r.Priority, Matcher: r.Matcher, OutboundTag: r.OutboundTag, Enabled: r.Enabled, Note: r.Note,
		})
	}
	return in
}

// parseUUIDList 解析请求体里的一组 id，坏的回 422（字段名 field）。
func parseUUIDList(field string, raw []string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(raw))
	for _, v := range raw {
		id, err := uuid.Parse(v)
		if err != nil {
			return nil, errs.Invalid(map[string]string{field: "包含无效的 id：" + v})
		}
		out = append(out, id)
	}
	return out, nil
}

// routingPathID 取路径里的 :id；不是 uuid 回中性 404。
func routingPathID(c *gin.Context) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		return uuid.Nil, errs.New(errs.NotFound)
	}
	return id, nil
}

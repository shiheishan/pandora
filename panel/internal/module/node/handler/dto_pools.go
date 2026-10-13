package handler

import (
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

type poolCreateRequest struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Region string `json:"region"`
	// AllowedUserGroupIDs 省略或 null = 不设名单；带了（含 []）就要近期重认证。
	AllowedUserGroupIDs *[]string `json:"allowed_user_group_ids"`
	// Status 前端会发，忽略。
	Status string `json:"status"`
}

type poolUpdateRequest struct {
	Name   string `json:"name"`   // 空串 = 不改
	Region string `json:"region"` // 空串 = 不改
	Status string `json:"status"` // 空串 = 不改
	// AllowedUserGroupIDs 省略或 null = 不改，[] = 取消限定；带了就要近期重认证。
	AllowedUserGroupIDs *[]string `json:"allowed_user_group_ids"`
	// Code 前端会发，忽略。
	Code string `json:"code"`
}

type planPoolsSetRequest struct {
	VersionID                 string   `json:"version_id"`
	ExpectedVersionRowVersion int64    `json:"expected_version_row_version"`
	PoolIDs                   []string `json:"pool_ids"`
}

type poolListResponse struct {
	Pools []poolResponse `json:"pools"`
}

type poolResponse struct {
	ID                string               `json:"id"`
	Code              string               `json:"code"`
	Name              string               `json:"name"`
	Region            string               `json:"region"`
	Status            string               `json:"status"`
	Nodes             int64                `json:"nodes"`
	ActiveNodes       int64                `json:"active_nodes"`
	Plans             int                  `json:"plans"`
	Members           []poolMemberResponse `json:"members"`
	PlanNames         []string             `json:"plan_names"`
	AllowedUserGroups []poolGroupResponse  `json:"allowed_user_groups"`
}

type poolMemberResponse struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	NodeNo int64  `json:"node_no"`
}

type poolGroupResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type poolCreatedResponse struct {
	ID string `json:"id"`
}

type poolOKResponse struct {
	OK bool `json:"ok"`
}

type planPoolsResponse struct {
	VersionID     string                   `json:"version_id"`
	VersionStatus string                   `json:"version_status"`
	RowVersion    int64                    `json:"row_version"`
	Editable      bool                     `json:"editable"`
	Pools         []planPoolOptionResponse `json:"pools"`
}

type planPoolOptionResponse struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	ActiveNodes      int64  `json:"active_nodes"`
	DeliverableNodes int64  `json:"deliverable_nodes"`
	Bound            bool   `json:"bound"`
}

type planPoolsSetResponse struct {
	Bound      int    `json:"bound"`
	RowVersion int64  `json:"row_version"`
	VersionID  string `json:"version_id"`
}

func poolResponseFrom(p service.Pool) poolResponse {
	members := make([]poolMemberResponse, 0, len(p.Members))
	for _, m := range p.Members {
		members = append(members, poolMemberResponse{
			ID:     m.ID.String(),
			Name:   m.Name,
			NodeNo: m.NodeNo,
		})
	}
	planNames := p.PlanNames
	if planNames == nil {
		planNames = []string{}
	}
	groups := make([]poolGroupResponse, 0, len(p.AllowedUserGroups))
	for _, g := range p.AllowedUserGroups {
		groups = append(groups, poolGroupResponse{ID: g.ID.String(), Name: g.Name})
	}
	return poolResponse{
		ID:                p.ID.String(),
		Code:              p.Code,
		Name:              p.Name,
		Region:            p.Region,
		Status:            p.Status,
		Nodes:             p.Nodes,
		ActiveNodes:       p.ActiveNodes,
		Plans:             p.Plans,
		Members:           members,
		PlanNames:         planNames,
		AllowedUserGroups: groups,
	}
}

func poolListResponseFrom(ps []service.Pool) poolListResponse {
	pools := make([]poolResponse, 0, len(ps))
	for _, p := range ps {
		pools = append(pools, poolResponseFrom(p))
	}
	return poolListResponse{Pools: pools}
}

func planPoolsResponseFrom(p service.PlanPools) planPoolsResponse {
	versionID := ""
	if p.VersionID != nil {
		versionID = p.VersionID.String()
	}
	pools := make([]planPoolOptionResponse, 0, len(p.Pools))
	for _, o := range p.Pools {
		pools = append(pools, planPoolOptionResponse{
			ID:               o.ID.String(),
			Name:             o.Name,
			ActiveNodes:      o.ActiveNodes,
			DeliverableNodes: o.DeliverableNodes,
			Bound:            o.Bound,
		})
	}
	return planPoolsResponse{
		VersionID:     versionID,
		VersionStatus: p.VersionStatus,
		RowVersion:    p.RowVersion,
		Editable:      p.Editable,
		Pools:         pools,
	}
}

// parsePoolUserGroupIDs：超过 service.MaxPoolUserGroups 个 → allowed_user_group_ids「一个节点池最多限定 100 个用户组」；
// 有非法 UUID 或重复 → allowed_user_group_ids「必须是无重复的用户组 UUID 列表」。返回按 String() 升序、非 nil。
func parsePoolUserGroupIDs(raw []string) ([]uuid.UUID, error) {
	if len(raw) > service.MaxPoolUserGroups {
		return nil, errs.Invalid(map[string]string{
			"allowed_user_group_ids": "一个节点池最多限定 100 个用户组",
		})
	}
	seen := make(map[uuid.UUID]struct{}, len(raw))
	ids := make([]uuid.UUID, 0, len(raw))
	for _, s := range raw {
		id, err := uuid.Parse(strings.TrimSpace(s))
		if err != nil {
			return nil, errs.Invalid(map[string]string{
				"allowed_user_group_ids": "必须是无重复的用户组 UUID 列表",
			})
		}
		if _, dup := seen[id]; dup {
			return nil, errs.Invalid(map[string]string{
				"allowed_user_group_ids": "必须是无重复的用户组 UUID 列表",
			})
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids, nil
}

// parsePlanPoolsSetRequest：version_id 不是 UUID →「必须是有效 UUID」；expected_version_row_version <= 0 →「必须是正整数」；
// pool_ids 超过 service.MaxPlanPools 个 →「一次最多绑定 500 个节点池」；有非法 UUID 或重复 → pool_ids「必须是无重复的 UUID 列表」。
// 多个字段的错误合在一个 map 里。pool_ids 为 null 视同 []。返回的 poolIDs 按 String() 升序、非 nil。
func parsePlanPoolsSetRequest(req planPoolsSetRequest) (versionID uuid.UUID, poolIDs []uuid.UUID, err error) {
	fields := map[string]string{}

	vid, verr := uuid.Parse(strings.TrimSpace(req.VersionID))
	if verr != nil {
		fields["version_id"] = "必须是有效 UUID"
	}
	if req.ExpectedVersionRowVersion <= 0 {
		fields["expected_version_row_version"] = "必须是正整数"
	}

	raw := req.PoolIDs
	if raw == nil {
		raw = []string{}
	}
	if len(raw) > service.MaxPlanPools {
		fields["pool_ids"] = "一次最多绑定 500 个节点池"
	}

	seen := make(map[uuid.UUID]struct{}, len(raw))
	parsed := make([]uuid.UUID, 0, len(raw))
	for _, s := range raw {
		id, perr := uuid.Parse(strings.TrimSpace(s))
		if perr != nil {
			fields["pool_ids"] = "必须是无重复的 UUID 列表"
			continue
		}
		if _, dup := seen[id]; dup {
			fields["pool_ids"] = "必须是无重复的 UUID 列表"
			continue
		}
		seen[id] = struct{}{}
		parsed = append(parsed, id)
	}

	if len(fields) > 0 {
		return uuid.Nil, nil, errs.Invalid(fields)
	}
	sort.Slice(parsed, func(i, j int) bool { return parsed[i].String() < parsed[j].String() })
	return vid, parsed, nil
}

// poolPathID：c.Param(name) 不是 UUID → errs.New(errs.NotFound)。
func poolPathID(c *gin.Context, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(c.Param(name))
	if err != nil {
		return uuid.Nil, errs.New(errs.NotFound)
	}
	return id, nil
}

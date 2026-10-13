// 本文件归 p5-routing：全局与节点私有路由的读写、生效预览。
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// GlobalRouting 是全局出站与规则；Revision 是乐观并发凭据（存储内容的摘要，空集也有值）。
type GlobalRouting struct {
	Revision    string
	Outbounds   []RoutingOutbound
	Routes      []RoutingRule
	OnlineNodes int64
}

// NodeRouting 是节点私有的出站与规则与它所在的路由组（按生效顺序）；RowVersion 是节点行版本。
type NodeRouting struct {
	RowVersion int64
	Groups     []RouteGroupRef
	Outbounds  []RoutingOutbound
	Routes     []RoutingRule
}

// EffectiveRouting 是节点合并后的生效路由与每条的来源，与下发的配置文档同一口径（mergeRouting）。
type EffectiveRouting struct {
	Groups    []RouteGroupRef
	Outbounds []EffectiveOutbound
	Routes    []EffectiveRoute
}

// RoutingPublish 是全局路由发布的结果。
type RoutingPublish struct {
	Revision      string
	AffectedNodes int
}

// onlineNodeWindow 是「在线节点」的心跳窗口：服务器 90 秒内有心跳。
const onlineNodeWindow = 90 * time.Second

type revOutbound struct {
	Tag      string          `json:"tag"`
	Type     string          `json:"type"`
	Settings json.RawMessage `json:"settings"`
}

type revRule struct {
	Priority    int             `json:"priority"`
	Matcher     json.RawMessage `json:"matcher"`
	OutboundTag string          `json:"outbound_tag"`
	Enabled     bool            `json:"enabled"`
	Note        string          `json:"note"`
}

// routingWriteErr 把 repo 写路由时的约束错误译成对外错误。
func routingWriteErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, repo.ErrRouteGroupNameTaken) {
		return &errs.Error{
			Code:    errs.Conflict,
			Message: "路由组名称已存在",
			Fields:  map[string]string{"name": "路由组名称已存在"},
		}
	}
	if errors.Is(err, repo.ErrOutboundTagTaken) {
		return errs.Newf(errs.Conflict, "出站 tag 重复")
	}
	if errors.Is(err, repo.ErrRoutingCheck) {
		return errs.Wrap(errs.Internal, err)
	}
	return err
}

func danglingRefKey(d repo.DanglingRef) string {
	return d.Kind + "\x00" + d.Name + "\x00" + d.Tag
}

func danglingRefSet(refs []repo.DanglingRef) map[string]bool {
	out := make(map[string]bool, len(refs))
	for _, d := range refs {
		out[danglingRefKey(d)] = true
	}
	return out
}

func formatDanglingRef(d repo.DanglingRef) string {
	kind := "节点"
	if d.Kind == "group" {
		kind = "路由组"
	}
	return fmt.Sprintf("%s %s → %s", kind, d.Name, d.Tag)
}

// refuseNewDangling 只拒绝本次写新造成的悬空引用；存量悬空不拦。
func refuseNewDangling(ctx context.Context, s *Service, before []repo.DanglingRef, message string) error {
	after, err := s.repo.DanglingRefs(ctx)
	if err != nil {
		return err
	}
	was := danglingRefSet(before)
	var fresh []string
	for _, d := range after {
		if !was[danglingRefKey(d)] {
			fresh = append(fresh, formatDanglingRef(d))
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	sort.Strings(fresh)
	return errs.Newf(errs.Conflict, message+"："+strings.Join(fresh, "、"))
}

func loadScope(ctx context.Context, s *Service, sc repo.RoutingScope) ([]RoutingOutbound, []RoutingRule, error) {
	om, rm, err := s.repo.ScopeRouting(ctx, sc)
	if err != nil {
		return nil, nil, err
	}
	outs := make([]RoutingOutbound, 0, len(om))
	for _, m := range om {
		outs = append(outs, outboundFromModel(m))
	}
	routes := make([]RoutingRule, 0, len(rm))
	for _, m := range rm {
		routes = append(routes, ruleFromModel(m))
	}
	return outs, routes, nil
}

// routingRevision 对按存储顺序的出站与规则做规范 JSON 的 sha256；settings / matcher 取库里 jsonb 的文本形态，同一份存储值每次读出来一样。
func routingRevision(outs []RoutingOutbound, routes []RoutingRule) (string, error) {
	revOuts := make([]revOutbound, len(outs))
	for i, o := range outs {
		revOuts[i] = revOutbound(o)
	}
	revRoutes := make([]revRule, len(routes))
	for i, r := range routes {
		revRoutes[i] = revRule(r)
	}
	canonical, err := json.Marshal(struct {
		Outbounds []revOutbound `json:"outbounds"`
		Routes    []revRule     `json:"routes"`
	}{revOuts, revRoutes})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func visibleTagMap(ctx context.Context, s *Service, nodeID *uuid.UUID) (map[string]bool, error) {
	tags, err := s.repo.VisibleOutboundTags(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	m := make(map[string]bool, len(tags))
	for _, t := range tags {
		m[t] = true
	}
	return m, nil
}

// GetGlobalRouting 读取全局出站与规则及在线在役节点数。
func (s *Service) GetGlobalRouting(ctx context.Context) (GlobalRouting, error) {
	outs, routes, err := loadScope(ctx, s, repo.RoutingScope{})
	if err != nil {
		return GlobalRouting{}, err
	}
	rev, err := routingRevision(outs, routes)
	if err != nil {
		return GlobalRouting{}, err
	}
	online, err := s.repo.OnlineServedNodeCount(ctx, s.now().Add(-onlineNodeWindow))
	if err != nil {
		return GlobalRouting{}, err
	}
	return GlobalRouting{Revision: rev, Outbounds: outs, Routes: routes, OnlineNodes: online}, nil
}

// SetGlobalRouting 全量替换全局出站与规则；提交后推进配置纪元，会话层现算配置文档并按摘要重发（不再使用 config_source_generation 与发布锁）。
func (s *Service) SetGlobalRouting(ctx context.Context, expectedRevision string, in RoutingInput) (RoutingPublish, error) {
	tags, err := validateRoutingInput(&in)
	if err != nil {
		return RoutingPublish{}, err
	}
	if err := checkRouteRefs(in.Routes, tags); err != nil {
		return RoutingPublish{}, err
	}

	var out RoutingPublish
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockRoutingWrites(ctx); err != nil {
			return err
		}
		curOuts, curRoutes, err := loadScope(ctx, s, repo.RoutingScope{})
		if err != nil {
			return err
		}
		current, err := routingRevision(curOuts, curRoutes)
		if err != nil {
			return err
		}
		if current != expectedRevision {
			return &errs.Error{
				Code:    errs.Conflict,
				Message: "全局路由已被其他管理员修改，请刷新后重试",
				Fields:  map[string]string{"expected_revision": "current=" + current},
			}
		}
		before, err := s.repo.DanglingRefs(ctx)
		if err != nil {
			return err
		}
		if err := s.repo.ReplaceScopeRouting(ctx, repo.RoutingScope{}, outboundsToModel(in.Outbounds), rulesToModel(in.Routes)); err != nil {
			return routingWriteErr(err)
		}
		if err := refuseNewDangling(ctx, s, before, "要删除的全局出站仍被规则引用"); err != nil {
			return err
		}
		affected, err := s.repo.AllLiveNodeCount(ctx)
		if err != nil {
			return err
		}
		newOuts, newRoutes, err := loadScope(ctx, s, repo.RoutingScope{})
		if err != nil {
			return err
		}
		out.Revision, err = routingRevision(newOuts, newRoutes)
		if err != nil {
			return err
		}
		out.AffectedNodes = affected
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action: "node.routing.global_publish", ResourceType: "node_routing", ResourceID: nil,
			Data: map[string]any{
				"before_revision": current, "revision": out.Revision,
				"outbounds": len(in.Outbounds), "routes": len(in.Routes), "affected_nodes": affected,
			},
		}); err != nil {
			return err
		}
		if affected > 0 {
			s.MarkConfigChanged(ctx)
		}
		return nil
	})
	if err != nil {
		return RoutingPublish{}, err
	}
	return out, nil
}

// GetNodeRouting 读取节点私有路由与它所在的路由组。
func (s *Service) GetNodeRouting(ctx context.Context, nodeID uuid.UUID) (NodeRouting, error) {
	n, found, err := s.repo.NodeByID(ctx, nodeID, false)
	if err != nil {
		return NodeRouting{}, err
	}
	if !found {
		return NodeRouting{}, errs.New(errs.NotFound)
	}
	rows, err := s.repo.NodeRouteGroups(ctx, []uuid.UUID{nodeID})
	if err != nil {
		return NodeRouting{}, err
	}
	groups := make([]RouteGroupRef, 0, len(rows))
	for _, row := range rows {
		groups = append(groups, RouteGroupRef{ID: row.GroupID, Name: row.Name, SortOrder: row.SortOrder})
	}
	sc := repo.RoutingScope{NodeID: &nodeID}
	outs, routes, err := loadScope(ctx, s, sc)
	if err != nil {
		return NodeRouting{}, err
	}
	return NodeRouting{RowVersion: n.RowVersion, Groups: groups, Outbounds: outs, Routes: routes}, nil
}

// SetNodeRouting 全量替换节点私有出站与规则，返回新的节点行版本。
func (s *Service) SetNodeRouting(ctx context.Context, nodeID uuid.UUID, rowVersion int64, in RoutingInput) (int64, error) {
	if rowVersion <= 0 {
		return 0, errs.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	tags, err := validateRoutingInput(&in)
	if err != nil {
		return 0, err
	}

	var newVersion int64
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockRoutingWrites(ctx); err != nil {
			return err
		}
		n, found, err := s.repo.NodeByID(ctx, nodeID, true)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		if n.RowVersion != rowVersion {
			return nodeVersionConflict(n.RowVersion)
		}
		visible, err := visibleTagMap(ctx, s, &nodeID)
		if err != nil {
			return err
		}
		if err := checkRouteRefs(in.Routes, tags, visible); err != nil {
			return err
		}
		before, err := s.repo.DanglingRefs(ctx)
		if err != nil {
			return err
		}
		sc := repo.RoutingScope{NodeID: &nodeID}
		if err := s.repo.ReplaceScopeRouting(ctx, sc, outboundsToModel(in.Outbounds), rulesToModel(in.Routes)); err != nil {
			return routingWriteErr(err)
		}
		if err := refuseNewDangling(ctx, s, before, "要删除的节点出站仍被本节点的规则引用"); err != nil {
			return err
		}
		if err := s.repo.BumpNodeRowVersions(ctx, []uuid.UUID{nodeID}); err != nil {
			return err
		}
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action: "node.routing.update", ResourceType: "node", ResourceID: &nodeID,
			Data: map[string]any{
				"before_row_version": rowVersion,
				"outbounds":          len(in.Outbounds),
				"routes":             len(in.Routes),
			},
		}); err != nil {
			return err
		}
		if n.ServingStatus != NodeRetired {
			s.MarkConfigChanged(ctx)
		}
		newVersion = rowVersion + 1
		return nil
	})
	if err != nil {
		return 0, err
	}
	return newVersion, nil
}

// PreviewNodeRouting 只读预览节点合并后的生效路由。
func (s *Service) PreviewNodeRouting(ctx context.Context, nodeID uuid.UUID) (EffectiveRouting, error) {
	_, found, err := s.repo.NodeByID(ctx, nodeID, false)
	if err != nil {
		return EffectiveRouting{}, err
	}
	if !found {
		return EffectiveRouting{}, errs.New(errs.NotFound)
	}
	layers, groups, err := s.nodeRoutingLayers(ctx, []uuid.UUID{nodeID})
	if err != nil {
		return EffectiveRouting{}, err
	}
	outs, routes := mergeRouting(layers[nodeID])
	g := groups[nodeID]
	if g == nil {
		g = []RouteGroupRef{}
	}
	return EffectiveRouting{Groups: g, Outbounds: outs, Routes: routes}, nil
}

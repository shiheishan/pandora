// [INPUT]: 依赖 uniproxy_config.go 的 ValidateRoutingMatcher，依赖 config_publish.go 的 lockLegacyConfigRelease，依赖 platform 的 db/audit/httpx；读写 node_outbounds / node_routes
// [OUTPUT]: 对外提供 RoutingOutbound / RoutingRule、ValidateRoutingPayload，Service 的 GetGlobalRouting / SetGlobalRouting / GetNodeRouting / SetNodeRouting 及其输入输出类型
// [POS]: domain/nodefabric 的后台路由编辑（NODE-012）：全局与单节点两个范围的读、校验、整体替换、推进 generation 与审计都在这里；api/admin/node_routing.go 只做解析与写响应，提交后由 handler 通知节点；生效合并口径在 routing_merge.go

package nodefabric

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// RoutingOutbound 是后台编辑的一条出站。
type RoutingOutbound struct {
	Tag      string          `json:"tag"`
	Type     string          `json:"type"`
	Settings json.RawMessage `json:"settings"`
}

// RoutingRule 是后台编辑的一条分流规则（含停用的）。
type RoutingRule struct {
	Priority    int             `json:"priority"`
	Matcher     json.RawMessage `json:"matcher"`
	OutboundTag string          `json:"outbound_tag"`
	Enabled     bool            `json:"enabled"`
	Note        string          `json:"note"`
}

// ValidateRoutingPayload 做不依赖数据库的校验：出站 tag 与 type 必填、tag 不重复
// 也不占内置名（direct / block）、每条匹配器能跨内核下发、空匹配兜底只能在最后。
// 会就地规范化出站的 tag 与 type。重名与占用内置名按大小写不敏感判（宁严勿松），
// 返回的可引用 tag 却是原样的（不含内置两个）：引用校验与下发同一口径，见 checkRouteRefs。
// 规则指向的出站是否存在还要连库里其他范围的出站一起看，由调用方在事务里查。
// 各范围的保存入口共用它，规则永远一致。
func ValidateRoutingPayload(outbounds []RoutingOutbound, routes []RoutingRule) (map[string]bool, error) {
	seen := map[string]bool{"direct": true, "block": true}
	tags := map[string]bool{}
	for i := range outbounds {
		o := &outbounds[i]
		o.Tag = strings.TrimSpace(o.Tag)
		o.Type = strings.ToLower(strings.TrimSpace(o.Type))
		if o.Tag == "" || o.Type == "" {
			return nil, httpx.Invalid(map[string]string{"outbounds": "每条出站都要有 tag 和 type"})
		}
		tagKey := strings.ToLower(o.Tag)
		if seen[tagKey] {
			return nil, httpx.Invalid(map[string]string{"outbounds": "出站 tag 重复或占用内置名称：" + o.Tag})
		}
		seen[tagKey] = true
		tags[o.Tag] = true
	}
	lastEnabled := -1
	for i, x := range routes {
		if x.Enabled {
			lastEnabled = i
		}
	}
	for i, x := range routes {
		empty, err := ValidateRoutingMatcher(x.Matcher)
		if err != nil {
			return nil, httpx.Invalid(map[string]string{
				"routes": fmt.Sprintf("第 %d 条规则无法跨内核下发：%v", i+1, err)})
		}
		if x.Enabled && empty && i != lastEnabled {
			return nil, httpx.Invalid(map[string]string{
				"routes": fmt.Sprintf("第 %d 条空匹配兜底规则必须放在最后", i+1)})
		}
	}
	return tags, nil
}

// isBuiltinOutbound 判断规则是否指向内置的 direct / block（去空白、不分大小写，规范形见 canonicalRouteTag）。
func isBuiltinOutbound(tag string) bool {
	t := canonicalRouteTag(tag)
	return t == "direct" || t == "block"
}

// checkRouteRefs 要求每条规则指向内置出站或 visible 里的出站。自定义出站按原样精确比较：
// 合并下发（MergeRouting）与 pdnd 的路由引擎（route/rule.go、core/*/routing.go）都按 tag
// 原样查表、不做规范化，大小写或空白不同的引用在节点上就是一条指向不存在出站的规则。
func checkRouteRefs(routes []RoutingRule, visible ...map[string]bool) error {
	for i, x := range routes {
		found := isBuiltinOutbound(x.OutboundTag)
		for _, v := range visible {
			if found {
				break
			}
			found = v[x.OutboundTag]
		}
		if !found {
			return httpx.Invalid(map[string]string{
				"routes": fmt.Sprintf("第 %d 条规则指向不存在的出站 %q", i+1, x.OutboundTag)})
		}
	}
	return nil
}

//------------------------------------------------------------------------------
// 范围读写：全局（两个 ID 都空）/ 路由组 / 节点三选一，与 00096 的 CHECK 同构
//------------------------------------------------------------------------------

// routingScope 是 node_outbounds / node_routes 的一个范围。
type routingScope struct{ NodeID, GroupID string }

var globalScope = routingScope{}

// scopeWhere 是按范围过滤的谓词，占位符固定为 $1 租户、$2 节点、$3 组。
// IS NOT DISTINCT FROM 让空 ID 对上 NULL，一条 SQL 管三种范围。
const scopeWhere = `tenant_id = $1 AND node_id IS NOT DISTINCT FROM nullif($2, '')::uuid
		   AND group_id IS NOT DISTINCT FROM nullif($3, '')::uuid`

// loadScopeRoutingTx 按存储顺序读一个范围的出站与全部规则（含停用的）。
// jsonb 列取库里的文本形态，同一份存储值每次读出来都一样（全局 revision 依赖这点）。
func loadScopeRoutingTx(ctx context.Context, tx pgx.Tx, tenantID string, sc routingScope) ([]RoutingOutbound, []RoutingRule, error) {
	outs := []RoutingOutbound{}
	rows, err := tx.Query(ctx, `
		SELECT tag, type, settings::text FROM node_outbounds
		 WHERE `+scopeWhere+`
		 ORDER BY sort_order, tag`, tenantID, sc.NodeID, sc.GroupID)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var o RoutingOutbound
		var settings string
		if err := rows.Scan(&o.Tag, &o.Type, &settings); err != nil {
			rows.Close()
			return nil, nil, err
		}
		o.Settings = json.RawMessage(settings)
		outs = append(outs, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	routes := []RoutingRule{}
	rrows, err := tx.Query(ctx, `
		SELECT priority, matcher::text, outbound_tag, enabled, coalesce(note, '')
		  FROM node_routes
		 WHERE `+scopeWhere+`
		 ORDER BY priority, created_at`, tenantID, sc.NodeID, sc.GroupID)
	if err != nil {
		return nil, nil, err
	}
	defer rrows.Close()
	for rrows.Next() {
		var x RoutingRule
		var matcher string
		if err := rrows.Scan(&x.Priority, &matcher, &x.OutboundTag, &x.Enabled, &x.Note); err != nil {
			return nil, nil, err
		}
		x.Matcher = json.RawMessage(matcher)
		routes = append(routes, x)
	}
	return outs, routes, rrows.Err()
}

// replaceScopeRoutingTx 整体替换一个范围的出站与规则。
//
// 全量而非增量：分流规则是有顺序的整体，增量接口会让「调整顺序」
// 这种最常见的操作变成一串难以原子化的增删。整体替换在一个事务里完成，
// 要么全成要么全不成，也不会出现规则指向刚被删掉的出站这种中间态。
func replaceScopeRoutingTx(ctx context.Context, tx pgx.Tx, tenantID string, sc routingScope,
	outbounds []RoutingOutbound, routes []RoutingRule) error {
	if _, err := tx.Exec(ctx, `DELETE FROM node_routes WHERE `+scopeWhere, tenantID, sc.NodeID, sc.GroupID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM node_outbounds WHERE `+scopeWhere, tenantID, sc.NodeID, sc.GroupID); err != nil {
		return err
	}
	for i, o := range outbounds {
		settings := o.Settings
		if len(settings) == 0 {
			settings = json.RawMessage("{}")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO node_outbounds (tenant_id, node_id, group_id, tag, type, settings, sort_order)
			VALUES ($1, nullif($2, '')::uuid, nullif($3, '')::uuid, $4, $5, $6, $7)`,
			tenantID, sc.NodeID, sc.GroupID, o.Tag, o.Type, settings, i*10); err != nil {
			return err
		}
	}
	for i, x := range routes {
		matcher := x.Matcher
		if len(matcher) == 0 {
			matcher = json.RawMessage("{}")
		}
		pri := x.Priority
		if pri == 0 {
			pri = (i + 1) * 10
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO node_routes (tenant_id, node_id, group_id, priority, matcher, outbound_tag, enabled, note)
			VALUES ($1, nullif($2, '')::uuid, nullif($3, '')::uuid, $4, $5, $6, $7, nullif($8, ''))`,
			tenantID, sc.NodeID, sc.GroupID, pri, matcher, canonicalRouteTag(x.OutboundTag), x.Enabled, x.Note); err != nil {
			return err
		}
	}
	return nil
}

//------------------------------------------------------------------------------
// 全局路由（契约后台-07 GET / PUT v1/nodes/routing）
//------------------------------------------------------------------------------

// GlobalRouting 是全局出站与规则，Revision 为乐观并发凭据。
type GlobalRouting struct {
	Revision    string            `json:"revision"`
	Outbounds   []RoutingOutbound `json:"outbounds"`
	Routes      []RoutingRule     `json:"routes"`
	OnlineNodes int               `json:"online_nodes"`
}

// loadGlobalRoutingTx 读全局出站与规则，并算出 revision：按存储顺序的规范 JSON 的
// sha256，空集也有值。
func loadGlobalRoutingTx(ctx context.Context, tx pgx.Tx, tenantID string) (*GlobalRouting, error) {
	outs, routes, err := loadScopeRoutingTx(ctx, tx, tenantID, globalScope)
	if err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(struct {
		Outbounds []RoutingOutbound `json:"outbounds"`
		Routes    []RoutingRule     `json:"routes"`
	}{outs, routes})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	return &GlobalRouting{Revision: hex.EncodeToString(sum[:]), Outbounds: outs, Routes: routes}, nil
}

// GetGlobalRouting 读全局路由，并附上发布确认框的「会下发到 M 个在线节点」。
func (s *Service) GetGlobalRouting(ctx context.Context, tenantID string) (*GlobalRouting, error) {
	var out *GlobalRouting
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var err error
		if out, err = loadGlobalRoutingTx(ctx, tx, tenantID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT count(*) FROM nodes
			 WHERE tenant_id = $1 AND status <> 'destroyed' AND serving_status = 'active'
			   AND last_heartbeat_at >= now() - interval '90 seconds'`, tenantID).Scan(&out.OnlineNodes)
	})
	return out, err
}

// SetGlobalRoutingInput 是全局路由的整体替换请求。
type SetGlobalRoutingInput struct {
	TenantID, ActorID string
	ExpectedRevision  string
	Outbounds         []RoutingOutbound
	Routes            []RoutingRule
}

// RoutingPublishResult 是一次路由发布的结果：新 revision 与被推进 generation 的节点，
// 调用方在提交后逐个通知。
type RoutingPublishResult struct {
	Revision string
	NodeIDs  []string
}

// SetGlobalRouting 全量替换全局出站与规则并发布到全部未退役节点。
//
// 乐观并发用 revision 而不是行版本：全局配置分散在两张表的多行里，没有一个
// 可以挂版本号的行。持 node-config-release 锁，与节点发布、退役串行。
func (s *Service) SetGlobalRouting(ctx context.Context, in SetGlobalRoutingInput) (*RoutingPublishResult, error) {
	if in.Outbounds == nil {
		in.Outbounds = []RoutingOutbound{}
	}
	if in.Routes == nil {
		in.Routes = []RoutingRule{}
	}
	tags, err := ValidateRoutingPayload(in.Outbounds, in.Routes)
	if err != nil {
		return nil, err
	}
	// 全局规则只能指向全局出站或内置出站：节点私有出站不在全部节点上
	if err := checkRouteRefs(in.Routes, tags); err != nil {
		return nil, err
	}

	out := &RoutingPublishResult{}
	err = s.pool.InTx(ctx, db.Scope{TenantID: in.TenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		if err := lockLegacyConfigRelease(ctx, tx, in.TenantID); err != nil {
			return err
		}
		current, err := loadGlobalRoutingTx(ctx, tx, in.TenantID)
		if err != nil {
			return err
		}
		if current.Revision != in.ExpectedRevision {
			return &httpx.Error{Code: httpx.CodeConflict, Message: "全局路由已被其他管理员修改，请刷新后重试",
				Fields: map[string]string{"expected_revision": "current=" + current.Revision}}
		}
		// 要删掉的全局出站如果还被组或节点的规则引用，拒绝并列出引用方
		before, err := danglingRefsTx(ctx, tx, in.TenantID)
		if err != nil {
			return err
		}
		if err := replaceScopeRoutingTx(ctx, tx, in.TenantID, globalScope, in.Outbounds, in.Routes); err != nil {
			return err
		}
		if err := refuseNewDanglingTx(ctx, tx, in.TenantID, before, "要删除的全局出站仍被规则引用"); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			UPDATE nodes SET config_source_generation = config_source_generation + 1
			 WHERE tenant_id = $1 AND status NOT IN ('destroyed','retired') AND serving_status <> 'retired'
			RETURNING id::text`, in.TenantID)
		if err != nil {
			return err
		}
		if out.NodeIDs, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		next, err := loadGlobalRoutingTx(ctx, tx, in.TenantID)
		if err != nil {
			return err
		}
		out.Revision = next.Revision
		return audit.Write(ctx, tx, in.TenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID,
			Action: "node.routing.global_publish", ResourceType: "node_routing",
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
			BeforeDigest: map[string]any{"revision": current.Revision,
				"outbounds": len(current.Outbounds), "routes": len(current.Routes)},
			AfterDigest: map[string]any{"revision": out.Revision,
				"outbounds": len(in.Outbounds), "routes": len(in.Routes), "affected_nodes": len(out.NodeIDs)},
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

//------------------------------------------------------------------------------
// 单节点路由（GET / PUT v1/nodes/{id}/routing）
//------------------------------------------------------------------------------

// NodeRouting 是某节点私有的出站与规则与它所在的路由组（按生效顺序），RowVersion 为节点行版本。
type NodeRouting struct {
	RowVersion int64             `json:"row_version"`
	Groups     []RouteGroupRef   `json:"groups"`
	Outbounds  []RoutingOutbound `json:"outbounds"`
	Routes     []RoutingRule     `json:"routes"`
}

// GetNodeRouting 读取某节点的私有出站与分流，以及它所在的路由组。
func (s *Service) GetNodeRouting(ctx context.Context, tenantID, nodeID string) (*NodeRouting, error) {
	parsed, err := uuid.Parse(nodeID)
	if err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	nodeID = parsed.String()
	out := &NodeRouting{}
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT row_version FROM nodes
			WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, nodeID).Scan(&out.RowVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		var err error
		if out.Groups, err = nodeRouteGroupsTx(ctx, tx, tenantID, nodeID); err != nil {
			return err
		}
		out.Outbounds, out.Routes, err = loadScopeRoutingTx(ctx, tx, tenantID, routingScope{NodeID: nodeID})
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetNodeRoutingInput 是单节点路由的整体替换请求。
type SetNodeRoutingInput struct {
	TenantID, ActorID, NodeID string
	RowVersion                int64
	Outbounds                 []RoutingOutbound
	Routes                    []RoutingRule
}

// SetNodeRouting 全量替换某节点的私有出站与分流，返回新的节点行版本。
// 提交后由调用方通知节点。
func (s *Service) SetNodeRouting(ctx context.Context, in SetNodeRoutingInput) (int64, error) {
	tags, err := ValidateRoutingPayload(in.Outbounds, in.Routes)
	if err != nil {
		return 0, err
	}
	if in.RowVersion <= 0 {
		return 0, httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"})
	}
	if _, err := uuid.Parse(in.NodeID); err != nil {
		return 0, httpx.NotFoundOrForbidden()
	}
	err = s.pool.InTx(ctx, db.Scope{TenantID: in.TenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		// 引用校验要读所在组的出站：与改成员、改组内出站的写用同一把发布锁串行，
		// 否则并发移出组后本节点规则会指向已看不见的出站
		if err := lockLegacyConfigRelease(ctx, tx, in.TenantID); err != nil {
			return err
		}
		// 空替换也必须落在真实节点上；锁节点行让路由替换与节点生命周期、移动串行
		var currentVersion int64
		if err := tx.QueryRow(ctx, `SELECT row_version FROM nodes
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, in.TenantID, in.NodeID).Scan(&currentVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		if currentVersion != in.RowVersion {
			return &httpx.Error{Code: httpx.CodeConflict, Message: "节点已被其他管理员修改，请刷新后重试",
				Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", currentVersion)}}
		}
		// 规则可以指向 direct/block、本次提交的私有出站，也可以指向本节点看得见的
		// 全局出站与所在路由组的出站：下发时本来就把这几层合在一起。
		// 原先只认前两类，单节点规则没法用「US-LAX-01」这种公共中转（缺陷 18）
		visible, err := visibleOutboundTagsTx(ctx, tx, in.TenantID, in.NodeID)
		if err != nil {
			return err
		}
		if err := checkRouteRefs(in.Routes, tags, visible); err != nil {
			return err
		}
		if err := replaceScopeRoutingTx(ctx, tx, in.TenantID, routingScope{NodeID: in.NodeID}, in.Outbounds, in.Routes); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE nodes SET row_version=row_version+1,
			config_source_generation=config_source_generation+1
			WHERE tenant_id=$1 AND id=$2::uuid AND row_version=$3`, in.TenantID, in.NodeID, currentVersion); err != nil {
			return err
		}
		return audit.Write(ctx, tx, in.TenantID, audit.Entry{
			ActorKind: "admin", ActorID: &in.ActorID,
			Action: "node.routing.update", ResourceType: "node", ResourceID: &in.NodeID,
			APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{
				"outbounds": len(in.Outbounds), "routes": len(in.Routes),
			},
		})
	})
	if err != nil {
		return 0, err
	}
	return in.RowVersion + 1, nil
}

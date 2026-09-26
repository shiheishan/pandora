// [INPUT]: 依赖 domain/nodefabric 的 ValidateRoutingMatcher 与 NotifyNodeChanged，依赖 platform 的 db/audit/httpx；读写 node_outbounds / node_routes（node_id 为空即全局）
// [OUTPUT]: 对外提供 validateRoutingPayload、routingOutbound / routingRule / routingPayload，handlers 的 nodeGetGlobalRouting / nodeSetGlobalRouting 与单节点的 nodeGetRouting / nodeSetRouting；包内 loadGlobalOutboundTags
// [POS]: api/admin 的出站与分流（NODE-012）：全局路由（契约后台-07 GET / PUT v1/nodes/routing）与单节点路由（从 handlers.go 挪来）共用同一个校验函数；单节点路由保存在事务提交后才通知节点；生效口径（节点私有规则在前、全局在后）在 nodefabric 的两个加载函数里
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// validateRoutingPayload 做不依赖数据库的校验：出站 tag 与 type 必填、tag 不重复
// 也不占内置名（direct / block）、每条匹配器能跨内核下发、空匹配兜底只能在最后。
// 会就地规范化出站的 tag 与 type。返回可引用的出站 tag（小写，含内置两个）；
// 规则指向的出站是否存在还要连库里的全局出站一起看，由调用方在事务里查。
// 单节点与全局两个保存入口共用它，两边的规则永远一致。
func validateRoutingPayload(outbounds []routingOutbound, routes []routingRule) (map[string]bool, error) {
	tags := map[string]bool{"direct": true, "block": true}
	for i := range outbounds {
		o := &outbounds[i]
		o.Tag = strings.TrimSpace(o.Tag)
		o.Type = strings.ToLower(strings.TrimSpace(o.Type))
		if o.Tag == "" || o.Type == "" {
			return nil, httpx.Invalid(map[string]string{"outbounds": "每条出站都要有 tag 和 type"})
		}
		tagKey := strings.ToLower(o.Tag)
		if tags[tagKey] {
			return nil, httpx.Invalid(map[string]string{"outbounds": "出站 tag 重复或占用内置名称：" + o.Tag})
		}
		tags[tagKey] = true
	}
	lastEnabled := -1
	for i, x := range routes {
		if x.Enabled {
			lastEnabled = i
		}
	}
	for i, x := range routes {
		empty, err := nodefabric.ValidateRoutingMatcher(x.Matcher)
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

type globalRouting struct {
	Revision    string            `json:"revision"`
	Outbounds   []routingOutbound `json:"outbounds"`
	Routes      []routingRule     `json:"routes"`
	OnlineNodes int               `json:"online_nodes,omitempty"`
}

// loadGlobalRouting 读全局出站与规则，并算出 revision：按存储顺序的规范 JSON 的
// sha256。jsonb 列取库里的文本形态，同一份存储值每次读出来都一样，空集也有值。
func loadGlobalRouting(ctx context.Context, tx pgx.Tx, tenantID string) (*globalRouting, error) {
	out := &globalRouting{Outbounds: []routingOutbound{}, Routes: []routingRule{}}
	rows, err := tx.Query(ctx, `
		SELECT tag, type, settings::text FROM node_outbounds
		 WHERE tenant_id = $1 AND node_id IS NULL
		 ORDER BY sort_order, tag`, tenantID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o routingOutbound
		var settings string
		if err := rows.Scan(&o.Tag, &o.Type, &settings); err != nil {
			rows.Close()
			return nil, err
		}
		o.Settings = json.RawMessage(settings)
		out.Outbounds = append(out.Outbounds, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rrows, err := tx.Query(ctx, `
		SELECT priority, matcher::text, outbound_tag, enabled, coalesce(note, '')
		  FROM node_routes
		 WHERE tenant_id = $1 AND node_id IS NULL
		 ORDER BY priority, created_at`, tenantID)
	if err != nil {
		return nil, err
	}
	for rrows.Next() {
		var x routingRule
		var matcher string
		if err := rrows.Scan(&x.Priority, &matcher, &x.OutboundTag, &x.Enabled, &x.Note); err != nil {
			rrows.Close()
			return nil, err
		}
		x.Matcher = json.RawMessage(matcher)
		out.Routes = append(out.Routes, x)
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(struct {
		Outbounds []routingOutbound `json:"outbounds"`
		Routes    []routingRule     `json:"routes"`
	}{out.Outbounds, out.Routes})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	out.Revision = hex.EncodeToString(sum[:])
	return out, nil
}

func (h *handlers) nodeGetGlobalRouting(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	var out *globalRouting
	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var err error
		if out, err = loadGlobalRouting(r.Context(), tx, tenantID); err != nil {
			return err
		}
		// 发布确认框的「会下发到 M 个在线节点」
		return tx.QueryRow(r.Context(), `
			SELECT count(*) FROM nodes
			 WHERE tenant_id = $1 AND status <> 'destroyed' AND serving_status = 'active'
			   AND last_heartbeat_at >= now() - interval '90 seconds'`, tenantID).Scan(&out.OnlineNodes)
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	httpx.OK(w, map[string]any{"revision": out.Revision, "outbounds": out.Outbounds,
		"routes": out.Routes, "online_nodes": out.OnlineNodes})
}

type globalRoutingReq struct {
	ExpectedRevision string            `json:"expected_revision"`
	Outbounds        []routingOutbound `json:"outbounds"`
	Routes           []routingRule     `json:"routes"`
}

// nodeSetGlobalRouting 全量替换全局出站与规则并发布到全部未退役节点。
//
// 乐观并发用 revision 而不是行版本：全局配置分散在两张表的多行里，没有一个
// 可以挂版本号的行。持 node-config-release 锁，与节点发布、退役串行。
func (h *handlers) nodeSetGlobalRouting(w http.ResponseWriter, r *http.Request) {
	var req globalRoutingReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.Outbounds == nil {
		req.Outbounds = []routingOutbound{}
	}
	if req.Routes == nil {
		req.Routes = []routingRule{}
	}
	tags, err := validateRoutingPayload(req.Outbounds, req.Routes)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 全局规则只能指向全局出站或内置出站：节点私有出站不在全部节点上
	for i, x := range req.Routes {
		if !tags[strings.ToLower(strings.TrimSpace(x.OutboundTag))] {
			httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{
				"routes": fmt.Sprintf("第 %d 条规则指向不存在的出站 %q", i+1, x.OutboundTag)}))
			return
		}
	}

	tenantID := httpx.TenantIDFrom(r.Context())
	actor := httpx.PrincipalFrom(r.Context()).UserID
	var nodeIDs []string
	var revision string
	err = h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID, ActorID: actor}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(),
			`SELECT pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`,
			"node-config-release/"+tenantID); err != nil {
			return err
		}
		current, err := loadGlobalRouting(r.Context(), tx, tenantID)
		if err != nil {
			return err
		}
		if current.Revision != req.ExpectedRevision {
			return &httpx.Error{Code: httpx.CodeConflict, Message: "全局路由已被其他管理员修改，请刷新后重试",
				Fields: map[string]string{"expected_revision": "current=" + current.Revision}}
		}
		// 要删掉的全局出站如果还被某个节点的私有规则引用，拒绝并列出节点
		keep := map[string]bool{}
		for _, o := range req.Outbounds {
			keep[strings.ToLower(o.Tag)] = true
		}
		var removed []string
		for _, o := range current.Outbounds {
			if !keep[strings.ToLower(o.Tag)] {
				removed = append(removed, strings.ToLower(o.Tag))
			}
		}
		if len(removed) > 0 {
			var users []string
			if err := tx.QueryRow(r.Context(), `
				SELECT coalesce(array_agg(DISTINCT coalesce(n.display_name, n.name) ORDER BY coalesce(n.display_name, n.name)), '{}')
				  FROM node_routes nr
				  JOIN nodes n ON n.tenant_id = nr.tenant_id AND n.id = nr.node_id
				 WHERE nr.tenant_id = $1 AND nr.node_id IS NOT NULL
				   AND lower(nr.outbound_tag) = ANY($2::text[])
				   AND NOT EXISTS (SELECT 1 FROM node_outbounds o WHERE o.tenant_id = nr.tenant_id
				                    AND o.node_id = nr.node_id AND lower(o.tag) = lower(nr.outbound_tag))`,
				tenantID, removed).Scan(&users); err != nil {
				return err
			}
			if len(users) > 0 {
				return &httpx.Error{Code: httpx.CodeConflict,
					Message: "要删除的全局出站仍被节点规则引用：" + strings.Join(users, "、")}
			}
		}

		if _, err := tx.Exec(r.Context(), `DELETE FROM node_routes WHERE tenant_id = $1 AND node_id IS NULL`, tenantID); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `DELETE FROM node_outbounds WHERE tenant_id = $1 AND node_id IS NULL`, tenantID); err != nil {
			return err
		}
		for i, o := range req.Outbounds {
			settings := o.Settings
			if len(settings) == 0 {
				settings = json.RawMessage("{}")
			}
			if _, err := tx.Exec(r.Context(), `
				INSERT INTO node_outbounds (tenant_id, node_id, tag, type, settings, sort_order)
				VALUES ($1, NULL, $2, $3, $4, $5)`, tenantID, o.Tag, o.Type, settings, i*10); err != nil {
				return err
			}
		}
		for i, x := range req.Routes {
			matcher := x.Matcher
			if len(matcher) == 0 {
				matcher = json.RawMessage("{}")
			}
			pri := x.Priority
			if pri == 0 {
				pri = (i + 1) * 10
			}
			if _, err := tx.Exec(r.Context(), `
				INSERT INTO node_routes (tenant_id, node_id, priority, matcher, outbound_tag, enabled, note)
				VALUES ($1, NULL, $2, $3, $4, $5, nullif($6, ''))`,
				tenantID, pri, matcher, x.OutboundTag, x.Enabled, x.Note); err != nil {
				return err
			}
		}
		rows, err := tx.Query(r.Context(), `
			UPDATE nodes SET config_source_generation = config_source_generation + 1
			 WHERE tenant_id = $1 AND status NOT IN ('destroyed','retired') AND serving_status <> 'retired'
			RETURNING id::text`, tenantID)
		if err != nil {
			return err
		}
		if nodeIDs, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		next, err := loadGlobalRouting(r.Context(), tx, tenantID)
		if err != nil {
			return err
		}
		revision = next.Revision
		return audit.Write(r.Context(), tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actor,
			Action: "node.routing.global_publish", ResourceType: "node_routing",
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(r.Context()),
			BeforeDigest: map[string]any{"revision": current.Revision,
				"outbounds": len(current.Outbounds), "routes": len(current.Routes)},
			AfterDigest: map[string]any{"revision": revision,
				"outbounds": len(req.Outbounds), "routes": len(req.Routes), "affected_nodes": len(nodeIDs)},
		})
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 提交之后再推：推送失败不影响已经落库的配置，节点下次拉取同样拿到新版
	if h.d.Node != nil {
		for _, id := range nodeIDs {
			h.d.Node.NotifyNodeChanged(r.Context(), tenantID, id)
		}
	}
	httpx.OK(w, map[string]any{"ok": true, "revision": revision, "affected_nodes": len(nodeIDs)})
}

// --- 出站与分流（NODE-012）---

type routingOutbound struct {
	Tag      string          `json:"tag"`
	Type     string          `json:"type"`
	Settings json.RawMessage `json:"settings"`
}

type routingRule struct {
	Priority    int             `json:"priority"`
	Matcher     json.RawMessage `json:"matcher"`
	OutboundTag string          `json:"outbound_tag"`
	Enabled     bool            `json:"enabled"`
	Note        string          `json:"note"`
}

type routingPayload struct {
	RowVersion int64             `json:"row_version"`
	Outbounds  []routingOutbound `json:"outbounds"`
	Routes     []routingRule     `json:"routes"`
}

// nodeGetRouting 读取某节点的出站与分流。
func (h *handlers) nodeGetRouting(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	tenantID := httpx.TenantIDFrom(r.Context())
	out := routingPayload{Outbounds: []routingOutbound{}, Routes: []routingRule{}}

	err := h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT row_version FROM nodes
			WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, id).Scan(&out.RowVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		rows, err := tx.Query(r.Context(), `
			SELECT tag, type, settings FROM node_outbounds
			 WHERE tenant_id=$1 AND node_id=$2::uuid
			 ORDER BY sort_order, tag`, tenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var o routingOutbound
			if err := rows.Scan(&o.Tag, &o.Type, &o.Settings); err != nil {
				return err
			}
			out.Outbounds = append(out.Outbounds, o)
		}
		if err := rows.Err(); err != nil {
			return err
		}

		rrows, err := tx.Query(r.Context(), `
			SELECT priority, matcher, outbound_tag, enabled, coalesce(note,'')
			  FROM node_routes
			 WHERE tenant_id=$1 AND node_id=$2::uuid
			 ORDER BY priority, created_at`, tenantID, id)
		if err != nil {
			return err
		}
		defer rrows.Close()
		for rrows.Next() {
			var x routingRule
			if err := rrows.Scan(&x.Priority, &x.Matcher, &x.OutboundTag,
				&x.Enabled, &x.Note); err != nil {
				return err
			}
			out.Routes = append(out.Routes, x)
		}
		return rrows.Err()
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.Internal(err))
		return
	}
	httpx.OK(w, out)
}

// nodeSetRouting 全量替换某节点的出站与分流。
//
// 全量而非增量：分流规则是有顺序的整体，增量接口会让「调整顺序」
// 这种最常见的操作变成一串难以原子化的增删。整体替换在一个事务里完成，
// 要么全成要么全不成，也不会出现规则指向刚被删掉的出站这种中间态。
func (h *handlers) nodeSetRouting(w http.ResponseWriter, r *http.Request) {
	var req routingPayload
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	tags, err := validateRoutingPayload(req.Outbounds, req.Routes)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	id := chi.URLParam(r, "id")
	tenantID := httpx.TenantIDFrom(r.Context())
	actor := httpx.PrincipalFrom(r.Context()).UserID
	if req.RowVersion <= 0 {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"row_version": "必须提供正整数版本号"}))
		return
	}

	err = h.d.Pool.InTx(r.Context(), db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			// Even an empty replacement must target a real Node. Locking the row also
			// serializes routing replacement with Node lifecycle/move operations.
			var lockedID string
			var currentVersion int64
			if err := tx.QueryRow(r.Context(), `SELECT id FROM nodes
				WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, id).Scan(&lockedID); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return httpx.NotFoundOrForbidden()
				}
				return err
			}
			if err := tx.QueryRow(r.Context(), `SELECT row_version FROM nodes
				WHERE tenant_id=$1 AND id=$2::uuid`, tenantID, id).Scan(&currentVersion); err != nil {
				return err
			}
			if currentVersion != req.RowVersion {
				return &httpx.Error{Code: httpx.CodeConflict, Message: "节点已被其他管理员修改，请刷新后重试",
					Fields: map[string]string{"row_version": fmt.Sprintf("current=%d", currentVersion)}}
			}
			// 规则可以指向 direct/block、本次提交的私有出站，也可以指向全局出站
			// （node_id IS NULL）：下发时 LoadRouting 本来就把两者合在一起。
			// 原先只认前两类，单节点规则没法用「US-LAX-01」这种公共中转（缺陷 18）
			globalTags, err := loadGlobalOutboundTags(r.Context(), tx, tenantID)
			if err != nil {
				return err
			}
			for i, x := range req.Routes {
				tag := strings.ToLower(strings.TrimSpace(x.OutboundTag))
				if !tags[tag] && !globalTags[tag] {
					return httpx.Invalid(map[string]string{
						"routes": fmt.Sprintf("第 %d 条规则指向不存在的出站 %q", i+1, x.OutboundTag)})
				}
			}
			if _, err := tx.Exec(r.Context(),
				`DELETE FROM node_routes WHERE tenant_id=$1 AND node_id=$2::uuid`,
				tenantID, id); err != nil {
				return err
			}
			if _, err := tx.Exec(r.Context(),
				`DELETE FROM node_outbounds WHERE tenant_id=$1 AND node_id=$2::uuid`,
				tenantID, id); err != nil {
				return err
			}
			for i, o := range req.Outbounds {
				settings := o.Settings
				if len(settings) == 0 {
					settings = json.RawMessage("{}")
				}
				if _, err := tx.Exec(r.Context(), `
					INSERT INTO node_outbounds (tenant_id, node_id, tag, type, settings, sort_order)
					VALUES ($1,$2::uuid,$3,$4,$5,$6)`,
					tenantID, id, o.Tag, o.Type, settings, i*10); err != nil {
					return err
				}
			}
			for i, x := range req.Routes {
				matcher := x.Matcher
				if len(matcher) == 0 {
					matcher = json.RawMessage("{}")
				}
				pri := x.Priority
				if pri == 0 {
					pri = (i + 1) * 10
				}
				if _, err := tx.Exec(r.Context(), `
					INSERT INTO node_routes
					  (tenant_id, node_id, priority, matcher, outbound_tag, enabled, note)
					VALUES ($1,$2::uuid,$3,$4,$5,$6,nullif($7,''))`,
					tenantID, id, pri, matcher, x.OutboundTag, x.Enabled, x.Note); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(r.Context(), `UPDATE nodes SET row_version=row_version+1,
				config_source_generation=config_source_generation+1
				WHERE tenant_id=$1 AND id=$2::uuid AND row_version=$3`, tenantID, id, currentVersion); err != nil {
				return err
			}
			return audit.Write(r.Context(), tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actor,
				Action: "node.routing.update", ResourceType: "node", ResourceID: &id,
				APIDomain: "admin", Outcome: "success",
				RequestID: httpx.RequestIDFrom(r.Context()),
				AfterDigest: map[string]any{
					"outbounds": len(req.Outbounds), "routes": len(req.Routes),
				},
			})
		})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 提交之后通知节点：原先只递增 config_source_generation，在线节点要等
	// 下一轮轮询才生效，长连接推送形同虚设（缺陷 18）
	h.d.Node.NotifyNodeChanged(r.Context(), tenantID, id)
	httpx.OK(w, map[string]any{"ok": true, "row_version": req.RowVersion + 1})
}

// loadGlobalOutboundTags 取租户的全局出站 tag（小写），供单节点规则校验引用。
func loadGlobalOutboundTags(ctx context.Context, tx pgx.Tx, tenantID string) (map[string]bool, error) {
	rows, err := tx.Query(ctx,
		`SELECT lower(tag) FROM node_outbounds WHERE tenant_id=$1 AND node_id IS NULL`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		out[tag] = true
	}
	return out, rows.Err()
}

package nodefabric

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// visibleOutboundTagsTx 取某节点除自己私有出站外看得见的出站 tag（原样，与下发同一口径）：
// 全局出站与它所在各路由组的出站。nodeID 为空时只剩全局，路由组的规则校验用它。
func visibleOutboundTagsTx(ctx context.Context, tx pgx.Tx, tenantID, nodeID string) (map[string]bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT o.tag FROM node_outbounds o
		 WHERE o.tenant_id = $1 AND o.node_id IS NULL
		   AND (o.group_id IS NULL OR o.group_id IN (
		        SELECT m.group_id FROM route_group_members m
		         WHERE m.tenant_id = $1 AND m.node_id = nullif($2, '')::uuid))`, tenantID, nodeID)
	if err != nil {
		return nil, err
	}
	tags, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(tags))
	for _, t := range tags {
		out[t] = true
	}
	return out, nil
}

// danglingRef 是一条指向不存在出站的规则：引用方（路由组或节点）与它指向的 tag。
type danglingRef struct {
	Kind, Name, Tag string
}

func (d danglingRef) String() string {
	kind := "节点"
	if d.Kind == "group" {
		kind = "路由组"
	}
	return fmt.Sprintf("%s %s → %s", kind, d.Name, d.Tag)
}

// danglingRefsTx 列出租户内全部悬空引用（含停用的规则：启用时不该突然失效）。
// 自定义出站按 tag 原样精确比较（与下发、pdnd 一致），内置 direct / block 沿用不分大小写。
// 全局规则的引用在保存时已对着本次提交校验，这里只看组与节点两种范围。
func danglingRefsTx(ctx context.Context, tx pgx.Tx, tenantID string) (map[danglingRef]bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT 'group', g.name, r.outbound_tag
		  FROM node_routes r
		  JOIN route_groups g ON g.tenant_id = r.tenant_id AND g.id = r.group_id
		 WHERE r.tenant_id = $1 AND lower(btrim(r.outbound_tag)) NOT IN ('direct', 'block')
		   AND NOT EXISTS (SELECT 1 FROM node_outbounds o
		                    WHERE o.tenant_id = r.tenant_id AND o.tag = r.outbound_tag
		                      AND o.node_id IS NULL AND (o.group_id IS NULL OR o.group_id = r.group_id))
		UNION
		SELECT 'node', coalesce(n.display_name, n.name), r.outbound_tag
		  FROM node_routes r
		  JOIN nodes n ON n.tenant_id = r.tenant_id AND n.id = r.node_id
		 WHERE r.tenant_id = $1 AND lower(btrim(r.outbound_tag)) NOT IN ('direct', 'block')
		   AND NOT EXISTS (SELECT 1 FROM node_outbounds o
		                    WHERE o.tenant_id = r.tenant_id AND o.tag = r.outbound_tag
		                      AND (o.node_id = r.node_id
		                           OR (o.node_id IS NULL AND o.group_id IS NULL)
		                           OR o.group_id IN (SELECT m.group_id FROM route_group_members m
		                                              WHERE m.tenant_id = r.tenant_id AND m.node_id = r.node_id)))`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[danglingRef]bool{}
	for rows.Next() {
		var d danglingRef
		if err := rows.Scan(&d.Kind, &d.Name, &d.Tag); err != nil {
			return nil, err
		}
		out[d] = true
	}
	return out, rows.Err()
}

// refuseNewDanglingTx 在写之后再取一次悬空引用，出现了写之前没有的就回 409 并列出引用方，
// 调用方的事务随之回滚。存量悬空（历史数据）不拦，免得一条旧数据卡住所有路由修改。
func refuseNewDanglingTx(ctx context.Context, tx pgx.Tx, tenantID string, before map[danglingRef]bool, message string) error {
	after, err := danglingRefsTx(ctx, tx, tenantID)
	if err != nil {
		return err
	}
	var fresh []string
	for d := range after {
		if !before[d] {
			fresh = append(fresh, d.String())
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	sort.Strings(fresh)
	return httpx.New(httpx.CodeConflict, message+"："+strings.Join(fresh, "、"))
}

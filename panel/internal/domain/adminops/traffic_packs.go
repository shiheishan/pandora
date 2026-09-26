// [INPUT]: 依赖 platform/db 的租户事务、platform/audit、platform/httpx，依赖 service.go 的 requireP0BSales 销售闸门与 catalog.go 的 catalogResult 错误翻译
// [OUTPUT]: 对外提供 TrafficPackRow、TrafficPackInput、ListTrafficPacks、CreateTrafficPack、UpdateTrafficPack、SetTrafficPackStatus
// [POS]: adminops 的流量包目录管理（后台-04 流量包 tab）：列表、新建、修改、上下架；与 catalog.go 的套餐目录并列，门户目录与下单在 billing/traffic_pack.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

// 流量包是在售商品，改它等于改价（D-C-2 同门槛）：路由上挂 catalog.publish、
// 近期重认证与幂等键，这里再过一道 P0B 销售闸门 —— 新建、修改、重新上架
// 都会让一个价格对用户生效；下架只收窄销售面，与 ArchivePlan 一样不过闸门。
//
// 已售出的不受影响：订单项在下单那一刻快照了名称、容量与金额，履约只认
// 快照（billing/traffic_pack.go），所以这里可以放心改容量和价格。
//
// 并发改同一个流量包用 updated_at 做乐观锁：traffic_packs 没有 row_version，
// 而 updated_at 由触发器在每次 UPDATE 时置为事务开始时刻，两个请求不可能
// 拿到同一个值。客户端把列表里读到的 updated_at 原样传回来即可。

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// maxTrafficPackBytes 是单个流量包的容量上限（1 PiB）。用户余额按笔累加，
// 上限卡在这里，求和就不可能逼近 bigint。
const maxTrafficPackBytes int64 = 1 << 50

// maxTrafficPackAmount 是单价上限（最小货币单位，即 100 万元 / 美元）。
const maxTrafficPackAmount int64 = 100_000_000

type TrafficPackRow struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	TrafficBytes int64  `json:"traffic_bytes"`
	Currency     string `json:"currency"`
	UnitAmount   int64  `json:"unit_amount"`
	Recommended  bool   `json:"recommended"`
	Status       string `json:"status"`
	SortOrder    int    `json:"sort_order"`
	// SoldCount 是已付款的流量包订单数（含之后退款的），与套餐的 stock_sold 同义。
	SoldCount int64     `json:"sold_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type TrafficPackInput struct {
	ActorID string `json:"-"`
	// ExpectedUpdatedAt 只在修改时必填：列表里读到的 updated_at。
	ExpectedUpdatedAt *time.Time `json:"expected_updated_at"`
	Name              string     `json:"name"`
	TrafficBytes      int64      `json:"traffic_bytes"`
	Currency          string     `json:"currency"`
	UnitAmount        int64      `json:"unit_amount"`
	Recommended       bool       `json:"recommended"`
	SortOrder         int        `json:"sort_order"`
}

const trafficPackSelectSQL = `
	SELECT p.id::text, p.name, p.traffic_bytes, p.currency::text, p.unit_amount,
	       p.recommended, p.status, p.sort_order,
	       (SELECT count(*) FROM order_items oi
	          JOIN orders o ON o.tenant_id = oi.tenant_id AND o.id = oi.order_id
	         WHERE oi.tenant_id = p.tenant_id AND oi.traffic_pack_id = p.id
	           AND o.status IN ('paid','fulfilled','partially_refunded','refunded')),
	       p.created_at, p.updated_at
	  FROM traffic_packs p`

func scanTrafficPack(row pgx.Row, out *TrafficPackRow) error {
	return row.Scan(&out.ID, &out.Name, &out.TrafficBytes, &out.Currency, &out.UnitAmount,
		&out.Recommended, &out.Status, &out.SortOrder, &out.SoldCount,
		&out.CreatedAt, &out.UpdatedAt)
}

func loadTrafficPack(ctx context.Context, tx pgx.Tx, tenantID, packID string, out *TrafficPackRow) error {
	return scanTrafficPack(tx.QueryRow(ctx, trafficPackSelectSQL+`
		 WHERE p.tenant_id = $1 AND p.id = $2::uuid`, tenantID, packID), out)
}

// ListTrafficPacks 列出流量包，status 为空时在售与已下架都列，在售的在前。
func (s *Service) ListTrafficPacks(ctx context.Context, tenantID, status string) ([]TrafficPackRow, error) {
	if status != "" && status != "active" && status != "archived" {
		return nil, httpx.Invalid(map[string]string{"status": "只能是 active 或 archived"})
	}
	out := []TrafficPackRow{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, trafficPackSelectSQL+`
			 WHERE p.tenant_id = $1 AND ($2 = '' OR p.status = $2)
			 ORDER BY p.status = 'archived', p.sort_order, p.created_at, p.id`, tenantID, status)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p TrafficPackRow
			if err := scanTrafficPack(rows, &p); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

func validateTrafficPackInput(in *TrafficPackInput) error {
	in.Name = strings.TrimSpace(in.Name)
	fields := map[string]string{}
	// 库里的 CHECK 按 length(btrim(name)) 数字符，这里同样按字数卡
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > 60 {
		fields["name"] = "名称 1 到 60 个字"
	}
	if in.TrafficBytes <= 0 || in.TrafficBytes > maxTrafficPackBytes {
		fields["traffic_bytes"] = "容量必须大于 0，且不超过 1 PiB"
	}
	if in.Currency != "CNY" && in.Currency != "USD" {
		fields["currency"] = "仅允许 CNY 或 USD"
	}
	if in.UnitAmount <= 0 || in.UnitAmount > maxTrafficPackAmount {
		fields["unit_amount"] = "价格必须大于 0，且不超过 100 万"
	}
	if in.SortOrder < -1_000_000 || in.SortOrder > 1_000_000 {
		fields["sort_order"] = "排序号超出范围"
	}
	if len(fields) > 0 {
		return httpx.Invalid(fields)
	}
	return nil
}

func trafficPackDigest(p TrafficPackRow) map[string]any {
	return map[string]any{
		"name": p.Name, "traffic_bytes": p.TrafficBytes, "currency": p.Currency,
		"unit_amount": p.UnitAmount, "recommended": p.Recommended,
		"status": p.Status, "sort_order": p.SortOrder,
	}
}

func (s *Service) writeTrafficPackAudit(ctx context.Context, tx pgx.Tx, tenantID, actorID,
	action string, packID string, before, after any) error {
	return audit.Write(ctx, tx, tenantID, audit.Entry{
		ActorKind: "admin", ActorID: &actorID, Action: action,
		ResourceType: "traffic_pack", ResourceID: &packID, APIDomain: "admin",
		Outcome: "success", RequestID: httpx.RequestIDFrom(ctx),
		BeforeDigest: before, AfterDigest: after,
	})
}

// lockTrafficPack 锁住一行并核对乐观锁；不存在回中性 404。
func lockTrafficPack(ctx context.Context, tx pgx.Tx, tenantID, packID string,
	expected time.Time, out *TrafficPackRow) error {
	// 先单独锁行再读：选择列里带着计数子查询，不和 FOR UPDATE 混在一句里
	var locked string
	err := tx.QueryRow(ctx, `SELECT id::text FROM traffic_packs
		 WHERE tenant_id = $1 AND id = $2::uuid FOR UPDATE`, tenantID, packID).Scan(&locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFoundOrForbidden()
	}
	if err != nil {
		return err
	}
	if err := loadTrafficPack(ctx, tx, tenantID, packID, out); err != nil {
		return err
	}
	if !out.UpdatedAt.Equal(expected) {
		return &httpx.Error{Code: httpx.CodeConflict,
			Message: "流量包已被其他管理员修改，请刷新后重试",
			Fields:  map[string]string{"updated_at": "current=" + out.UpdatedAt.Format(time.RFC3339Nano)}}
	}
	return nil
}

// CreateTrafficPack 新建一个在售的流量包。
func (s *Service) CreateTrafficPack(ctx context.Context, tenantID string, in TrafficPackInput) (*TrafficPackRow, error) {
	if err := validateTrafficPackInput(&in); err != nil {
		return nil, err
	}
	if err := s.requireP0BSales(); err != nil {
		return nil, err
	}
	var out TrafficPackRow
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO traffic_packs
				(tenant_id, name, traffic_bytes, currency, unit_amount, recommended, sort_order)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id::text`,
			tenantID, in.Name, in.TrafficBytes, in.Currency, in.UnitAmount,
			in.Recommended, in.SortOrder).Scan(&id); err != nil {
			return err
		}
		if err := loadTrafficPack(ctx, tx, tenantID, id, &out); err != nil {
			return err
		}
		return s.writeTrafficPackAudit(ctx, tx, tenantID, in.ActorID,
			"traffic_pack.create", id, nil, trafficPackDigest(out))
	})
	if err != nil {
		return nil, catalogResult(err)
	}
	return &out, nil
}

// UpdateTrafficPack 改名称、容量、价格、推荐与排序；上下架走 SetTrafficPackStatus。
func (s *Service) UpdateTrafficPack(ctx context.Context, tenantID, packID string, in TrafficPackInput) (*TrafficPackRow, error) {
	if _, err := uuid.Parse(packID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	if in.ExpectedUpdatedAt == nil {
		return nil, httpx.Invalid(map[string]string{"expected_updated_at": "必填：列表里读到的 updated_at"})
	}
	if err := validateTrafficPackInput(&in); err != nil {
		return nil, err
	}
	if err := s.requireP0BSales(); err != nil {
		return nil, err
	}
	var out TrafficPackRow
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		var before TrafficPackRow
		if err := lockTrafficPack(ctx, tx, tenantID, packID, *in.ExpectedUpdatedAt, &before); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE traffic_packs
			   SET name = $3, traffic_bytes = $4, currency = $5, unit_amount = $6,
			       recommended = $7, sort_order = $8
			 WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, packID, in.Name, in.TrafficBytes, in.Currency, in.UnitAmount,
			in.Recommended, in.SortOrder); err != nil {
			return err
		}
		if err := loadTrafficPack(ctx, tx, tenantID, packID, &out); err != nil {
			return err
		}
		return s.writeTrafficPackAudit(ctx, tx, tenantID, in.ActorID,
			"traffic_pack.update", packID, trafficPackDigest(before), trafficPackDigest(out))
	})
	if err != nil {
		return nil, catalogResult(err)
	}
	return &out, nil
}

// SetTrafficPackStatus 上架（active）或下架（archived）。下架只是不再出现在
// 门户目录、不能再下单；已买到的余额不受影响，待支付的单照常可以付。
func (s *Service) SetTrafficPackStatus(ctx context.Context, tenantID, packID, actorID, status string,
	expected *time.Time) (*TrafficPackRow, error) {
	if _, err := uuid.Parse(packID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	fields := map[string]string{}
	if status != "active" && status != "archived" {
		fields["status"] = "只能是 active 或 archived"
	}
	if expected == nil {
		fields["expected_updated_at"] = "必填：列表里读到的 updated_at"
	}
	if len(fields) > 0 {
		return nil, httpx.Invalid(fields)
	}
	if status == "active" {
		if err := s.requireP0BSales(); err != nil {
			return nil, err
		}
	}
	var out TrafficPackRow
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var before TrafficPackRow
		if err := lockTrafficPack(ctx, tx, tenantID, packID, *expected, &before); err != nil {
			return err
		}
		if before.Status == status {
			if status == "active" {
				return httpx.New(httpx.CodeConflict, "流量包已经在售")
			}
			return httpx.New(httpx.CodeConflict, "流量包已经下架")
		}
		if _, err := tx.Exec(ctx, `UPDATE traffic_packs SET status = $3
			 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, packID, status); err != nil {
			return err
		}
		if err := loadTrafficPack(ctx, tx, tenantID, packID, &out); err != nil {
			return err
		}
		action := "traffic_pack.archive"
		if status == "active" {
			action = "traffic_pack.restore"
		}
		return s.writeTrafficPackAudit(ctx, tx, tenantID, actorID, action, packID,
			map[string]any{"status": before.Status}, map[string]any{"status": out.Status})
	})
	if err != nil {
		return nil, catalogResult(err)
	}
	return &out, nil
}

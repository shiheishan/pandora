// [INPUT]: 依赖 subscriptions / users / tenants 的归属与时区，依赖 uniproxy.go 的 chargeTraffic；写 subscription_usage_daily（迁移 00072）；time/tzdata 内嵌时区库
// [OUTPUT]: 对外提供 UsageLocation、UsageDay（按日流量的日界口径，subscription 的读接口共用）；包内提供 chargeReportEntry
// [POS]: domain/nodefabric 流量上报的单用户记账：扣配额与流量包（chargeTraffic）并在同一事务里累加当日用量，被 ReportTraffic 逐条调用
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"context"
	"errors"
	"sync"
	"time"
	// 日界必须与主机装没装 tzdata 无关：发布的是 CGO_ENABLED=0 的静态二进制，
	// 精简系统上 LoadLocation 会全部失败，所有人的「今天」就悄悄变成了 UTC。
	_ "time/tzdata"

	"github.com/jackc/pgx/v5"
)

//------------------------------------------------------------------------------
// 日界口径
//------------------------------------------------------------------------------

// zoneCache 只缓存加载成功的时区：名字来自 IANA 库，集合有界；失败的名字
// 不缓存，免得把库里的脏值攒成一张无界的表。
var zoneCache sync.Map // name → *time.Location

func loadZone(name string) (*time.Location, bool) {
	// 空串与 "Local" 在 Go 里分别是 UTC 与「服务器本地时区」，都不是用户选的时区。
	if name == "" || name == "Local" {
		return nil, false
	}
	if loc, ok := zoneCache.Load(name); ok {
		return loc.(*time.Location), true
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, false
	}
	zoneCache.Store(name, loc)
	return loc, true
}

// UsageLocation 决定按日流量按哪个时区切日：用户 timezone，无效退回租户
// timezone，再无效退回 UTC。写入（上报）与读取（门户柱状图）必须用同一个
// 函数，否则同一笔流量会落在两边不同的「那一天」。
func UsageLocation(userTZ, tenantTZ string) *time.Location {
	if loc, ok := loadZone(userTZ); ok {
		return loc
	}
	if loc, ok := loadZone(tenantTZ); ok {
		return loc
	}
	return time.UTC
}

// UsageDay 返回时刻 t 在 loc 里的自然日，表示为该日 00:00 UTC——pgx 按
// date 编码时只取年月日，这样不会因为 t 自身带的时区再偏一天。
func UsageDay(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

//------------------------------------------------------------------------------
// 单用户记账
//------------------------------------------------------------------------------

// chargeReportEntry 把一个 uid 的一笔计费用量记账：扣套餐额度与流量包，
// 再把同一笔量累加进这条订阅当天的用量行。两件事在调用方的同一个事务里，
// 柱状图与配额读数不会互相漂移。uid 找不到订阅（已删除）时返回 false。
//
// now 由调用方对整份上报取一次：同一份报文里的所有用户按同一时刻切日。
func chargeReportEntry(ctx context.Context, tx pgx.Tx, tenantID string, uid, billed int64, now time.Time) (bool, error) {
	var subID, userID, userTZ, tenantTZ string
	if err := tx.QueryRow(ctx, `
		SELECT s.id::text, s.user_id::text, u.timezone, t.timezone
		  FROM subscriptions s
		  JOIN users u   ON u.tenant_id = s.tenant_id AND u.id = s.user_id
		  JOIN tenants t ON t.id = s.tenant_id
		 WHERE s.tenant_id = $1 AND s.node_uid = $2`,
		tenantID, uid).Scan(&subID, &userID, &userTZ, &tenantTZ); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	if err := chargeTraffic(ctx, tx, tenantID, subID, userID, billed); err != nil {
		return false, err
	}
	if billed > 0 {
		day := UsageDay(now, UsageLocation(userTZ, tenantTZ))
		if _, err := tx.Exec(ctx, `
			INSERT INTO subscription_usage_daily (tenant_id, subscription_id, day, bytes)
			VALUES ($1, $2::uuid, $3::date, $4)
			ON CONFLICT (tenant_id, subscription_id, day) DO UPDATE
			   SET bytes = subscription_usage_daily.bytes + EXCLUDED.bytes,
			       updated_at = now()`,
			tenantID, subID, day, billed); err != nil {
			return false, err
		}
	}
	return true, nil
}

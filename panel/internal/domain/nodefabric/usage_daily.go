package nodefabric

import (
	"context"
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
// （站点）timezone，再无效退回 UTC。写入（上报）与读取（门户柱状图）必须用
// 同一个函数，否则同一笔流量会落在两边不同的「那一天」。
//
// 用户 timezone 为 'UTC' 视同未设（R50）：users.timezone 非空、默认 'UTC'，
// 而且没有任何入口能改它，存量的 'UTC' 都是默认值。真想按 UTC 切日的站点把
// 站点时区设成 UTC 即可。
func UsageLocation(userTZ, tenantTZ string) *time.Location {
	if userTZ != "UTC" {
		if loc, ok := loadZone(userTZ); ok {
			return loc
		}
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
// 整份上报记账
//------------------------------------------------------------------------------

// billedEntry 是上报里一个 uid 的一笔计费用量（已按节点倍率折算）。
type billedEntry struct {
	uid    int64
	billed int64
}

// chargeReportEntries 把一份上报里各 uid 的计费用量记账：扣套餐额度与流量包
// （applyTrafficCharges），再把同一笔量累加进各订阅当天的用量行。两件事在调用方的
// 同一个事务里，柱状图与配额读数不会互相漂移。返回找到订阅的 uid 数（已删除的
// 订阅忽略）。
//
// entries 按 uid 升序传入（同一 uid 已合并），记账按这个顺序决定流量包怎么扣。
// now 由调用方对整份上报取一次：同一份报文里的所有用户按同一时刻切日。
//
// 原来每个 uid 四条语句（查订阅、锁配额、扣配额、写当日用量），第一条配额行锁一直
// 持有到整份上报处理完；现在与条数无关：一次查订阅，记账见 applyTrafficCharges，
// 当日用量一条 upsert（按订阅排序写入，并发上报同序加锁）。
func chargeReportEntries(ctx context.Context, tx pgx.Tx, tenantID string, entries []billedEntry, now time.Time) (int, error) {
	if len(entries) == 0 {
		return 0, nil
	}
	uids := make([]int64, len(entries))
	for i, e := range entries {
		uids[i] = e.uid
	}
	type owner struct {
		subID, userID, userTZ, tenantTZ string
	}
	owners := map[int64]owner{}
	rows, err := tx.Query(ctx, `
		SELECT s.node_uid, s.id::text, s.user_id::text, u.timezone, t.timezone
		  FROM subscriptions s
		  JOIN users u   ON u.tenant_id = s.tenant_id AND u.id = s.user_id
		  JOIN tenants t ON t.id = s.tenant_id
		 WHERE s.tenant_id = $1 AND s.node_uid = ANY($2::bigint[])`,
		tenantID, uids)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var uid int64
		var o owner
		if err := rows.Scan(&uid, &o.subID, &o.userID, &o.userTZ, &o.tenantTZ); err != nil {
			rows.Close()
			return 0, err
		}
		owners[uid] = o
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	accepted := 0
	charges := make([]trafficCharge, 0, len(entries))
	type usageKey struct {
		subID string
		day   string
	}
	usage := map[usageKey]int64{}
	var usageKeys []usageKey
	for _, e := range entries {
		o, ok := owners[e.uid]
		if !ok {
			continue
		}
		accepted++
		charges = append(charges, trafficCharge{subID: o.subID, userID: o.userID, billed: e.billed})
		if e.billed > 0 {
			k := usageKey{o.subID, UsageDay(now, UsageLocation(o.userTZ, o.tenantTZ)).Format(time.DateOnly)}
			if _, ok := usage[k]; !ok {
				usageKeys = append(usageKeys, k)
			}
			usage[k] += e.billed
		}
	}
	if err := applyTrafficCharges(ctx, tx, tenantID, charges); err != nil {
		return 0, err
	}
	if len(usageKeys) == 0 {
		return accepted, nil
	}
	subs := make([]string, len(usageKeys))
	days := make([]string, len(usageKeys))
	bytes := make([]int64, len(usageKeys))
	for i, k := range usageKeys {
		subs[i], days[i], bytes[i] = k.subID, k.day, usage[k]
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO subscription_usage_daily (tenant_id, subscription_id, day, bytes)
		SELECT $1, v.subscription_id, v.day::date, v.bytes
		  FROM unnest($2::uuid[], $3::text[], $4::bigint[]) AS v(subscription_id, day, bytes)
		 ORDER BY v.subscription_id, v.day
		ON CONFLICT (tenant_id, subscription_id, day) DO UPDATE
		   SET bytes = CASE WHEN subscription_usage_daily.bytes > 9223372036854775807 - EXCLUDED.bytes
		                    THEN 9223372036854775807
		                    ELSE subscription_usage_daily.bytes + EXCLUDED.bytes END,
		       updated_at = now()`,
		tenantID, subs, days, bytes); err != nil {
		return 0, err
	}
	return accepted, nil
}

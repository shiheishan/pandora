// Package integrity 是数据巡检的公共返回类型：各属主模块的只读检查方法都回 Result，
// report 的定时任务（report:integrity）把它存进 integrity_checks、按变化告警。
//
// 这里只放类型；检查的 SQL 在各属主模块的 repo，口径写在各自的 service 注释里。
package integrity

import "github.com/google/uuid"

// SampleLimit 是每项检查最多带回的样例 id 个数。
const SampleLimit = 5

// Result 是一项检查的结论：违例行数与最多 SampleLimit 个样例 id（新的在前）。
type Result struct {
	Count  int64
	Sample []string
}

// Row 是检查 SQL 的一行：样例 id 与随行带回的违例总数（count(*) OVER ()，在 LIMIT 之前算）。
// 各 repo 的检查 SQL 统一写成「SELECT x.id::text AS id, count(*) OVER () AS n … ORDER BY … LIMIT SampleLimit」。
type Row struct {
	ID string `gorm:"column:id"`
	N  int64  `gorm:"column:n"`
}

// FromRows 把检查 SQL 的行转成 Result：没有行即 0 违例；Sample 不为 nil。
func FromRows(rows []Row) Result {
	out := Result{Sample: make([]string, 0, len(rows))}
	for _, r := range rows {
		out.Count = r.N
		out.Sample = append(out.Sample, r.ID)
	}
	return out
}

// OrderRef 是一条挂在订单上的记录（收款、佣金条目）与它的订单：跨模块的检查由属主各给出这一半，
// report 再经 order 核对订单状态（不在一个模块的 SQL 里连别人的表）。
type OrderRef struct {
	ID      uuid.UUID `gorm:"column:id"`
	OrderID uuid.UUID `gorm:"column:order_id"`
}

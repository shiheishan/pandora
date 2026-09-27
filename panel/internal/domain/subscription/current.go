// [INPUT]: 无（纯 SQL 片段，调用方拼进自己的查询；依赖 subscriptions / plans 两张表的列名）
// [OUTPUT]: 对外提供 LiveStatusesSQL、CurrentOrderSQL、CurrentSQL、ActivePlanNameSQL、HasLiveSQL
// [POS]: domain/subscription 的「当前订阅」口径（契约后台-03 订阅态口径 R118）唯一真相源：adminops（用户列表、批量运营、风控聚类）与 support（工单队列与详情）都从这里取，两者互不依赖
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package subscription

// 后台所有「用户现在是什么订阅」的问题共用一个口径：
//
//   - 在用：status 为 active / trialing / grace / past_due。宽限期与欠费中的用户
//     仍然算有订阅——他们的节点还在、续费后原地恢复，后台把他们当成「无订阅」
//     会让客服和运营看错人。
//   - 当前订阅：在用的优先，其次到期最晚，再次最近创建，取一条。用户没有在用
//     订阅时它可以是一条已结束的订阅（列表要显示「上一份到哪天」）。
//   - 在用套餐名（active_plan / user_active_plan）：当前订阅在用时它的套餐名，
//     否则 NULL。它从当前订阅派生，所以同一行不会出现两者指向两条订阅。
//
// 这里只给 SQL 片段而不给查询：各调用方的外层表别名、租户列不同，片段按参数拼。
// 表达式参数由调用方写死的列引用（如 u.tenant_id），从不接用户输入；子查询内部占用
// 别名 s 与 pl，所以参数不能引用外层叫这两个名字的表。
//
// 不在此口径内的：订阅下发与节点用户列表的资格（active / trialing / grace，
// 见 listEligibleNodesTx）、到期与流量预警、仪表盘按单一状态分别计数。

// LiveStatusesSQL 是「在用」的状态集合，写成可直接跟在 IN 后面的形状。
const LiveStatusesSQL = `('active','trialing','grace','past_due')`

// CurrentOrderSQL 是挑当前订阅的排序，别名 s 指 subscriptions。
const CurrentOrderSQL = `(s.status IN ` + LiveStatusesSQL + `) DESC,
	                   s.current_period_end DESC NULLS LAST, s.created_at DESC`

// currentFromSQL 是当前订阅子查询的 FROM … LIMIT 1 部分；plans 用 LEFT JOIN，
// 取套餐名时不改变挑中的那一条。
func currentFromSQL(tenantExpr, userExpr string) string {
	return ` FROM subscriptions s
	          LEFT JOIN plans pl ON pl.id = s.plan_id
	         WHERE s.tenant_id = ` + tenantExpr + ` AND s.user_id = ` + userExpr + `
	         ORDER BY ` + CurrentOrderSQL + `
	         LIMIT 1`
}

// CurrentSQL 是当前订阅某一列（subscriptions 的列名）的标量子查询；没有订阅为 NULL。
func CurrentSQL(tenantExpr, userExpr, col string) string {
	return `(SELECT s.` + col + currentFromSQL(tenantExpr, userExpr) + `)`
}

// ActivePlanNameSQL 是在用套餐名的标量子查询：当前订阅在用时它的套餐名，否则 NULL。
func ActivePlanNameSQL(tenantExpr, userExpr string) string {
	return `(SELECT CASE WHEN s.status IN ` + LiveStatusesSQL + ` THEN pl.name END` +
		currentFromSQL(tenantExpr, userExpr) + `)`
}

// HasLiveSQL 是「存在在用订阅」的布尔条件（sub_state=active、has_active_sub=true）。
func HasLiveSQL(tenantExpr, userExpr string) string {
	return `EXISTS (SELECT 1 FROM subscriptions s
	           WHERE s.tenant_id = ` + tenantExpr + ` AND s.user_id = ` + userExpr + `
	             AND s.status IN ` + LiveStatusesSQL + `)`
}

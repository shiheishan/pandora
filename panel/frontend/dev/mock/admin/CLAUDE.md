# panel/frontend/dev/mock/admin/
> L2 | 父级: /panel/frontend/dev/mock/CLAUDE.md

后台十个模块的假接口，一个模块一个文件、导出一个 MockModule，归属与 src/admin/screens 相同（后台前端一 dash / tickets / users / plans / billing，后台前端二 marketing / nodes / content / system / security）。index.ts 的登记顺序即询问顺序，不再改动。模块状态放在文件内的模块级变量里，vite 重启即复原。

成员清单
index.ts: 登记表 ADMIN_MODULES
users.ts: 用户（后台-03）；已有 POST v1/users/{id}/balance（billing.provider.write + reauth + 幂等 admin_user_balance_adjust，响应 { balance }），第 2 阶段起用来在浏览器里验证 reauth 重放
dash.ts: 仪表盘（后台-01）八个只读接口，照契约按权限回 404、tasks 条目按各自读权限过滤（withdrawals_pending 挂 marketing.commission.read）、校验 currency / days / range / limit / snapshot_at 并回契约里的 422；数据按日期确定性生成，概览今日 / 昨日与收入趋势末两天同源；含全部待补·后端字段
tickets.ts / plans.ts / billing.ts / marketing.ts / nodes.ts / content.ts / system.ts / security.ts: 其余八个模块，目前为空壳

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

# panel/frontend/dev/mock/admin/
> L2 | 父级: /panel/frontend/dev/mock/CLAUDE.md

后台十个模块的假接口，一个模块一个文件、导出一个 MockModule，归属与 src/admin/screens 相同（后台前端一 dash / tickets / users / plans / billing，后台前端二 marketing / nodes / content / system / security）。index.ts 的登记顺序即询问顺序，不再改动。模块状态放在文件内的模块级变量里，vite 重启即复原。

成员清单
index.ts: 登记表 ADMIN_MODULES
users.ts: 用户（后台-03）；已有 POST v1/users/{id}/balance（billing.provider.write + reauth + 幂等 admin_user_balance_adjust，响应 { balance }），第 2 阶段起用来在浏览器里验证 reauth 重放
dash.ts / tickets.ts / plans.ts / billing.ts / marketing.ts / nodes.ts / content.ts / system.ts / security.ts: 其余九个模块，目前为空壳

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

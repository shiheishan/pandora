# panel/frontend/src/admin/screens/security/
> L2 | 父级: /panel/frontend/src/admin/screens/CLAUDE.md

安全与运维（后台-09 后半，归后台前端二）。四个标签全部接入：审计日志、访问日志、风控、降级开关。视觉按 管理后台-09-通知与安全.dc.html，数据与规则按 api-contract.md 后台-09 · 安全与运维（含 R23 R40 R41 R44 R58），并对过 Go 的 audit_log.go + adminops/audit.go、access_log.go、risk.go + adminops/risk.go、handlers.go setSwitch + adminops.SetSwitch；契约里的待补·后端（审计 q / 来源 IP / 对象名 / auth_context 与导出、访问日志分类与 outcome 筛选、聚类字段与两种处置、开关 reauth 与四个新开关）主线都已实现，照实现写。设计缺、契约标「待补·前端」的都补上了：审计的动作前缀 / 操作者 / 结果筛选与分页、访问日志的 IP 与账号筛选和「登录 / 注册 / 订阅拉取」分段、停用确认框的原因、开关确认框的原因。
分层：schemas（zod）→ queries（读 hook，写后按 SK 前缀失效；转出 admin/actions.ts 的 useCan / useFailure / useIntentKey）→ logic（纯函数，security.test.ts 守住）→ 组件。
四条后端事实决定了写法：访问日志是 audit_events 与订阅拉取日志的归并，不是 HTTP 访问日志，没有方法、路径、状态码与耗时，设计稿的列按契约换义；这几张表都不在 SSE 监听里，「实时尾随」是第一页 5 秒轮询；开关极性是 enabled = 功能可用，设计稿的「开启『暂停…』」对应 enabled=false，关闭时没给原因是数据库 CHECK，回 409 而不是 422，前端先拦；停用走 suspended（可恢复）且后台账号由后端跳过，所以结果按 disabled / skipped 汇总，一个都没停成时后端不写结论。
权限：四个标签的读都挂 security.audit.read；审计导出另要 ops.export + reauth；标记正常 security.risk.review（无 reauth 无幂等），批量停用另要 iam.user.write + reauth + 幂等 ip_cluster_disable；开关切换 platform.settings.write + reauth（无幂等）。只读演示账号没有 security.audit.read，整个模块不可见。
待决：D-A-3 ——「订阅下发使用缓存」后端做不到，不显示；ops.bulk_export / ops.reports / node.autoscale 没有代码读取，灰显「未接入」不可切；admin.writes 的豁免清单按后端现状写在说明里。switches.changed 广播还没登记进 core/query 的 REALTIME_TOPICS，报告协调会话。

成员清单
index.tsx: 页面入口，按标签分发：audit → AuditTab、access → AccessTab、risk → RiskTab、switches → SwitchesTab
schemas.ts: 审计行（actor_kind 含迁移 00012 的 node，可空字段全是 null）与列表、访问日志行（omitempty 即缺席，outcome 混有订阅拉取的原始 result 所以不收成枚举）、聚类（成员、复核结论、风险）与标记正常 / 批量停用响应、开关行与切换响应的 zod schema 与枚举
queries.ts: 查询键前缀 SK、useAudit（翻页保留上一页）、useAccessLog（实时尾随 = 第一页 5 秒轮询）、useClusters（include_reviewed）、useSwitches、useInvalidateSecurity，转出三件通用 hook
logic.ts: 审计查询串 / 导出查询串与日期校验（与 auditExportRange 同键同文案）/ 操作人 · 对象 · 认证 · 结果文字、访问日志分段 → category 或 outcome=error、结果文字（订阅拉取的 result 也翻成中文）与行提示、聚类的归属地文字 / 复核状态（正常 30 天、过期、已停用）/ 可停用成员 / 停用校验与结果摘要 / 写后缓存补丁、降级开关字典（中文名、说明、连带影响、是否接线、缺行时的效果）与行视图、切换请求与原因校验
AuditTab.tsx: 审计日志：搜索框（300ms 防抖）+ 动作前缀 / 操作者 / 结果筛选 + 导出，六列表格（非成功结果标签、对象的原因提示、存量行认证与 IP 显示「—」）与分页；导出弹窗选可选起止日期，经 requestRaw 取 CSV 存文件
AccessTab.tsx: 访问日志：IP 与账号两个筛选框，深色终端（实时尾随指示与暂停、六段分段、时间 / 分类 / 动作 / 结果 / IP · 归属地五列、账号与客户端放行提示），接口没有 total，按「这页满没满」给「更早」与「回到最新」
RiskTab.tsx: 风控：说明 + 「显示已标记正常的」，聚类卡片（IP 或「IP 无法解密」、归属地 · 网络类型 · 最近时间、风险徽标、账号数与事件数、成员可跳用户详情并标出非正常状态、结果行），标记为正常直接执行，禁用走 DisableModal（默认全选可停用成员、原因必填、幂等键一次意图一把），成功后就地补缓存、失效审计与用户模块
SwitchesTab.tsx: 降级开关：黄底提示、只读模式开启时的红色提示、开关行（中文名 + code、说明、连带影响、当前原因、状态字、受控开关；核心项锁定、未接入灰显、缺行不可切），ToggleModal 进入降级时原因必填、恢复时可选
security.module.css: 工具条（换行时导出仍贴右）与表格面板（审计表在容器里改固定布局、最小 840，1280 铺满、960 面板内横滚，不挤没列）、弹窗表单、深色终端（容器内重映射令牌让 ui 分段 / 骨架 / 空状态可用）、聚类卡片网格（auto-fill 340）、开关行（降级中浅红底且开关转危险色、未接入灰显）
security.test.ts: logic 与 schema 边界的单元测试

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

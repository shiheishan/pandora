---
paths:
  - "panel/frontend/src/admin/screens/security/**"
---

# 后台 · 安全与运维

- 「访问日志」是 audit_events 与订阅拉取日志的归并（安全事件流），不是 HTTP 访问日志：没有方法、路径、状态码与耗时，设计稿的列按契约换义；没有 total，只能按「这页满没满」翻更早。
- 这几张表都不在 SSE 监听里，「实时监控」是第一页 5 秒轮询（界面与代码注释都叫实时监控，不叫尾随）；只有降级开关按 `meta.topics` 接 `switches.changed`。
- 降级开关极性：`enabled` = 功能可用，设计稿的「开启『暂停…』」对应 `enabled=false`。进入降级时原因必填（数据库 CHECK，后端回 409 而不是 422），前端先拦；恢复时可选。
- 批量停用走 `suspended`（可恢复），已停用 / 已封禁 / 后台账号由后端跳过：结果按 `disabled` / `skipped` 汇总，一个都没停成时后端不写结论、前端缓存补丁也不写。
- 产品取舍：「订阅下发使用缓存」开关不做；字典不收 `ops.bulk_export` / `ops.reports` / `node.autoscale`（旧库残留的行按未知 code 显示、照后端极性可切）。
- 审计导出要 reauth，导出日期校验与后端 `auditExportRange` 同键同文案。
- 10-16 设计稿：审计日志「结果」单独一列（每行都有标签）；访问日志行里直接显示账号（`accessAccount`：本人邮箱 > 短用户 id；匿名的登录失败显示「匿名 · 试登 <被试邮箱>」，取后端 `target_user_id/email`；有 id 的账号是到用户详情的链接），后接「· 动作中文名」与代号小字，工具栏右侧显示本页条数与「清空筛选」。
- 审计与访问日志的筛选、翻页都在地址上（`auditFromQuery` / `auditToQuery`：`q`、`action`、`actor`、`outcome`、`o`；`accessFromQuery` / `accessToQuery`：`view`、`ip`、`user`、`o`；`navigate` 用 replace），搜索框与动作前缀都是 `DebouncedInput`（停手 300ms 回写，地址变了跟着地址走；受地址直接控制的输入框会在 hashchange 前被还原，光标跳、输入法组字被打断）。用户详情「看登录记录 →」（`#/security/access?view=login&user=<id>`）、「看审计记录 →」（`#/security/audit?q=<用户 id>`）与风控成员行的「登录记录」靠这个深链；`paused` 仍是组件状态。
- 动作显示中文名：`logic.ts` 的 `ACTION_NAMES` 覆盖 Go 写进审计的全部代号（10-21 核过 181 个，同日随接口删掉 5 个复制节点、重下发、改组成员、订阅模板的动作，剩 176 个）外加访问日志的 `subscription.fetch`，`actionLabel` 查不到回退代号；Go 新增、改名、删除审计动作时同改这张表（单测查重复、代号格式、按代号排序；前后端对照守卫是 Go 侧 `panel/internal/module/audit/service/actions_frontend_test.go` 的 `TestAuditActionsMatchFrontend`，两边差一个都红，访问日志伪动作登记在它的 `frontendOnlyActions`）。对象类型中文名 `RESOURCE_TYPE_LABEL`（后端给了 `resource_label` 就显示它，否则 `objectLabel` 写「类型 · id 前 8 位」；Go 给用户、工单、套餐、套餐版本解析可读名，假后端 `labelOf` 同口径）；登录失败原因码 `reasonLabel`，在对象下面直接显示，不藏在悬停里。
- 审计搜索框占位 `AUDIT_SEARCH_PLACEHOLDER`（邮箱、动作代号或对象 ID）与 Go 搜索口径一致：邮箱命中操作人或「对象是这个用户」，名称搜不到，空结果的说明里写明。操作者类型只有库里的四种（`ACTOR_KINDS`：user / admin / system / anonymous），订阅拉取结果只有迁移 00017 的五种（`FETCH_RESULT`）。
- 访问日志行本身不可点，不加悬停底色（只有账号链接可点）。术语就地解释：表头与风险标签用 `TERM_HINT` 作 `title`，表头文字套 `.term`（点线下划线）；页面说明里同名 `{ term, text }` 条目口径一致。
- 风控是三段筛选（待处理 / 已标记正常 / 全部，带计数）：待处理取默认列表，另两段取 `include_reviewed=1` 的列表再按有效的「标记正常」过滤（`clusterViews`），两个查询同时发；「标记正常」先确认再提交；成员不显示「最近出现」（后端没有）；成员行末尾「登录记录」链到访问日志的登录分段。风控列表下方挂用户块的 `DevicePolicy`，「注册防护」页签挂 `SignupSecurity`（组件归 users/，这里只接线）。
- 降级开关页没有顶部说明与只读横幅（说明在 `PAGE_HELP`）。没有关不掉的开关：00002 种的三个核心项（auth.login / subscription.renewal / client.config_sync）没有代码读取，迁移 00064 删去并取消 essential（10-21），`GET v1/switches` 每项只有 code、enabled、reason，按 code 升序。
- av4（样稿 admin-v4）：
  - 访问日志是亮底白框（不再是深色终端）：头部两行——第一行 `StatusDot`（监控中 / 已暂停 / 查看更早的记录）+ 说明 + 第一页的「暂停 / 继续」，第二行 `FilterTabs` 分段；事件行是 5 列 grid（时间、分类标签、结果、IP、账号 · 动作）；页脚常显，「回到最新」第一页置灰。结果色档在 `logic.ts` 的 `accessTone`：成功绿、订阅拉取 `rate_limited` 黄、其余非成功红、无结果灰。
  - 风控每个聚类一张卡：一行头部（IP、归属地、风险标签、账号数、「标记正常」「禁用账号…」）+ 成员行（邮箱链接、状态、套餐），复核结论（如「已标记为正常，MM-DD 前不再提示」）在卡片最下一行；有效标记正常的卡整张淡显。
  - 审计日志：「导出」在页头 `PageActions`；表格 `Table boxed`，表尾一页时「共 N 条」、多页 `<Pager inline>`；手机上只留操作人与结果（其余列 `hideOnMobile`），动作与时间挪到操作人下一行（全局类 `m-only`）。结果色档 `OUTCOME_TONE`：成功绿、失败红、拒绝黄、部分成功蓝。
  - 降级开关：`Card flush` 里一项一行，暂停中整行 `--danger-soft` 底、开关 `Switch tone="danger"`（红）；缺行的开关不能切换。
  - 注册防护（`users/SignupForm.tsx` 的 security 段）：「人机验证」「注册限制」两节 `SectionTitle variant="group"`，每项一个 kit `SettingRow`（打开才展开配置）；「开启人机验证」= `captcha_provider !== ''`，关掉时前端记住上次选的服务，第一次打开默认 Turnstile；验证服务用 `RadioCards`；邮箱后缀是 `TagInput`（上限 100，表单字段是数组）；整页一个 `SaveBar`（提示「保存时要再验证一次身份」），只提交改过的字段。
- 溯源总览（`TraceTab.tsx`，数据层 `trace.ts`，10-20）：
  - 时间窗 24h / 7d / 30d（`Segmented`，24h 不带 `window` 参数）；选 30d 时提示拉取日志只保留 31 天。概览与 IP 排行是同一个请求（`/v1/trace/ips`），订阅、账号两张排行只在切到时才发请求；都不在 SSE 里、不轮询，换窗口保留上一份数据（`placeholderData`）；整窗聚合较重，`staleTime` 5 分钟、关掉 `refetchOnWindowFocus`，工具条上手动「刷新」（`useRefreshTrace` 作废 `TRACE_KEY` 整组）。「失败」= 拉取结果不是 ok，含到期提示与被限流（页面说明里写明）。
  - IP 默认打码（后端给 `ip_masked` 与 `token`），点「显示」调 `/v1/trace/reveal`，每次都写审计，所以结果不进 react-query 缓存，只放组件状态；关联账号在单元格里展开，链接 `/users/list/<id>`，已注销显示 `traceUserLabel`（与 `userLabel` 同口径）。
  - 排序都是降序、由后端排（`SUB_SORTS` / `ACC_SORTS`，列 key 与 sort 值一致）；订阅排行的「地区数」只在本页内排（`sortByRegions`）。
  - schema 与 `identity/handler/trace.go` 的 DTO 逐键一致、全部必有；假后端在 `dev/mock/admin/security-trace.ts`，由审计行与拉取日志现算（与访问日志、审计页对得上，口径与不同处见 frontend-mock），校验文案与 Go 相同。

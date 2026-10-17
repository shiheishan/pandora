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
- 10-16 设计稿：审计日志「结果」单独一列（每行都有标签）；访问日志行里直接显示账号（`accessDetail`：邮箱 > 短用户 id > 匿名，再接动作），工具栏右侧显示本页条数与「清空筛选」。
- 风控是三段筛选（待处理 / 已标记正常 / 全部，带计数）：待处理取默认列表，另两段取 `include_reviewed=1` 的列表再按有效的「标记正常」过滤（`clusterViews`），两个查询同时发；「标记正常」先确认再提交；成员不显示「最近出现」（后端没有）。风控列表下方挂用户块的 `DevicePolicy`，「注册防护」页签挂 `SignupSecurity`（组件归 users/，这里只接线）。
- 降级开关页没有顶部说明与只读横幅（说明在 `PAGE_HELP`）；核心项画成禁用的打开开关。
- av4（样稿 admin-v4）：
  - 访问日志是亮底白框（不再是深色终端）：头部两行——第一行 `StatusDot`（监控中 / 已暂停 / 查看更早的记录）+ 说明 + 第一页的「暂停 / 继续」，第二行 `FilterTabs` 分段；事件行是 5 列 grid（时间、分类标签、结果、IP、账号 · 动作）；页脚常显，「回到最新」第一页置灰。结果色档在 `logic.ts` 的 `accessTone`：成功绿、订阅拉取 `rate_limited` 黄、其余非成功红、无结果灰。
  - 风控每个聚类一张卡：一行头部（IP、归属地、风险标签、账号数、「标记正常」「禁用账号…」）+ 成员行（邮箱链接、状态、套餐），复核结论（如「已标记为正常，MM-DD 前不再提示」）在卡片最下一行；有效标记正常的卡整张淡显。
  - 审计日志：「导出」在页头 `PageActions`；表格 `Table boxed`，表尾一页时「共 N 条」、多页 `<Pager inline>`；手机上只留操作人与结果（其余列 `hideOnMobile`），动作与时间挪到操作人下一行（`.mOnly`）。结果色档 `OUTCOME_TONE`：成功绿、失败红、拒绝黄、部分成功蓝。
  - 降级开关：`Card flush` 里一项一行，暂停中整行 `--danger-soft` 底、开关 `Switch tone="danger"`（红），核心项是禁用的打开开关。
  - 注册防护（`users/SignupForm.tsx` 的 security 段）：「人机验证」「注册限制」两节 `SectionTitle variant="group"`，每项一个 kit `SettingRow`（打开才展开配置）；「开启人机验证」= `captcha_provider !== ''`，关掉时前端记住上次选的服务，第一次打开默认 Turnstile；验证服务用 `RadioCards`；邮箱后缀是 `TagInput`（上限 100，表单字段是数组）；整页一个 `SaveBar`（提示「保存时要再验证一次身份」），只提交改过的字段。

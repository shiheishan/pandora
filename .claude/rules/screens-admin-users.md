---
paths:
  - "panel/frontend/src/admin/screens/users/**"
---

# 后台 · 用户

- 页签只有三个：用户列表、用户组、批量运营（10-16 设计稿）。注册设置与设备策略是本目录导出的组件，由别的模块接：`SignupGrowth`（营销）、`SignupSecurity`（安全）、`DevicePolicy`（安全），组件头注释写着用途与 props；用户模块的 `index.tsx` 不再接它们。会员折扣、流量重置统计页与重置历史、用户级「加时长」「加流量包」已删（10-16）。
- 保留规则 2 贯穿整个模块：后台看不到订阅地址。列表、详情、画像都不含令牌；「订阅与设备」页签用一句说明代替设计稿的「订阅地址 + 复制」；换发订阅链接的响应 schema 用 `.strict()` 守住「不回令牌」（`model.test.ts` 断言带令牌即判不符），成功后提示让用户到用户端重新复制。换发会连节点密码一起换（2026-10-07），对话框写明旧链接和已导入的节点立即失效、用户所有设备要重新更新订阅；后台换发不限频。
- 明文注册 IP 只从风控画像接口取。
- 产品取舍：重置密码是管理员直接设新密码，不要原因、不发邮件、不回显（D-B-2）；不给全局「默认同时在线设备」滑块；批量生成不带「开通套餐」（引导去人工开单）；设备只有在线台数与上限，不列逐台客户端与 IP（节点只上报 IP 哈希，D-B-8）；识别窗口只给 5 / 10 / 30 / 60 分钟（`DEVICE_WINDOWS`），下拉旁说明代价。
- 列表不排序（后端不支持），页脚只显示后端给的总数。
- 用户详情是 760 宽弹窗（`UserDetail.tsx`），4 个页签：资料（`ProfileTab.tsx`）、订阅与设备、订单与余额（`tabs.tsx`）、风控（`RiskTab.tsx`）；地址 `/users/list/<id>/<页签>`，资料不写进地址，旧的 devices / resets 页签落到资料。操作分到字段行旁：显示名→显示名与备注、邮箱→改邮箱、登录密码→重置、邀请人→修改、余额→跳到「订单与余额」并展开调整；启停账号与注销在资料页底部的处置区。
- 风控页签按设计稿只有两组：风险等级；注册 IP、近 7 天登录 IP 数（`loginIps7d`：画像 `ips[].last` 在近 7 天内的去重 IP 数，前端算，≥ `LOGIN_IPS_WARN` 标警示）、共享 IP 聚类（`related`）。
- 用户组删除拦截先看节点池名单（`exclusive_pools`），再看成员、套餐可见、优惠券，与后端 409 同序（`model.ts` 的 `groupBlocker`）；删除确认用固定文案 `GROUP_DELETE_NOTICE`（10-16 起没有组专属价）。删组、启停后连套餐页（`['admin','plans']`）与订阅域名页的用户组查询一起失效。新建与编辑都是弹窗。
- 用户组停用 / 启用（`POST v1/user-groups/{id}/status`，`{disabled}`，无 reauth、无幂等）：停用要确认（`GROUP_DISABLE_NOTICE`：已有成员不受影响，只是不能再加人、下拉里不再出现），启用直接做。所有「分配 / 选择用户组」的下拉与勾选（用户详情分组、批量生成、套餐可见组、节点池仅限用户组）用 `assignableGroups(groups, 当前值)` 隐藏停用组但保留当前已选的，名字用 `groupOptionLabel`（停用的加「（已停用）」）；筛选类下拉（用户列表分组筛选、批量运营按条件筛选）不隐藏，只标「（已停用）」。公告定向的组列表来自 content 后端，不带 status，暂未区分。
- 前端口径与后端逐一对齐，改后端这些函数时同步改 `model.ts`：当前订阅挑法 = subscription service 的 `currentIndex`（`panel/internal/module/subscription/service/state.go`）；套餐类型按流量配额周期认（`planKind`：`total` 是一次性，= subscription 服务 `trafficPeriod`），永久 = 期末晚于现在 + 50 年（`isPermanent` = `IsPermanent`）；批量生成校验 = identity service 的 `normalizeGenerateInput`（`ops_generate.go`）；密码预检 = identity service 的 `validatePassword`（`password.go`）。
- 设备策略请求体：strict 才带 grace，识别窗口改了才带 `window_minutes`。
- 订阅只有 `active` / `expired` 两态（`00011_subscription.sql`）：「还在用」只有一处判断 `model.ts` 的 `isLive`（`status === 'active'`），不要另写状态集合；换发链接的候选是 `rotatableSubscriptions`（还在用的，一个都没有时退回全部）。
- 加时长口径（与 `subscriptionExtendable` / `extendSubscriptionTx` 一致，`model.ts` 的 `extendableSubscriptions`）：可选生效中、过期不满 30 天（`RENEWAL_WINDOW_DAYS`，按周期末推算）且有到期时间的订阅；永久的一次性不给加时长、改到期。救回（已过期，`isRescue`）要二次确认「已过期 N 天，延长后旧链接恢复可用」。
- 订阅卡按份操作：每份订阅一张卡（当前订阅排第一），标题是备注名（`label`，没起名用套餐名；后台只读），分三块：有效期（加时长、改到期）、流量（订阅「本期流量」/ 一次性「总流量」；加流量、重置流量、改上限）、设备（在线圆点、±、不限、恢复默认）；卡底「续费这一份」（一次性写「给这份开单」，跳 `#/billing/orders?new=<用户 id>&sub=<订阅 id>`，要同步 `billing/OrdersTab.tsx`）。不适用的按钮灰着并在悬停里写明原因。已注销用户不画写按钮。
- 加流量（`GrantTrafficDialog.tsx`，`POST v1/subscriptions/{id}/traffic-pack`，路径沿用旧名，reauth + 幂等）：只对还在用的这一份；送的量订阅套餐加到本周期上限、周期结束作废，一次性加到总量上；按 GB 整数填、提交字节（上限 10240 GB）；回执 `bonus_bytes` 是这一份本期送的量合计。没有流量包、没有「未分配」余量。
- 重置流量（`SubEditDialogs.tsx` 的 `ResetTrafficDialog`，`POST v1/subscriptions/{id}/traffic-reset`，reauth + 幂等）：只重置点的这一份，原因 5–500 字。
- 调整余额（`dialogs.tsx` 的 `BalanceForm`，在「订单与余额」页签里展开）：「增加 / 扣减」单选 + 正金额（`balanceCents`），提交前确认框，原因必填；请求体只有 `{ amount, reason }`（只有人民币）。
- 批量导出没有 cookie，只能 fetch 带 Bearer 取回再存文件，要 reauth。
- 用户相关的地址被别处引用：仪表盘排行、工单「查看用户」、订单抽屉、⌘K 都跳 `#/users/list/<用户 id>`，订单标签跳 `#/billing/orders?user_id=`；改地址格式要同步这些入口。
- 批量运营是上下两段分步（批量操作用户：筛选 → 确认名单 → 选操作；批量生成用户：账号格式 → 归属与原因 → 提交）。批量生成是后台任务：提交回 202 与任务，`useGenerationJob` 轮询到结束为止，进度条按 completed/total；结果 CSV 只能从服务端下（`requestRaw`，只有提交人、要 reauth、24 小时内）。任务与加流量的 schema 在 `opsSchemas.ts`（不依赖 React，假后端测试也引用）。
- 注册设置（`SignupForm.tsx` 的 `SignupSettingsPanel`，`section` 为 growth / security；`signupSettings.ts`）：GET / POST `v1/settings/signup`，只发改了的字段（`signupBody`），所以两个组件互不覆盖；secret 只写不读（`captcha_secret` 发空串表示清除）；邮件链接登录在没配发信时禁用。
- 已注销（`status === 'deleted'`）：列表与详情标题一律 `userLabel`（「已注销 #<短 id>」，不显示占位邮箱）；详情顶部一句说明，没有任何写入口。注销对话框要填原因并输入该用户邮箱才能点，管理员不显示「注销账号」。
- 写操作：改邮箱、改邀请人（按邮箱精确找人，留空清除）、注销要 reauth；显示名与备注不要。原因 5–500 字，fields 标到对应输入框。「改到期」「改上限」只对生效中的那一份（`SubEditDialogs.tsx`、`subEdit.ts`，上限按 GiB 填、提交字节、留空为不限，最多 10 TiB）。
- 批量封禁（`BulkBanForm.tsx`）沿用批量运营的筛选：至少一个条件、带预览人数 `expected_total`、二次确认、幂等键；人数变了后端 409，要重新预览。

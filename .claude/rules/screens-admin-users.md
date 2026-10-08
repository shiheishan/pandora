---
paths:
  - "panel/frontend/src/admin/screens/users/**"
---

# 后台 · 用户

- 保留规则 2 贯穿整个模块：后台看不到订阅地址。列表、详情、画像都不含令牌；订阅标签用一句说明代替设计稿的「订阅地址 + 复制」；换发订阅链接的响应 schema 用 `.strict()` 守住「不回令牌」（`model.test.ts` 断言带令牌即判不符），成功后提示让用户到门户重新复制。换发会连节点密码一起换（2026-10-07），对话框写明旧链接和已导入的节点立即失效、用户所有设备要重新更新订阅；后台换发不限频。
- 明文注册 IP 只从风控画像接口取（`security.audit.read`），没有该权限整行不显示。
- 产品取舍：重置密码是管理员直接设新密码，不要原因、不发邮件、不回显（D-B-2）；不给全局「默认同时在线设备」滑块；批量生成不带「开通套餐」（引导去人工开单）；设备标签只有在线台数与上限，不列逐台客户端与 IP（节点只上报 IP 哈希，D-B-8）；识别窗口只给 5 / 10 / 30 / 60 分钟（`DEVICE_WINDOWS`），下拉旁说明代价。
- 用户组删除拦截先看节点池名单（`exclusive_pools`），再看成员与引用，与后端 409 同序（`model.ts` 的 `groupBlocker`）。
- 前端口径与后端逐一对齐，改后端这些函数时同步改 `model.ts`：当前订阅挑法 = `currentSubscriptionSQL`；手动重置按份（只能重置 `status=active` 的订阅，`resettableSubs`；多份时预选用得最多的，`defaultResetSub` 对应 `purchase.Options` 的重置规则，加流量预选剩余最少的 `defaultTrafficSub`）；批量生成校验 = `adminops.normalizeGenerateUsers`；密码预检 = `platform/crypto.ValidatePassword`。
- 设备策略请求体：strict 才带 grace，识别窗口改了才带 `window_minutes`。
- 加时长口径（w5expiry，与 `billing.subscriptionExtendable` / `extendSubscriptionTx` 一致，`model.ts` 的 `extendableSubscriptions`）：可选生效中、试用中、过期不满 30 天（`RENEWAL_WINDOW_DAYS`，后台列表不带关窗时刻，按周期末推算）且有到期时间的订阅。救回（已过期或试用中，`isRescue`）要二次确认「已过期 N 天，延长后旧链接恢复可用」：状态回到正常、流量按延长天数 ÷ 套餐周期天数折算进本周期、已用流量沿用；生效中没到期的只给时间
- 订阅卡按份操作（购买模型统一）：每份订阅一张卡，标题是备注名（`label`，没起名用套餐名；后台只读，改名在门户），显示这一份的流量包余量（`pack_remaining_bytes`），行上四个按钮「给这份开单」（跳 `#/billing/orders?new=<用户 id>&sub=<订阅 id>`，要同步 `billing/OrdersTab.tsx`）、「加时长」、「加流量」（只对还在用的订阅，流量包只挂这一份）、「重置流量」（`POST v1/subscriptions/{id}/traffic-reset`，按用户重置的老接口已删）。不适用的按钮灰着并在悬停里写明原因，权限不够的才不画。抽屉顶部的用户级「加时长」「加流量包」「立即重置本期」保留，多份时对话框里自己选。顶层 `unattached_pack_bytes`（还没加到任何一份的流量包）大于 0 时订阅页顶部有一条提示：写明数量、暂时不能用，以及怎样生效（没有在用订阅时开第一份会自动挂上；已有在用的要用户在门户里选一份，后台没有挪包入口，`unattachedNext`）。
- 批量导出没有 cookie，只能 fetch 带 Bearer 取回再存文件，要 reauth。
- 用户相关的地址被别处引用：仪表盘排行、工单「查看用户」、订单抽屉都跳 `#/users/list/<用户 id>`，订单标签跳 `#/billing/orders?user_id=`；改地址格式要同步这些入口。
- 批量生成是后台任务：提交回 202 与任务，`useGenerationJob` 轮询到结束为止，进度条按 completed/total；结果 CSV 只能从服务端下（`requestRaw`，只有提交人、要 reauth、24 小时内）。任务与加流量包的 schema 在 `opsSchemas.ts`（不依赖 React，假后端测试也引用）
- 加流量包（TrafficPackDialog）与加时长同权限码 `billing.adjustment.write`，只列还在用的订阅，余额挂在选中的这一份上（回执 `remaining_bytes_total` 是这一份的余量），按 GB 整数填、提交字节（上限 10240 GB，`trafficPack.ts`）

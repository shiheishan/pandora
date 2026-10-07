# 面板待办清单

面板重构时的前后端接口契约（原在 `panel/docs/redesign/`）已删除，以后以代码为准。删除前（2026-10-06）逐条核对过契约里的全部「待补」标记、第 5 节待决与定案（D-x-n）、第 7 节既有缺陷；下面只列核对时仍未实现或只实现了一部分的条目。

「出处」指原契约的节号或条目，原文可在 git 历史里按契约路径查。

## 安全与计费缺陷

1. ~~**后台改自己密码没有认证类限流**~~ —— 2026-10-06 已修（第 1 波 w1plat：按账号、按 IP 两级限流，旧口令错误按账号计数）
   - 要做：`POST v1/me/password` 要校验旧口令，应和同组的 `POST v1/auth/reauth` 一样，按账号、按 IP 两级挂 `RateLimit`。
   - 现状：`panel/internal/api/admin/router.go` 的 `NewRouter` 直接注册 `h.changePassword`，只受网关全局的按 IP 限流约束；`identity.ChangePassword` 也没有失败计数。
   - 出处：§7.3「同类都有、这条没有」。

## 后端

2. ~~**设备列表行缺 `user_id`**~~ —— 2026-10-06 后端已补（第 1 波 w1sub），第 5 条可以做了
   - 要做：`GET v1/devices` 每行加 `user_id`（不需要迁移），前端就能直接打开用户抽屉，不用再按邮箱搜（见第 5 条）。
   - 现状：`panel/internal/domain/nodefabric/device_limit_admin.go` 的 `OnlineDevice` 只有 `subscription_id`、`email` 等字段。
   - 出处：§8.2「其他发现」（后台-03 用户 · 设备策略）。

3. **节点换池冻结与 PATCH 自相矛盾**
   - 要做：先定「直接换池被冻结」是否仍有效。
     - 有效：`PATCH v1/nodes/{id}` 改 `pool_id` 也要走 `CheckNodePoolAssignment`。
     - 无效：删掉没有路由的 `assignNodePool`、`ErrNodePoolMoveFrozen` 和守它的契约测试。
   - 现状：
     - `panel/internal/api/admin/pools.go` 的 `assignNodePool` 没有注册路由，`pool_assignment_freeze_contract_test.go` 仍在守冻结。
     - `panel/internal/domain/nodefabric/node_admin.go` 的节点更新直接 `UPDATE nodes SET ... pool_id=...`，没有冻结检查。
   - 出处：§7.4「`pools.go` 的 `assignNodePool`」。

4. **两处注释与实际行为不符**
   - 要做：改注释，不涉及逻辑。
   - 现状：
     - `panel/internal/api/admin/router_nodes.go` 的 `registerNodeRoutes`：`DELETE v1/nodes/{id}` 上方仍写「外键 CASCADE 连带删指标」。实际上 `nodefabric.DeleteNode` 是把节点转为 `destroyed` 终态并改名，数据保留。
     - `panel/internal/domain/billing/release.go` 的 `AdminCancelOrder` 注释说重认证由路由负责。实际上取消订单按设计不挂重认证（与查单同门槛）。
   - 出处：§7.4 删除节点注释；§7.3「注释说要 reauth、代码没挂」中的 `orders/{id}/cancel`。

## 前端

5. **设备策略「接近上限」按邮箱找用户**
   - 要做：后端补上 `user_id`（第 2 条）后，改为按 `user_id` 直接打开用户抽屉。
   - 现状：`panel/frontend/src/admin/screens/users/DevicePolicy.tsx` 用 `find(row.email)` 搜索后再打开。
   - 出处：§8.2「其他发现」。

6. **续费下单遇 409「所选价格已下架」不切换到改价视图**
   - 要做：续费提交回 409 价格已下架时，重新拉取订阅，进入改价视图：只列当前有效价格，默认选同周期，订单预览显示「原价格已调整」。
   - 现状：
     - 打开页面时就已失效的价格（`renewal_price.available=false`）已经处理，见 `panel/frontend/src/portal/screens/checkout/model.ts` 的 `isRepriced`、`periodOptions`、`defaultPriceId`。
     - 页面打开后、提交前价格被下架的情况，只经 `checkout/index.tsx` 的 `SubmitError` 显示后端原话。
   - 出处：§4 门户-03「POST v1/me/subscriptions/{id}/renew」待补·前端。

---
paths:
  - "panel/internal/api/admin/**"
---

# 管理端路由门槛

- 每条路由的门槛在 `router_<模块>.go` 里逐条声明，顺序固定：`RequirePermission` → `RequireRecentReauth` → `Idempotency` → 处理器
  - 缺权限回 404，不暴露接口存在
  - 重认证必须排在幂等之前：403 `reauth_required` 时幂等键还没被占，前端弹框后用原键重放。守卫（按路由组）：`order_pack_routes_test.go` 的 `TestManualOrderAndTrafficPackWritesRequireRecentReauth`、`catalog_plan_update_route_test.go` 的 `TestCatalogPlanUpdateRequestGuardsPrecedeIdempotencyAndHandler`
  - 新路由声明的权限码必须在迁移的权限字典里。守卫：`permission_catalog_contract_test.go` 的 `TestRoutePermissionsExistInCatalog`
- 每条幂等路由用自己的 scope 名，不要和别的接口共用，否则同一个键在两边会被当成重放；只有同一动作的别名路由才共享（如 `nodeBatchStatusIdempotencyScope`，守卫 `security_guards_test.go` 的 `TestNodeBatchStatusAliasesShareIdempotencyScope`）
- 哪些写不要求重认证是产品决定，有测试钉着，改之前先问：
  - 向渠道查单 `POST v1/orders/{id}/query`：要订单写权限和幂等键，不要重认证（只记渠道已确认的那笔，造不出钱）。守卫 `order_query_route_test.go` 的 `TestOrderQueryRouteNeedsWritePermissionAndKeyButNoReauth`
  - 节点一步上线 `nodeActivate` 不要重认证，一步退役 `nodeRetire` 要。守卫 `node_activate_route_test.go` 的 `TestNodeActivateRouteProtection`
  - 路由组：影响多个节点的写要 `node.config.publish` + 重认证 + 幂等，新建只要幂等。守卫 `route_groups_routes_test.go` 的 `TestRouteGroupRouteProtections`
- 路由中间件只能整条挂。只有带了某个字段才需要重认证时，在处理器里按字段判（先例 `pool_user_groups.go` 的 `requirePoolGroupsReauth`，错误码与文案同 `RequireRecentReauth`）
- 审计与拉取日志里的来源 IP、UA 只存密文。解密集中在 `profile.go` 的 `decryptWith`，按表用不同 AAD：`audit_events` 用 "audit"，`subscription_fetch_log` 用 "subfetch"，提现收款信息用 "payout"；用错 AAD 会解密失败
- CSV 导出的自由文本列一律过 `audit_log.go` 的 `csvSafe`（防公式注入）。守卫 `step4_test.go` 的 `TestCSVSafe`
- 站点时区 `tenants.timezone` 是收入趋势的切日口径，也是按日用量在用户时区无效时的回退（`nodefabric.UsageLocation`）；只收能被 `time.LoadLocation` 加载的 IANA 名，拒绝空串与 "Local"（`site_settings.go` 的 `validSiteTimezone`，守卫 `step5_test.go` 的 `TestValidSiteTimezone`）

---
paths:
  - "panel/internal/api/public/**"
---

# 门户 API

- 没有权限码，边界是「只能动自己的东西」：所有权校验写在 domain 的查询里（`WHERE user_id = 本人`），不存在与不属于本人回同一个 404
- 需登录的处理器开头一律 `httpx.RequireUser`，不要自己判 Principal。守卫：`panel/internal/platform/httpx/require_user_test.go`
- 任何接口都不向门户输出节点国家与负载（节点国家只进管理端，见 `panel/internal/domain/nodefabric/node_list_admin.go` 的 `AdminNodeListRow.CountryCode`）。节点预览的字段边界守卫：`subscription_nodes_contract_test.go` 的 `TestSubscriptionNodePreviewHandlerHasSafeResponseBoundary`
- 下单、发起支付、充值、续费、变更套餐、流量包下单挂 `billing.checkout` 降级开关，礼品卡兑换挂 `marketing.giftcard.redeem`；开关门排在幂等之前。支付回调不挂开关（已发起的支付必须照常入账）。守卫：`switch_routes_test.go` 的 `TestCheckoutAndRedeemRoutesAreSwitchGated`
- 「我已支付，刷新状态」`POST v1/orders/{id}/query` 不挂 checkout 开关、不要幂等键，按账号限流每分钟 6 次；它调 `billing.QueryOrderPayment`，不记审计（后台查单走 `AdminQueryOrderPayment`，记操作人）
- 变更套餐下单走独立幂等域 `subscription_change_plan_create`，不要和新购共用
- 门户 SSE 只推本人与全租户事件

# panel/internal/api/
> L2 | 父级: /panel/internal/CLAUDE.md

三个域各一个包，对应三个网关进程；域间令牌不可交叉（EXT-001），所以没有共享的 handler 基类，共享的只有 platform/httpx 的响应模型与 middleware 链。每个包都有 router.go 装路由、handlers.go 放核心处理器、helpers.go 放包内共用逻辑、events.go 或 stream.go 提供 SSE。

成员清单
admin/: 管理控制台 API，29 文件。router/handlers/helpers 骨架，router 经 platform/webapp 在根 / 与 /assets/* 下发管理控制台前端；dashboard/revenue/system_status/access_log 读模型；catalog/coupon/coupon_batch/giftcard/manual_order/late_payment/commission/traffic_reset 商品与交易；bulk_users/usergroup/devices/profile 用户；node_admin/pools/server 节点编排；announce/content/appearance/mail/mail_template/telegram 内容与通知；events.go 管理端 SSE
public/: 用户门户 API，15 文件（traffic_packs.go 为流量包目录、下单与余额；plan_change.go 为变更套餐试算与下单，下单走独立幂等域 subscription_change_plan_create）。router/handlers/helpers 骨架，router 同样在根挂门户前端，/assets 字面量优先于订阅通配 /{prefix}/{token}；selfservice 注册登录改密、subscribe 订阅分发、my_orders/order_cancel 订单、giftcard 兑换、notifications 通知偏好、content 知识库与页面、telegram 绑定、pdnd_install 节点安装引导；events.go 门户 SSE
node/: Node 域网关，3 文件。router.go 路由、handlers.go 节点 enrollment 与配置下发及 UniProxy 兼容的用户拉取与流量上报、stream.go 节点 SSE
*_test.go: 各域处理器测试随包放置；两个 router_contract_test.go 用 chi.Walk/Match 守根上的前端挂载且 /assets/* 不抢订阅链接；admin/permission_catalog_contract_test.go 核对路由声明的每个权限码都在迁移权限字典里；admin 的 catalog_routes/coupon_routes/finance_routes 三份 AST 契约测试逐条钉死套餐、优惠券、礼品卡与分销路由的权限码、重认证与幂等域

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

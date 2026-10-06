# panel/internal/api/
> L2 | 父级: /panel/internal/CLAUDE.md

三个域各一个包，对应三个网关进程；域间令牌不可交叉（EXT-001），所以没有共享的 handler 基类，共享的只有 platform/httpx 的响应模型与 middleware 链。每个包都有 router.go 装路由（admin 的业务路由再按模块分到 router_<模块>.go）、handlers.go 放核心处理器、events.go 或 stream.go 提供 SSE；admin 与 public 另有 helpers.go 放包内共用逻辑，node 没有。

成员清单
admin/: 管理控制台 API，52 文件，见 admin/CLAUDE.md（节点处理器在 nodes.go、客服工单在 tickets.go，单节点与全局路由都在 node_routing.go、路由组在 route_groups.go，SQL 都在 nodefabric）。router/handlers/helpers 骨架，路由表按模块分在 11 个 router_<模块>.go，router 经 platform/webapp 在根 / 与 /assets/* 下发管理控制台前端；dashboard/revenue/system_status/access_log 读模型；catalog/coupon/coupon_batch/giftcard/manual_order/order_query/late_payment/commission/traffic_reset 商品与交易；bulk_users/usergroup/devices/profile 用户；node_admin/pools/pool_user_groups/server 节点编排；announce/content/appearance/mail/mail_template/telegram 内容与通知；audit_log/risk 审计与风控、ticket_macros 快捷回复；events.go 管理端 SSE
public/: 用户门户 API，22 文件，见 public/CLAUDE.md（plans.go 套餐目录、tickets.go 工单、referral.go 邀请与佣金；traffic_packs.go 为流量包目录、下单与余额；plan_change.go 为变更套餐试算与下单，下单走独立幂等域 subscription_change_plan_create；subscription_usage.go 为本期按日用量，只读、?days 1–93）。router/handlers/helpers 骨架，router 同样在根挂门户前端，/assets 字面量优先于订阅通配 /{prefix}/{token}；selfservice 注册登录改密、subscribe 订阅分发、my_orders/order_cancel/order_query 订单、giftcard 兑换、notifications 通知偏好、content 知识库、页面与「有帮助」反馈、telegram 绑定、pdnd_install 节点安装引导；events.go 门户 SSE
node/: Node 域网关，3 文件，见 node/CLAUDE.md。router.go 路由、handlers.go 节点 enrollment 与配置下发及 UniProxy 兼容的用户拉取与流量上报、stream.go 节点 SSE
response_writes_guard_test.go: 跨包直写响应守卫（本目录只有测试文件，package api）：经 platform/sourcetest.TopDecls 扫 admin、public、node 全部非测试源码，对 http.ResponseWriter 参数的 Write / WriteHeader 与 fmt.Fprint*、io.Copy、json/csv 编码器、http.Error / ServeContent 等写出一律要走 httpx；CSV 导出、SSE、订阅输出、pdnd 安装、支付与 Telegram 回执、UniProxy 配置与 304 按「包目录/文件 声明名」进 directWriteAllowed（带理由），白名单项失效也红
handler_sql_guard_test.go: admin、public、node 三个包整包「处理器不跑 SQL」守卫（第二波 api 卫生收口，取代原先 admin 按文件的两份守卫）：源码不许出现 InTx( / tx.Query / tx.Exec / QueryRow( / pgx. 等，不许导入 pgx，不许在任何值上调 Query/Exec（带参数）、QueryRow、Begin、Acquire、InTx 等查询方法（r.URL.Query() 与就绪探针 Ping 不算；把 Pool 当参数传给 domain 包级函数不算）
*_test.go: 各域处理器测试随包放置；两个 router_contract_test.go 用 chi.Walk/Match 守根上的前端挂载且 /assets/* 不抢订阅链接；admin/permission_catalog_contract_test.go 核对路由声明的每个权限码都在迁移权限字典里；admin 的五份 AST 契约测试（catalog_routes、catalog_plan_update_route、coupon_routes_contract、dashboard_routes、finance_routes_contract，经 router_source_test.go 读全部路由文件）逐条钉死套餐、优惠券、礼品卡、分销与仪表盘路由的权限码、重认证与幂等域

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

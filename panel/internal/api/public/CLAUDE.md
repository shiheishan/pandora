# panel/internal/api/public/
> L2 | 父级: /panel/internal/api/CLAUDE.md

用户门户 API：登录用户的自助接口、订阅分发、节点安装引导与回调入口。与 admin 的不同：没有权限码，边界是「只能动自己的东西」，所有权校验在 domain 查询里（WHERE user_id = 本人）；任何接口都不输出节点国家与负载（保留规则 3）。字面量路由（/assets、/pdnd、/v1）优先于订阅通配 /{prefix}/{token}。

成员清单
router.go: Deps 与 NewRouter：匿名组（登录注册、站点配置、外观、回调）与需登录组，限流与幂等挂在各自路由上
handlers.go: handlers 结构与核心处理器：探针、注册登录登出、me、改密、站点配置、套餐目录、订阅列表与续费、下单支付与支付回调、工单、钱包充值、邀请与佣金提现、我的公告
helpers.go: 包内共用小工具（签名十六进制解析等）
selfservice.go: 自助小接口：佣金转余额、会话列表与踢下线（只作用于门户会话）、工单撤回、快捷登录签发与消费
subscribe.go: 订阅分发端点 /{prefix}/{token}
my_orders.go / order_cancel.go: 我的订单列表与详情、取消待支付订单
giftcard.go: 礼品卡预览与兑换、我的兑换记录
traffic_packs.go: 流量包目录、下单（kind=addon，无订阅也能买）与我的流量包余额（D-E-1）
plan_change.go: 变更套餐试算与下单（D-E-2，kind=upgrade，升降级同一接口），下单走独立幂等域 subscription_change_plan_create
notifications.go: 站内信收件箱与已读、通知偏好（按主键 upsert）
content.go: 帮助文章列表、正文与「有帮助」反馈（反馈与详情共用可见性 query）
telegram.go: Telegram 绑定状态、绑定码与解绑
pdnd_install.go: NativeCore 一键安装脚本与二进制分发
events.go: 门户 SSE，只推本人与全租户事件
*_test.go: 路由契约（含反馈路由在需登录组）、安全契约与处理器单测；notifications_pg18_test.go 为 PG18 集成测试（public_api 域）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

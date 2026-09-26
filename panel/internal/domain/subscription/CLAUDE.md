# panel/internal/domain/subscription/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

订阅分发：用户付费与节点运行之间的最后一环，也是唯一一个未登录、面向公网的业务端点。节点资格规则只在 listEligibleNodesTx 一处（订阅下发与门户预览共用），后台的「是否下发」由 DeliveryState 复述，两处由契约测试锁同步。订阅凭据只存哈希与信封密文。

成员清单
service.go: 订阅凭据解析、节点资格查询 listEligibleNodesTx（经节点池连接套餐授权、池限定用户组时按订阅主人过滤（nodefabric.PoolAdmitsUserSQL）、服务状态、服务器就绪、协议稳定、至少心跳过一次；无池节点不下发）、门户节点预览、DeliveryState（含无池节点「未划入节点池，不服务任何用户」）、用户自助换链接 Rotate
render.go: Clash YAML / sing-box JSON / base64 URI 三种格式渲染，字段按上游源码核对
usage_daily.go: 门户按日用量读模型 DailyUsage（门户-02 柱状图）：缺省窗口为本期流量周期（配额行 → 订阅周期 → 最近 30 天），?days 覆盖，最多 93 天，补零、今天与日均；日界与写入端共用 nodefabric.UsageLocation / UsageDay
admin_rotate.go: 管理员代换订阅链接：旧链接即刻失效、写审计，新令牌明文不交给管理员（D-B-1）
*_test.go: 渲染与资格契约测试；node_preview_pg18_test.go 为资格规则的 PG18 集成测试（run-pg18-gates.sh 的 node_preview 域），usage_daily_pg18_test.go 为按日用量读模型的 PG18 集成测试（usage_daily 域），delivery_pg18_test.go、pool_user_groups_pg18_test.go 与 device_window_pg18_test.go（设备识别窗口决定在线数与 strict 摘除，R103）在同一份夹具上对照节点用户列表、订阅下载、门户预览三处交付集合（无池节点、节点池限定用户组；delivery 域，与 api/admin 同域），同包三域靠精确过滤互不拉入

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

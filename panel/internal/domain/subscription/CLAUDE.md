# panel/internal/domain/subscription/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

订阅分发：用户付费与节点运行之间的最后一环，也是唯一一个未登录、面向公网的业务端点。节点资格规则只在 listEligibleNodesTx 一处（订阅下发与门户预览共用），后台的「是否下发」由 DeliveryState 复述，两处由契约测试锁同步。订阅凭据只存哈希与信封密文。

成员清单
service.go: 订阅凭据解析、节点资格查询 listEligibleNodesTx（服务状态、服务器就绪、协议稳定、至少心跳过一次）、门户节点预览、DeliveryState、用户自助换链接 Rotate
render.go: Clash YAML / sing-box JSON / base64 URI 三种格式渲染，字段按上游源码核对
usage_daily.go: 门户按日用量读模型 DailyUsage（门户-02 柱状图）：缺省窗口为本期流量周期（配额行 → 订阅周期 → 最近 30 天），?days 覆盖，最多 93 天，补零、今天与日均；日界与写入端共用 nodefabric.UsageLocation / UsageDay
admin_rotate.go: 管理员代换订阅链接：旧链接即刻失效、写审计，新令牌明文不交给管理员（D-B-1）
*_test.go: 渲染与资格契约测试；node_preview_pg18_test.go 为资格规则的 PG18 集成测试（run-pg18-gates.sh 的 node_preview 域），usage_daily_pg18_test.go 为按日用量读模型的 PG18 集成测试（usage_daily 域），同包两域靠精确过滤互不拉入

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

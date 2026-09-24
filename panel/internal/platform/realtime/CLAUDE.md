# panel/internal/platform/realtime/
> L2 | 父级: /panel/internal/platform/CLAUDE.md

服务端推送（SSE）。只推「什么变了」（topic + 定位 id），不推数据本身：权限过滤留给既有 REST 接口，丢一条通知由下一条拉取自愈，所以可靠性来自「真相在数据库」，不来自消息通道。多个网关进程经 Valkey Pub/Sub 互通；没有 Valkey 时退化为进程内广播（测试即如此）。

成员清单
realtime.go: Hub 与 NewHub：本机连接的订阅索引（按频道）、Publish / Subscribe / Count、频道名（ChannelPublic / User / Ticket / Admin / Node / NodeAll）与 FormatSSE；有 Valkey 时启动跨实例消费与连接数上报
listener.go: StartDBListener 把 notify_change 触发器的 pg_notify 负载转成前端主题（topicFor 表名映射）；高频写入的表（quota_balances、按日用量）不挂通知
connections.go: 跨进程在线连接数：每个进程每 30 秒把 Count 写进 rt:sse:conns:<实例> 键（TTL 90 秒、关闭时删除），SSEConnections 按前缀求和，供后台系统状态的 sse 组件
*_test.go: realtime_integration_test.go 为进程内广播与订阅索引的集成测试

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

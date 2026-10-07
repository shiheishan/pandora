---
paths:
  - "panel/internal/domain/nodefabric/uniproxy*.go"
  - "panel/internal/domain/nodefabric/usage_daily.go"
  - "panel/internal/domain/nodefabric/node_list_admin.go"
  - "panel/internal/domain/nodefabric/device_limit_admin.go"
  - "panel/internal/domain/subscription/**"
  - "panel/internal/domain/adminops/**"
  - "panel/internal/domain/support/**"
---

# 交付集合与订阅口径

## 谁能连哪些节点（三处必须同口径）
- 交付集合由三处共同决定：节点拉用户 `nodefabric.ListNodeUsers`、订阅下载与门户节点预览共用的 `subscription.listEligibleNodesTx`、后台「是否下发」的复述 `subscription.DeliveryState`。改资格条件要三处一起改。守卫 `subscription/eligible_nodes_contract_test.go:TestSubscriptionAndPreviewShareOneEligibilityQuery`、`heartbeat_delivery_test.go:TestDeliveryStateMatchesEligibilitySQL`，PG18 的 delivery 域在同一份夹具上对照三处
- 没划进节点池的节点不服务任何人（fail closed），不能当成对所有订阅开放的公共节点
- 节点池限定用户组的谓词只有 `nodefabric.PoolAdmitsUserSQL` 一份，参数只接受两个调用方写死的列表达式（白名单外直接 panic），不要另写变体（守卫 `pool_admission_test.go:TestPoolAdmitsUserSQLIsTheOnlyAdmissionRule`）。默认组（user_group_id 为空）的用户进不了任何限定了组的池
- 心跳超时不在 SQL 里排除（agent 挂了而代理还在跑很常见），降级在 Go 侧做；从没心跳过的节点则不下发
- 改变交付集合的后台写（套餐换绑池、池的用户组名单、用户换组），提交后调 `nodefabric.Service.NotifyUsersChanged` 发租户级 `node.users.changed`

## 「当前订阅」口径
- 后台「在用 / 当前订阅 / 在用套餐名」只用 `subscription/current.go` 的 SQL 片段（`LiveStatusesSQL` = active / trialing / grace / past_due、`CurrentSQL`、`ActivePlanNameSQL`、`HasLiveSQL`）。adminops 的用户列表、套餐在用数、风控成员，support 的 `user_active_plan` 都引用它，不要各写一份
- 下发资格不走这个口径：下发看 active / trialing / grace（不含 past_due），见 `listEligibleNodesTx`
- current.go 的片段内部占用别名 `s` 与 `pl`，调用方传入的列表达式不能引用外层叫这两个名字的表

## 按日流量与设备窗口
- 按日流量的切日只用 `nodefabric.UsageLocation` / `UsageDay`：写入（流量上报）和读取（门户柱状图）必须是同一个函数，否则同一笔流量会落在不同的「那一天」。用户时区为默认 'UTC' 视同未设，跟随站点时区
- 流量上报按 uid 排序逐个记账：先锁本周期配额行，再按先到先扣锁流量包；扣量与当日用量行在同一事务（重试报文两边都不记）
- 设备识别窗口的可选值 `DeviceWindowMinutes` 与迁移 00094 的 `app.device_limit_window_minutes` 一一对应，在线统计一律调这个库函数，不写死 interval；`PurgeStaleAlive` 的截止必须大于最大窗口。守卫 `device_window_test.go:TestDeviceWindowHasOneSource`

## 订阅拉取与在线设备的热路径
- `subscription_online_devices` 视图（00098）按订阅 LATERAL 聚合、窗口每条语句只算一次；调用方按单订阅 `LEFT JOIN LATERAL (… WHERE od.subscription_id = s.id)` 用它，条件一定能推进视图
- node_alive_ips 由 aegis-admin 的保留期任务清理（保留 70 分钟 > 最大设备窗口 60 分钟），清理截止不能小于最大窗口
- 订阅拉取：令牌按 `token_hash`（唯一索引）查；读在一个只读事务里一条语句取齐，写（凭据计数、拉取日志、限流）合成一个事务；未认证的失败按来源采样写日志，不每次落库；非「不存在」的错误对外仍伪装 404、对内打 ERROR
- 订阅里的节点列表按（租户, 套餐版本, 用户组）进程内缓存 20 秒，`node.*` 信号失效；认证、用量、限流一律现查，不缓存

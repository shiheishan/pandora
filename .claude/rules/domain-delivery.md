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
- 其中与用户、套餐都无关的那一半（节点自己能不能被发出去：服务器 ready 未删、服务状态 active、见过心跳、协议稳定、端口合法、有可连地址）只有 `subscription.DeliverableNodeSQL()` 一份，三个调用方：`listEligibleNodesTx`（再叠加套餐绑池与池的用户组限定）、套餐页每个池的 `deliverable_nodes`（`adminops.PlanPools`）、后台节点列表的「是否下发」（`NodeDeliverability` → `NodeDeliveryFacts.Refine`，在 `DeliveryState` 判「下发」之后补齐服务器未就绪、协议不完整、池没绑套餐三种说明）。改节点侧条件只改这一处
- 没划进节点池的节点不服务任何人（fail closed），不能当成对所有订阅开放的公共节点
- 节点池限定用户组的谓词只有 `nodefabric.PoolAdmitsUserSQL` 一份，参数只接受两个调用方写死的列表达式（白名单外直接 panic），不要另写变体（守卫 `pool_admission_test.go:TestPoolAdmitsUserSQLIsTheOnlyAdmissionRule`）。默认组（user_group_id 为空）的用户进不了任何限定了组的池
- 心跳超时不在 SQL 里排除（agent 挂了而代理还在跑很常见），降级在 Go 侧做；从没心跳过的节点则不下发
- 节点报告运行异常（`nodefabric.RuntimeFailingSQL`：端口被占、没起来、期望版本生效失败）同样不排除、只降级：`preferFreshNodes` 先摘降级节点再分新鲜，全是降级节点时照发，不给空订阅。后台「是否下发」对它显示 `subscription.DegradedNote`
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
- 过期订阅（2026-10-07 规则 1，w5expiry）：令牌有效、凭据 active、订阅已过期（status=expired 且窗口没关，或 active 但走过截止）时 `checkCredential` 回 ErrExpired，`LoadPull` 返回 `Pull.Expired`、不取节点；handler 回 200 只含一条提示节点（render_expired.go，「已于 X 到期，续费后更新订阅即可恢复」，X 用用户时区到分钟），Subscription-Userinfo 的 expire 是过去时刻，带 profile-web-page-url（门户续费页），更新间隔 1 小时，拉取日志记 expired。令牌不存在、已吊销（含关窗吊销）仍是伪装 404
- 门户链接列表照常列出过期 30 天内订阅的链接（`Link.Expired` 只读）；过期期间用户换链接回 409（`ErrRotateWhileExpired`）；`mySubscriptionsSQL` 的 renewable 与 `subscriptionAcceptsPaidChange` 同口径（含窗口内的 expired）
- 重置订阅链接（2026-10-07 用户定，w6plan）：`rotateInTx` 在同一事务里吊销旧凭据、签新凭据、`rotateProxyUUIDTx` 换 `subscriptions.proxy_uuid`（与 Xboard「重置订阅」一致），门户 `Rotate` 与后台 `AdminRotate` 同一个函数。00101 的下发纪元随订阅行更新推进，handler 提交后发一次 `node.users.changed`；pdnd 按用户 ID 记连接，旧 UUID 的连接被断开。门户重置按用户限频（`api/public` 的 `subscriptionRotateLimits`：间隔 10 分钟、24 小时 5 次），后台换发不限频。守卫 `api/public/rotate_pg18_test.go:TestSubscriptionRotatePG18`

## 流量上报记账（uniproxy_traffic.go，2026-10 w3node）
- 节点只能扣自己当前放行名单（`ListNodeUsers`）里的 uid；名单外与不合规条目（非整数 uid、不是恰好两个 0–30GB 的整数）照样留档，不扣费，计入 `PushResult.Invalid`，不拒整份报文
- 扣配额取「已开始且没结束」的行，外加每种周期里开始得最晚、结束不到 1 天的那一行（滚动空窗照扣，RollQuotaPeriods 结转时一并带走；结束更久的是没对齐的旧数据，不扣）；consumed 与当日用量用饱和加法
- 记账事务遇 40P01 / 40001 整笔重试，最多 3 次
- 幂等：带 `X-Report-Id`（`NormalizeTrafficReportID` 校验，1–64 个 `[A-Za-z0-9._:-]`）的上报按 (节点, 编号) 去重，`INSERT … ON CONFLICT (node_id, client_report_id) DO NOTHING`（00123 唯一部分索引），冲突了另记一行不带编号的重复件（duplicate_of 指向第一份），只留档不扣费；不论隔多久、是否并发。不带或编号不合规走原来的 10 秒内容哈希去重（不拒收：拒收会让 pdnd 丢掉这份流量）
- 入口两个：`ReportTraffic`（无编号）与 `ReportTrafficWithID`，都只许 `api/node` 的 uniPush 调（守卫 `TestReportTrafficOnlyReachedThroughAuthentication`），共用 `reportTraffic`

---
paths:
  - "pdnd/node/**"
  - "pdnd/panel/**"
  - "pdnd/release/acceptancepanel/**"
  - "pdnd/main.go"
  - "panel/internal/api/node/**"
  - "panel/internal/domain/nodefabric/service.go"
  - "panel/internal/domain/nodefabric/effective_release_codec.go"
  - "panel/internal/domain/nodefabric/heartbeat.go"
  - "panel/internal/domain/nodefabric/nonce_*.go"
  - "panel/internal/domain/nodefabric/server_identity_nonce.go"
---

# 节点端与面板的闭环（pdnd/node、pdnd/panel）

## 分层

- `panel/` 只管协议与线格式，不做编排；编排只在 `node/`。`node/` 向下只认 `core.Core` 抽象，不知道背后是 NativeCore 还是 compat 构建的兼容内核。
- 两条并列通道：兼容通道 `Client` 走 UniProxy（Xboard / V2board 兼容，bearer 鉴权）；签名通道 `SignedClient` 走 `/v1/nodes/*`（Ed25519 请求签名 + 配置验签）。用户列表与流量上报只有兼容通道；配置与心跳在签名通道存在时优先走它。
- SSE 事件流只是加速通路：失败一律退避重连、不向上冒泡，轮询永远在、不能因为有流就停轮询。退避在一次健康连接（回 200 且至少读到一帧，心跳也算）之后复位（`stream_backoff_test.go`）。
- `panel/enrollment.go` 的两阶段接入是首装产生节点身份的唯一入口；不要另开写 identity.json 的路径。

## 用户镜像不变式

- `node.Node` 是单 goroutine 主循环：轮询、上报、状态节拍与 SSE 事件在同一个 select 里串行处理，用户镜像因此不加锁。不要把用户同步挪到别的 goroutine。
- `n.known`、`n.userVersion`、`Client` 里的用户 ETag 三份说的是同一件事「内核里已是这一版」。内核用户表被清空（入站重建、回滚失败）时只能经 `resetUserMirror` 一起作废，漏掉任何一份都会让节点带着空表跑（`user_resync_test.go`）。
- ETag 归 `panel.Client` 存，何时作废由 `node/` 决定：入站重建即 `ForgetUsersVersion`；配置应用失败且节点已停即 `ForgetConfigVersion`，旧配置仍在服务时不作废。ETag 只在解析成功后才记，304 返回 changed=false 而不是空列表（`users_etag_test.go`）。

## 签名通道的配置台账

- 签名通道拉生效配置时带请求头 `X-Applied-Effective-Release`（已应用的版本），面板判定仍是当前版就回 204、不签名不写库；switched / health_passed 回执被面板收下就不再重报，版本变化时再报；签名钥匙约 10 分钟查一次、验签失败立刻补查；各节拍 ±10% 抖动。「哪个版本已知装不上」的台账在 `node/signed_config.go`，不在 `panel/`。
- 版本身份 `signedConfigKeyOf` 只认内容（生效发布 release_id + generation + content_sha256；旧式 version + hash），不认每轮重签都变的 issued_at、签名（`TestSignedConfigKeyIgnoresResigning`）。
- 坏版本：旧配置仍在服务时不再试装，节点已停时每轮按拉取间隔重试；failed 每版本只报一次，送不到（传输错误、5xx、408、429）随后续拉取补报，其余 4xx 即作罢。区分靠 `panel.StatusError`，`SignedClient.Do` 对非 2xx 必须返回它（`signed_failure_test.go`、`TestReportSettled`）。

## 与面板的跨 module 契约（改一边要同步另一边）

- 节点请求签名原像 `PANDORA-NODE-REQUEST-V2` 有三份独立实现：面板 `nodefabric.CanonicalPayloadV2`（`api/node` 的 `requireNodeSignature` 验签）、`pdnd/panel/signed.go` 的 `SignedClient.Do`、`release/acceptancepanel`。生效发布契约 `aegis-node-effective-config-release-v1` 的原像同样分在面板 `nodefabric/effective_release_codec.go`、`pdnd/panel/effective_release.go`、acceptancepanel 三处。
- `release/acceptancepanel` 按面板实现独立重写原像与校验，刻意不 import `pdnd/panel`：夹具借用被测代码，两边一起错时验收照样变绿。只有它的 `main_test.go` 可以用 `pdnd/panel` 的 `SignedClient` 对打。
- `panel/heartbeat_metrics.go` 把采样换算成面板 `nodefabric.Metrics` 的整数口径并钳到库列范围；面板改 Metrics 字段或单位要同步（`TestHeartbeatMetricsJSONMatchesPanelContract`）。`HostCapacity` 同时填进 enrollment begin 请求。
- 在线上报（alive）的设备键：pdnd `core.DeviceKey` 与面板 `nodefabric.aliveDeviceKey`（IPv4 按地址、IPv6 按 /64、已是 /64 串与解析不了的原样）同一口径，面板侧 `TestAliveDeviceKeyMatchesPdndDeviceKey` 读 pdnd 的 `TestDeviceKey` 表对照。
- 兼容通道 `/status` 只报机器级指标，不带任何用户信息。
- 面板的压测模拟节点 `panel/tools/loadtest/nodesim` 逐段对齐这里的节拍、ETag、退避与签名流程；改这里的行为要同步那边。

## 面板侧节点网关（api/node、nodefabric 的推送与签名，2026-10 w3node）

- `StreamConn.Send` 从不关闭，关闭只关 `closed`；写协程以 `Closed()` 退出。往已关闭通道发送曾让 aegis-node 整个崩溃（守卫 `nodestream_race_test.go`）
- 用户名单推送按版本共享编码（`nodestream_users.go`）：全量按版本、增量按（起点, 终点）只编码一次，帧直接写共享字节，每帧有写期限。增量只在面板确知节点手上是起点版本时发：REST `/user` 拉取在途或送出过别的版本（`BeginUsersPull`）一律改推全量——pdnd 收增量只核对流版本、不核对内核名单
- 重连带 `X-Users-Version`（与 ETag 同源）且等于当前版时不推首个全量，但连接标脏，下一次变更推全量；首帧 `retry:` 给 0–10 秒随机重连建议（pdnd 第 4 波再用）
- 签名请求：nonce 先在进程内近期集「查并占」（`nonce_recent.go`，16 字节摘要键），再在 Valkey `SET NX PX` 认领，Valkey 出错或超时回落 PG。存储状态机 healthy → down（冷却 1 秒）→ probing（只放一个探测请求）→ healthy，恢复后立即只走 Valkey；只有探测的应答改状态，非探测请求的失败带恢复代号，过期代号的失败不算。近期集到期与冷却按单调钟算，墙钟只用来和签名时间戳比。nonce 认领用 aegis-node 里单独的 Valkey 客户端（`nonceRedisOptions`：读写拨号 250ms、`ContextTimeoutEnabled`），go-redis 缺省不看 ctx 期限、实际要等 3 秒；不要再加「回落后若干分钟内每个请求都写 PG」的窗口（w10quiet 实测一次超时换来约 11 分钟、近 20 万次插入）。PG 里有、近期集里没有的 nonce 只有两种来源（上次运行留下的行、离开近期集的回落条目），都折成「签名时间戳补查界」：时间戳不晚于它的请求在 Valkey 认领后再在 PG 补查（`nonce_guard.go` 文件头）。后端不可用（库、Valkey、超时）回 503，身份无效、签名错、重放才回 401
- 缓存身份的纪元复核：纪元监听健康时在验签里按监听戳判新鲜（不读纪元，见 `domain-nodefabric-release.md`「纪元监听」）；不健康时心跳与拉生效配置并进自己的那一次查询（心跳写入在 SQL 里以 `activeIdentitySQL` + 公钥为门槛），其余签名端点在中间件里复核。新端点默认放中间件组（守卫 `TestDeferredIdentityConfirmationIsLimitedToHandlersThatConfirm`）。身份已复核的心跳走 `HeartbeatConfirmed`（可合并），欠复核的走 `HeartbeatSigned`（门槛写）
- 遥测写（心跳、探针点、在线 IP、nonce 清理）走 `db.BatchOptions{AsyncCommit: true}` 或 `SET LOCAL synchronous_commit = off`；记账、身份、配置一律同步提交
- nodes 上不要再加含心跳类列的索引（00116 删了 idx_nodes_heartbeat 换 HOT 更新）；给 nodes 加列要同步加进 `zz_notify_nodes_update` 的列清单（PG18 守卫对照 information_schema）
- aegis-node 启动不强依赖 Valkey：连不上只告警

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
- 兼容通道 `/status` 只报机器级指标，不带任何用户信息。
- 面板的压测模拟节点 `panel/tools/loadtest/nodesim` 逐段对齐这里的节拍、ETag、退避与签名流程；改这里的行为要同步那边。

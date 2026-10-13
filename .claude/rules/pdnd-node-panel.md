---
paths:
  - "pdnd/node/**"
  - "pdnd/panel/**"
  - "pdnd/release/acceptancepanel/**"
  - "pdnd/main.go"
  - "proto/pandora/node/**"
---

# 节点端与面板的闭环（pdnd/node、pdnd/panel）

通道是一条 gRPC 双向流（`proto/pandora/node/v1/node.proto`，设计见 `docs/architecture.md` 第 11 节）：
`EnrollmentService.Enroll` 一次性接入换客户端证书，`ControlService.Session` 是 mTLS 长连接会话。
一台服务器一个身份、一条会话，名下多个节点（入站）在同一条流里。

## 分层

- `panel/` 只管协议与线格式，不做编排：接入（`enroll.go`）、身份目录（`identity.go`）、会话（`session.go`）。
  会话收到的配置、名单、流量回执经 `Events()` 交给编排层；编排层要发的心跳、流量、在线、回执经 `Send*` 交回来。
- `node/` 是编排，只认 `core.Core` 抽象，不知道背后是 NativeCore 还是 compat 内核。
- `release/acceptancepanel` 按面板契约独立实现 CA 指纹、证书签发与会话规则，刻意不 import `pdnd/panel`：
  夹具借用被测代码，两边一起错时验收照样变绿。只有它的 `main_test.go` 可以用 `pdnd/panel` 对打。

## 接入与身份

- `pandora-native enroll --panel <host:port> --ca-sha256 sha256:<hex> --token-file <文件>`（或 `--token-stdin`）；
  不接受命令行明文令牌。CA 指纹是 `sha256:` + 小写 hex(SHA-256(CA 证书 SPKI DER))。
- 接入时 TLS 只信指纹匹配的那张 CA（`verifyPinnedChain`：链里找 IsCA 且指纹相等的证书，再只用它验叶子的主机名与 serverAuth）；
  不过就中止握手，令牌不发出（`TestEnrollRejectsWrongFingerprint`、`TestEnrollRejectsWrongHost`）。
- 私钥本地生成（P-256），不出机器。接入日志 `enroll.json`（request_id + 私钥 + 令牌哈希）在发请求之前落盘，
  崩溃或回包丢失后同令牌重跑沿用同一 request_id（`TestEnrollRetryReusesRequestID`）。
- 身份目录（缺省 `/var/lib/pandora-native/identity/`）：`ca.pem cert.pem key.pem server_id`，文件 0600、目录 0700。
  整套替换走 `next/` + `ready` 标记再逐个 rename（`stageAndPromote`），`LoadIdentity` 先补完或丢弃半截的 `next/`。
  不要另开写这几个文件的路径。目录须属运行用户（systemd 的 StateDirectory 会把 `/var/lib/pandora-native` 整个改给 `pandora`）。
- 证书剩余不到 10 天在会话里续期（RenewRequest → Certificate）：新私钥在内存里等证书到了再一起落盘；
  每条新流新建 ClientConn，下一条流就用新证书。

## 会话

- 第一条发 Hello（agent_version、二进制摘要、features、HostInfo、各节点已应用的配置摘要与名单版本），第一条收必须是 Welcome
  （Welcome 之前收到 Tombstone 也认）；Welcome 之前 `Send*` 一律返回 false。
- 每条流带 metadata `x-request-id`（新 UUID）。发送队列有界，满了就断流重连，不阻塞主循环。
- 断线全抖动退避 1s–30s，拿到过 Welcome 的流结束后复位。`Command.reconnect` 按 in_ms + [0, jitter_ms) 重连；
  Tombstone 后 `Run` 返回 `ErrTombstoned`、不再重连，节点继续用磁盘缓存服务。
- UsersFull 分块在 `panel/` 里收齐（`usersAssembler`），不全就回 `UsersAck{ok:false, reason:incomplete_chunks}`。

## 编排不变式

- `node.Server` 是单 goroutine 主循环：会话事件、上报、心跳、内务（1 秒）节拍在同一个 select 里串行处理，节点状态不加锁。
  唯一例外是给 Hello 用的已应用版本快照（`Applied()`），改了 appliedSHA / started / userVersion 之后经 `publishApplied` 刷新。
- ServerConfig 是全量：按列表顺序装（同机端口先到先得，赢家固定），不在列表的节点停掉（先收最后一轮流量）。
  冷启动按缓存里记的顺序装。
- 配置版本身份只认 `content_sha256`（config_json 原字节的摘要）。坏版本台账每节点一个槽：旧配置在服务时按 1、2、4、5 分钟退避重试，
  节点已停时每分钟重试；面板重发同一个坏版本不重装，只补报 failed。
- 配置回执 switched / health_passed / failed 按「摘要 + 阶段」每条流各报一次（`n.acked` 每次 Welcome 复位），丢在断开的流上的由
  Hello 对齐兜底。health_passed 要切换后稳定 5 秒且内核 `InboundReady`。
- 用户镜像：`n.known` 与 `n.userVersion` 说的是同一件事「内核里已是这一版」，内核用户表被清空（入站重建失败、回滚失败、名单预装失败）
  时只能经 `resetUserMirror` 一起作废，并回 `UsersAck{ok:false, reason:resync}`。
- 名单增量只在本地确是 from_version 时打，否则回 `version_mismatch`；装不进内核回 `apply_failed`。面板对任何 ok=false 都应发全量。
- 先有名单、再开 accept：入站起来之前收到的全量存在 `n.staged`，装入站时经 `ApplyInboundWithUsers` 一起交给内核；
  兼容内核装完入站再补装，换配置重建入站后把手上的名单补回。
- 流量不丢：按 (节点, uid) 合并；inflight 收到 TrafficAck 才丢，每个上报节拍与每次 Welcome 原样重发（同一 report_id）；
  缓冲上限 2^17 条，超限先丢最旧的 inflight 并计数。退出时先关内核让在途连接入账，再报最后一轮并等 ack。
- 落盘缓存（`<cache_dir>/nodes/`）：`server.json` 记身份与节点顺序，每节点 `config.json`（原字节 base64 + 摘要）与 `users.json`，
  0600 / 0700。读回时核对摘要，换了身份的缓存整个丢弃。冷启动先装缓存再连面板，心跳报 `serving_cached_config` 直到 Welcome。

## 心跳与运行状态

- 心跳带 `HostMetrics`（整数口径：CPU 万分比、负载 ×100、字节）与每个节点的 `NodeRuntime`：
  RUNNING；DEGRADED（新配置装不上、旧配置在服务：`config_apply_failed` 或内核原因；靠缓存服务：`serving_cached_config`）；
  DOWN（入站没起来：`not_started` 或内核原因）。
- 内核端口冲突原因 `port_in_use:<端口>/<tcp|udp>:<占用者节点 id 或 other>` 上报时拆成 `reason=port_in_use:<端口>/<L4>`
  与 `reason_node_id`（同一台服务器的节点才写得出 id：入站归属按服务器身份哈希登记）。
- 在线地址按 (节点, uid) 上报，设备键口径由内核保证（IPv4 按地址、IPv6 按 /64，`core.DeviceKey`）；一条消息至多 5000 条。

## 节点配置文档（NodeConfig.config_json）里节点端读取的键

面板按这张表组装文档；这里没有的键节点端不读。协议由 `NodeConfig.node_type` 决定（文档里的 protocol 不读）。
文档整份作为 `core.InboundConfig.Raw` 交给内核；内核的 `rawString` 类帮手对下划线与大小写宽容，面板一律用下划线小写。

通用（`node/config_apply.go`）：

| 键 | 类型 | 说明 |
|---|---|---|
| `server_port` | 整数 1–65535，必填 | 入站端口。监听地址固定为全部地址（不读 listen_ip）。 |
| `kernel` | 字符串，可选 | 承载运行时：空或 auto / pandora-native；multi 构建可显式选 sing-box / xray-core，native-only 运行时拒绝兼容内核。 |
| `outbounds` | 数组，可选 | 每项 `tag`、`type`（字符串，必填）、`settings`（对象，可选，按出站类型解析）。 |
| `routes` | 数组，可选 | 每项 `outbound`（必填，引用 outbounds 的 tag）、`matcher`（对象，可选）。 |
| `final` | 字符串，可选 | 没命中规则时走的出站。outbounds / routes / final 都不给 = 没配分流（直出）；给了空的 = 清空已有规则。 |

共用块（`kernel/`）：

- `network`：tcp（缺省）/ ws / httpupgrade / grpc / xhttp / xhttp-h3 / mkcp / udp，按协议取值，见 `kernel/capabilities.go` 的能力矩阵。
  - ws、httpupgrade：`ws_path`（没有就读 `path`）、`host`（没有就读 `server_name`）。
  - grpc：`grpc_service_name` 或 `grpc_path`、`host`。
  - xhttp / xhttp-h3：`host`、`path`、`mode`、`sc_max_buffered_posts`、`sc_max_each_post_bytes`、`sc_min_posts_interval_ms`、
    `sc_stream_up_server_secs`、`server_max_header_bytes`、`uplink_http_method`、`uplink_chunk_size`、
    `session_placement` / `session_key`、`seq_placement` / `seq_key`、`uplink_data_placement` / `uplink_data_key`；
    xhttp-h3 不走 REALITY 时要 cert_path、key_path。
  - mkcp：`kcp_settings` 对象（`mtu`、`tti`、`uplink_capacity`、`downlink_capacity`、`congestion`、`read_buffer_size`、
    `write_buffer_size`、`seed`、`header.type`、`mask` / `mask_password`，或 mask 数组每项的 `type` 与 `settings.password`）。
- TLS：`tls`（布尔）为真时要 `cert_path`、`key_path`（节点机上的文件路径）。
- `security`：vless 是 none / reality（TLS 用 `tls` 键）；trojan 是 tls / reality；vmess 里是加密方式。
- REALITY（`security: reality`）：`dest`、`server_names`、`private_key`、`short_ids`、`max_time_diff`、`xver`。
- 探测回落 `fallback`：`"host:port"` 字符串（trojan、anytls、naive）。

按协议：

| 协议 | 读的键 |
|---|---|
| vless | network、security、tls / cert_path / key_path、REALITY 块、传输块 |
| vmess | network、security（加密方式）、tls / cert_path / key_path、`alterId`（只接受 0）、传输块 |
| trojan | network（tcp / ws / httpupgrade / grpc / mkcp）、security、tls / cert_path / key_path、REALITY 块、fallback、传输块 |
| shadowsocks | `method`、`password`（2022 系列的服务端密钥）、network（tcp / udp）；`plugin` 非空即拒绝 |
| hysteria2 | network（只 udp）、cert_path、key_path（必填）、`obfs`（对象：`type: salamander`、`password`）、`up_mbps`、`down_mbps`、`udp_timeout`、`udp_queue_size` |
| tuic | network（只 udp）、cert_path、key_path（必填）、`congestion_control`（cubic / new_reno / bbr）、`alpn`、`zero_rtt`、`auth_timeout`、`heartbeat`、`udp_timeout`；出现 `udp_over_stream` 即拒绝 |
| anytls | network（只 tcp）、cert_path、key_path、`tls`、`padding_scheme`、fallback |
| juicity | network（只 udp）、cert_path、key_path（必填）、`congestion_control` |
| naive | network（只 tcp）、security、tls / cert_path / key_path、fallback |
| socks、http | network（tcp；socks 另可 udp）、security、tls / cert_path / key_path |
| shadowtls | network（只 tcp）、`password`、`version`（缺省 3）、`strict`、`server` 或 `handshake_server`、`server_port`（握手服务器端口，缺省 443）、`wildcard_sni`、`method`（内层 shadowsocks 方法） |
| mieru | `transport`（tcp / udp） |

数字一律经 `internal/confnum` 解析（JSON 数字与数字串都认）；时长类键（udp_timeout、auth_timeout、heartbeat、max_time_diff）
接受秒数或 Go 时长串。改内核读的键要同步这张表与面板的配置组装。

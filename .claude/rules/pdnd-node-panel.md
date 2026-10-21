---
paths:
  - "pdnd/node/**"
  - "pdnd/panel/**"
  - "pdnd/release/acceptancepanel/**"
  - "pdnd/main.go"
  - "pdnd/acme_config.go"
  - "pdnd/certs/**"
  - "pdnd/upgrade_cmd.go"
  - "pdnd/upgrade/**"
  - "pdnd/release/signmanifest/**"
  - "pdnd/release/pandora-native*.service"
  - "pdnd/release/pandora-native-upgrade.path"
  - "proto/pandora/node/**"
---

# 节点端与面板的闭环（pdnd/node、pdnd/panel）

通道是一条 gRPC 双向流（`proto/pandora/node/v1/node.proto`，设计见 `docs/architecture.md` 第 11 节）：
`EnrollmentService.Enroll` 一次性接入换客户端证书，`ControlService.Session` 是 mTLS 长连接会话。
一台服务器一个身份、一条会话，名下多个节点（入站）在同一条流里。

## 分层

- `panel/` 只管协议与线格式，不做编排：接入（`enroll.go`）、身份目录（`identity.go`）、会话（`session.go`）。
  会话收到的配置、名单、流量回执经 `Events()` 交给编排层；编排层要发的心跳、流量、在线、回执经 `Send*` 交回来。
- `node/` 是编排，只认 `core.Core` 抽象，发布物里背后只有 NativeCore（`kernel.NativeCore`），测试里换成 fake。
  NativeCore 要提供的能力（整代装入站 `ApplyInbound` / `ApplyInboundWithUsers`、就绪探测 `InboundReady`、入站归属 `SetInboundOwner`）都是 `core.Core` 接口本身的方法，编排层不做可选接口断言；fake 实现完整接口（`node/fixture_test.go` 的 `fakeKernel`）。
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
  Tombstone（或建流被拒并带重新接入 trailer）后 `Run` 返回 `ErrTombstoned`、不再重连；`Server.stopAllForTombstone` 立刻停掉全部入站并删掉落盘缓存，重启也不靠缓存恢复（用户 10-13 定）。
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
- 先有名单、再开 accept：入站起来之前收到的全量存在 `n.staged`，装入站时经 `ApplyInboundWithUsers` 一起交给内核
  （名单来源依次是内核里现有的 `n.known`、`n.staged`、冷启动时的落盘缓存，都没有就带空名单）；入站起来了、名单没装上（`UsersPreloadFailed`）
  按内核用户表已空处理。只有回滚装回旧配置时走 `ApplyInbound`，随后 `AddUsers` 补回旧名单。
- 流量不丢：按 (节点, uid) 合并；inflight 收到 TrafficAck 才丢，每个上报节拍与每次 Welcome 原样重发（同一 report_id）；
  缓冲上限 2^17 条，超限先丢最旧的 inflight 并计数。退出时先关内核让在途连接入账，再报最后一轮并等 ack。
- 落盘缓存（`<cache_dir>/nodes/`）：`server.json` 记身份与节点顺序，每节点 `config.json`（原字节 base64 + 摘要）与 `users.json`，
  0600 / 0700。读回时核对摘要，换了身份的缓存整个丢弃。冷启动先装缓存再连面板，心跳报 `serving_cached_config` 直到 Welcome。

## 在线升级（pdnd/upgrade、upgrade_cmd.go、node/upgrade.go）

两块共用的规格（文件格式、原因码、路径、能力名）以 `docs/architecture.md` 11.6 为准；面板侧在 `panel/internal/module/node/`。

- 信任根是二进制里 `go:embed` 的 `upgrade/keys/*.pub`：面板给的清单、签名、地址都只当搬运工。节点进程与 root 助手各验一遍（`upgrade.Check`：验签 → 解析 → 本机架构 → serial），
  助手用的是**已安装的 root 属主二进制自己的**公钥与 buildSerial。`keys/` 里没有 `.pub` = 一切升级 `signature_invalid`。
- `main.buildSerial` 由 `panel/deploy/build-release.sh` 以 `-X` 注入（提交时间）；缺省 `"0"` 是 dev 构建，不声明能力、助手也拒绝。
  `upgrade_v1` 只在四项都满足时声明（`upgradeEnv.upgradeUnsupported`）：serial > 0、内置公钥非空、二进制就是 `/usr/local/bin/pandora-native`、装了 `/etc/systemd/system/pandora-native-upgrade.path`。
- 节点进程（`upgrade.Agent`）只写 `/var/lib/pandora-native/upgrade/`：下载只走 https（`upgrade.allow_http` 只给验收），不跟随降到 http 的跳转，5 分钟、128 MiB；
  日志与回报里的地址去掉查询串。`next.tmp` 核过摘要才 rename，`request` 最后写；任一步失败不留半截。`request` 或 `applying` 已在 = `busy`。
- root 助手（`upgrade-apply`，`upgrade.Helper`）：不收参数，路径全用固定值，不信 request 里的任何路径。暂存目录一律经 `os.Root`；每个文件 Lstat 只认普通文件、大小上限、
  `O_NOFOLLOW|O_NONBLOCK` 打开后 `SameFile` 核对、链接数必须为 1（防硬链接），先拷进 root 0700 工作区 `/var/lib/pandora-native-upgrade/` 再验，验过的那份才装。
  工作区 `applying` 记 installing / installed / rolling_back，被打断后按当前二进制摘要续做；只补一次确认窗口（`Resumed`），回退不来回循环；记录最后删。
  重启前必须写成暂存 `applying` 并删掉旧 `healthy`（`prepareMarker`）：写不了就不重启、直接装回 `.prev`，结局 `apply_failed`（否则新进程写不出 healthy、面板却在新 Hello 上记成功、随后必然回退）。删不掉暂存 `request` 时报错退出，免得 path 单元空转。
  节点安装器重跑时先停助手、清掉工作区与暂存件（`pandora_reset_upgrade`，`result` 留着），免得续做把 `.prev` 装回来盖掉新装的版本。
- 编排（`node/upgrade.go`）：只有本条流 Welcome 协商到 `upgrade_v1` 才接指令；暂存在 goroutine 里跑，结局经 `upgradeDone` 回主循环，主循环仍单 goroutine 无锁。
  同时只许一次。Welcome 时写 `healthy`（`applying` 的目标摘要等于自己的 binary_sha256 才写）；Hello 带上的 `last_upgrade` 内容一致才删结果文件；内务节拍把结果报出去后删。
  成功不写结果文件：面板看新 Hello 的 binary_sha256。`LastUpgrade()` 在会话 goroutine 里调，只读文件。
- systemd：`pandora-native-upgrade.path` 监视 `request` 与工作区 `applying`（后者让助手被打断后开机续做）；助手单元 oneshot、`TimeoutStartSec=600`。
  主单元 `StartLimitIntervalSec=300` / `StartLimitBurst=10`，助手重启前先 `reset-failed`。三个单元由安装器内联生成，`TestInstallScriptUpgradeUnitsMatchPackaged` 逐行对照 `pdnd/release/`。
- 测试不需要 root 与 systemd：Agent / Helper 的根目录、二进制路径、`ServiceManager`、时钟、睡眠都可注入。

## 心跳与运行状态

- 心跳带 `HostMetrics`（整数口径：CPU 万分比、负载 ×100、字节）与每个节点的 `NodeRuntime`：
  RUNNING；DEGRADED（新配置装不上、旧配置在服务：`config_apply_failed` 或内核原因；靠缓存服务：`serving_cached_config`）；
  DOWN（入站没起来：`not_started` 或内核原因）。证书：`cert_pending`（acme 证书还没到：没在服务就 DOWN，旧配置在服务就 DEGRADED）、
  `cert_expired`（DEGRADED：在服务的证书过期了，继续服务、只如实上报，用户 10-20 定）。
- `NodeRuntime.cert`（`CertStatus`）：acme 节点报证书管理器的状态（标识符、到期、最近失败的原因码与详情、下次尝试），
  因 cert_pending 装不上时报新配置的标识符；file 节点只报装上时从 `cert_path` 读出的到期时间；不用证书的不带。
- 内核端口冲突原因 `port_in_use:<端口>/<tcp|udp>:<占用者节点 id 或 other>` 上报时拆成 `reason=port_in_use:<端口>/<L4>`
  与 `reason_node_id`（同一台服务器的节点才写得出 id：入站归属按服务器身份哈希登记）。
- 在线地址按 (节点, uid) 上报，设备键口径由内核保证（IPv4 按地址、IPv6 按 /64，`core.DeviceKey`）；一条消息至多 5000 条。

## 自动申请入站证书（pdnd/certs、node/certs.go、acme_config.go）

产品规则与用户文档见 `docs/node-certificates.md` 第 1 节；排障见 `panel/deploy/RUNBOOK.md` 11.5。

- `certs.Manager` 用 lego v5 向 Let's Encrypt 申请（HTTP-01，本机 80 端口；IP 标识符带 `shortlived` 档，续期带 ARI 的 replaces），单 goroutine `Run` 逐件办：
  申请 / 续期 > 7 天闲置回收 > 每 6 小时查 ARI。失败退避 15 分钟起翻倍到 4 小时（±10%），`rate_limited` 按 Retry-After，80 被占每 5 分钟。
  原因码表在 `certs/reason.go`，面板前端 `runtime.ts` 的 `certReasonText` 与 RUNBOOK 11.5 跟着它改。
- 落盘 `<cache_dir>/acme/`（目录 0700、文件 0600，原子写）：`account.json` 与 `certs/<规整后的标识符>/`。账户无邮箱、注册即同意订户协议。
  读盘的证书冷启动即可用；`Certificate` 只做原子读（写时复制的 map），内核握手路径上不加锁。
- 编排（`node/certs.go`）：每次 ServerConfig（与冷启动的缓存）先把全部 acme 标识符整体交给 `Want`，不论这次能不能装上；
  装之前 `certPrecheck`：证书没到返回 `*core.CertPendingError`、不碰内核，走坏版本台账；`Updates()` 一到，因这个标识符 cert_pending 的节点立刻重装。
  在服务的证书过期报 DEGRADED `cert_expired`。Tombstone 时 `Purge` 删掉账户与全部证书。node 包只认 `CertManager` 接口，不 import `certs` 与 `kernel`。
- 装配（`main.go`）：内核 `SetCertSource` 与 `node.Options.Certs` 在冷启动之前就位；Manager 建不起来只记错，手填证书的节点照常服务。
  config.json 的 `acme` 段（`directory_url` 只收 https、`http_port`、`ca_file`）只给验收与测试，生产不写。Hello 声明 `cert_acme_v1`。
- 测试：`certs/` 用进程内 pebble + 假 DNS（`testca_test.go`），不联网；`node/certs_test.go` 用 fake 管理器。

## 节点配置文档（NodeConfig.config_json）里节点端读取的键

面板按这张表组装文档；这里没有的键节点端不读。协议由 `NodeConfig.node_type` 决定（文档里的 protocol 不读）。
文档整份作为 `core.InboundConfig.Raw` 交给内核；内核的 `rawString` 类帮手对下划线与大小写宽容，面板一律用下划线小写。

通用（`node/config_apply.go`）：

| 键 | 类型 | 说明 |
|---|---|---|
| `server_port` | 整数 1–65535，必填 | 入站端口。监听地址固定为全部地址（不读 listen_ip）。 |
| `outbounds` | 数组，可选 | 每项 `tag`、`type`（字符串，必填）、`settings`（对象，可选，按出站类型解析）。 |
| `routes` | 数组，可选 | 每项 `outbound`（必填，引用 outbounds 的 tag）、`matcher`（对象，可选）。 |
| `final` | 字符串，可选 | 没命中规则时走的出站。outbounds / routes / final 都不给 = 没配分流（直出）；给了空的 = 清空已有规则。 |
| `cert_mode` | 字符串，可选 | `acme`（节点端自动申请）/ `file`（缺省）。只对带证书的协议有意义（见下「TLS 证书」）。 |
| `cert_identifier` | 字符串，acme 时必填 | 证书名字：SNI，没有就用节点地址（面板 `config_docs.go` 写入，协议配置里夹带的会被删掉）。 |
| `ech_keys` | 数组，可选，≤8 项 | ECH 服务端密钥（迁移 00045）：每项 `config`（base64，单个 ECHConfig，版本 0xfe0d）、`private_key`（base64，32 字节 X25519）、`send_as_retry`（布尔，只有当前钥为 true）。面板只给能用 Go 标准库 TLS 的入站下发（vless/vmess/trojan 普通 TLS、AnyTLS、naive 等，不含 REALITY 与 QUIC 类）；节点端在 `loadInboundTLSConfig` 与 AnyTLS 的 TLS 配置上装 `EncryptedClientHelloKeys`，开了即只收 TLS 1.3（`kernel/inbound_tls_ech.go`）。轮换后旧钥在过渡期内仍下发，供拿着旧订阅的客户端解密。 |

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
- TLS 证书：`cert_mode` 为 `acme` 时要 `cert_identifier`（面板算好的证书名字，域名或 IP，已规整），不许带 `cert_path` / `key_path`；
  否则（`file` 或缺省）`tls`（布尔）为真时要 `cert_path`、`key_path`（节点机上的文件路径）。检查与取证书在 `kernel/inbound_cert.go`，规整口径
  `core.NormalizeCertIdentifier` 与面板 `protocol.NormalizeCertIdentifier` 必须一致。vless / vmess 不收 acme（普通 TLS 未开放，xhttp-h3 只手填）。
- `security`：vless 是 none / reality（TLS 用 `tls` 键）；trojan 是 tls / reality；vmess 里是加密方式。
- REALITY（`security: reality`）：`dest`、`server_names`、`private_key`、`short_ids`、`max_time_diff`、`xver`。
- 探测回落 `fallback`：`"host:port"` 字符串（trojan、anytls、naive）。

按协议：

| 协议 | 读的键 |
|---|---|
| vless | network、security、tls / cert_path / key_path、REALITY 块、传输块 |
| vmess | network、security（加密方式）、tls / cert_path / key_path、`alterId`（只接受 0）、传输块 |
| trojan | network（tcp / ws / httpupgrade / grpc / mkcp）、security、tls / cert_path / key_path、REALITY 块、fallback、传输块 |
| shadowsocks | `method`（经典 AEAD 五种；2022 只认 `2022-blake3-aes-128-gcm` / `2022-blake3-aes-256-gcm`，`2022-blake3-chacha20-poly1305` 没有 SIP023 身份头，按「不支持的加密方式」拒绝启动入站）、`password`（只 2022 方法有：标准 base64 的服务端身份密钥 iPSK，解出长度必须等于方法的密钥长度，否则入站不启动；用户的 uPSK 由 UUID 算，见 pdnd-kernel 规则文件）；同端口总是 TCP+UDP，带 `network` 键即拒绝；`plugin` 非空即拒绝 |
| hysteria2 | network（只 udp）、cert_path、key_path（必填）、`obfs`（对象：`type: salamander`、`password`）、`up_mbps`、`down_mbps`、`udp_timeout`、`udp_queue_size` |
| tuic | network（只 udp）、cert_path、key_path（必填）、`congestion_control`（cubic / new_reno / bbr）、`alpn`、`zero_rtt`、`auth_timeout`、`heartbeat`、`udp_timeout`；出现 `udp_over_stream` 即拒绝 |
| anytls | network（只 tcp）、cert_path、key_path、`tls`、`padding_scheme`、fallback |
| juicity | network（只 udp）、cert_path、key_path（必填）、`congestion_control` |
| naive | network（只 tcp）、security、tls / cert_path / key_path、fallback |
| socks、http | network（tcp；socks 另可 udp）、security、tls / cert_path / key_path |
| shadowtls | network（只 tcp）、`password`、`version`（缺省 3）、`strict`、`server` 或 `handshake_server`（必须写成 host:port：配置文档摊平后 `server_port` 永远是入站端口）、`wildcard_sni`、`method`（内层 shadowsocks 方法） |
| mieru | `transport`（tcp / udp） |

数字一律经 `internal/confnum` 解析（JSON 数字与数字串都认）；时长类键（udp_timeout、auth_timeout、heartbeat、max_time_diff）
接受秒数或 Go 时长串。改内核读的键要同步这张表与面板的配置组装。

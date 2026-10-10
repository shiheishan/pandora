---
paths:
  - "pdnd/kernel/**"
  - "pdnd/core/mieru/**"
---

# NativeCore 数据面（pdnd/kernel）

协议以 Adapter 注册进 `AdapterRegistry`，只经 `DataPlane` 拨号与监听，流量在此按用户计量。增删协议另见 pdnd-build-boundary 规则文件。

## 接客骨架

- TCP 类入站的 accept 循环一律走 `accept_loop.go` 的 `runAcceptLoop`（RealityListener 也是）。传进去的 handle 必须立刻返回：循环里只登记连接、起 goroutine，TLS / REALITY 握手一律放在连接自己的 goroutine 里。否则一条只连不说话的连接能让整个入站在握手超时内接不进新连接（`accept_silent_test.go`）。
- Accept 出错：done 关或 `net.ErrClosed` 即退出，其余一律退避重试（5ms 起翻倍、封顶 1 秒，不区分 Temporary）。不退避会在 EMFILE 时空转跑满一核，出错即退会让入站永久停止接客（`TestAdapterAcceptLoopsBackOffOnEMFILE`、`TestRealityListenerSurvivesEMFILE`）。
- `core/mieru/listener.go` 有一份同值的退避参数：kernel 依赖 core/mieru，那边不能反向引用，改一处要同步另一处。
- 握手限时：普通 TLS 用 `inboundHandshakeTimeout`（10 秒，`withHandshakeDeadline` / `serverTLSHandshake`），REALITY 握手 worker 15 秒。Close 打断的握手不算失败、不上报。ShadowTLS 组合入站把 `inboundHandshakeTimeout` 显式传给 `nativewire/shadowtls`，只限认证判定之前，判定后的诱饵中继不限时（原则见 pdnd-forks 规则文件）。
- 全部 HTTP 承载（WebSocket、HTTP Upgrade、gRPC、XHTTP、Naive）的 `http.Server` 只经 `newInboundHTTPServer` 构造，靠它的 `ReadHeaderTimeout` 给 TLS 握手与请求头限时；不要直接 `&http.Server{}`（`TestInboundHTTPServerBoundsHandshake`）。
- 它同时设 `ErrorLog`（`inbound_http_errorlog.go`）：net/http 缺省把「TLS handshake error from 完整IP」逐条写标准库 log、不限流。TLS 由 net/http 握手的入站（Naive）把 ErrorLog 换成带自己 connErr 的那份，握手失败走 OnConnError；panic（`http:` 与 `http2:` 两种前缀）按 Error 记；只丢 `httpPeerNoisePrefixes` 逐条列出的对端噪声，其余按 WARN 记（如 `http: Accept error … too many open files`）；记之前行内 ip:port 截网段。

## 认证失败的回落

- Trojan / AnyTLS / Naive 认证不过、或不是本协议客户端，一律交给 `probe_fallback*.go`：配了 `fallback` 由回落站点应答，没配是中性 404；不回 407、不带 realm。
- HTTP 承载的回落里，CONNECT 不走 `httputil.ReverseProxy`：Go 1.26.9 起它对 CONNECT 一律走 ErrorHandler（CVE-2026-56866），回成节点合成的 502 就是指纹。改由 `probeFallbackConnect` 发 `CONNECT /`：只转请求头、不带请求体（探测方在 CONNECT 后面带的字节一个也不进回落站点），专用 Transport 关 keep-alive，回落站点回 2xx 也只转这一个响应、不建隧道。守卫 `probe_resistance_connect_test.go`（h2 与 http/1.1 都带走私字节）。兼容内核 `core/sing/naive_masquerade.go` 同一口径各留一份。
- 本机 Go 1.27.1 早于这批修复（h1 服务端 CONNECT 后不关连接，会接着服务流水线请求）：跑这组测试用 `GOTOOLCHAIN=go1.26.9`，与 CI 同版本。

## 转发的空闲回收

- `core.Relay` 两个方向都没有数据超过 `runtime.connection_idle_seconds`（默认 1800 即 30 分钟，用户 10-08 定；与 Xray connIdle 同义但 Xray 默认 300；0 不回收）即断开，整段无数据的长连接（iperf3 的控制连接、不发保活的 SSH）也在此列。10-08 验收记的「iperf3 控制连接第 240 秒被断」是 `-i 60` 的汇报粒度，回环 `-i 2` 复测断在 298–300 秒，就是这个回收。

## 连接失败观测链

- 适配器在失败点经 `connErrorReporter` 调 OnConnError；`reportAdapterConnError(Addr)` 统一跳过 `io.EOF`、`net.ErrClosed`、`context.Canceled`，不要在适配器里各自过滤。
- 日志里不落完整用户 IP 与凭据：`maskRemoteAddr` 把对端截到 IPv4 /24、IPv6 /48（与面板 `middleware.ByIPPrefix` 同粒度），`sanitizeConnErrorReason` 抹掉 UUID、IP、域名、长 hex/base64 并截断。用户 UUID 就是口令，日志只记内部 ID（`core/mieru` 同此）。
- `connerror_log.go` 按 (入站, 协议, 阶段, 分类) 固定窗口限流，锁内只计数、日志写在锁外。
- 新增协议要在 `connerror_hook_test.go` 或 `connerror_hook_client_test.go`（真实 QUIC / mieru 客户端）补至少一条失败路径真的到达 OnConnError。

## 在线设备与设备数限制

- 各适配器内嵌一份 `online_devices.go` 的 `onlineDevices`（零值可用），不要再各写 `map[用户]map[IP]struct{}`。按设备键记引用计数：同一设备多条连接只占一个名额，计数到 0 才离线；设备上限按不同设备数判。
- 设备键是 `core.DeviceKey`（用户 10-09 定）：IPv4 与 IPv4 映射地址按单个地址，IPv6 按 /64 网段（写成 `2001:db8:1:2::/64`）。OnlineIPs 上报的就是设备键；面板 `aliveRows` 取哈希前再按同一口径归一（`nodefabric/alive_device_key.go`），新旧节点混跑也不多算。两边读同一张用例表（pdnd `core/device_key_test.go` 的 `TestDeviceKey`），改口径要两边一起改。mieru 的 `core/counter.OnlineTracker` 也用它。
- 写法固定为 `if !a.online.enter(user, ip) { 拒绝 }` 紧跟 `defer a.online.leave(user, ip)`，异常退出也经 defer 撤销。新增协议要进接线用例：TCP 进 `online_devices_wiring_test.go`（同 IP 两条关一条仍在线、全关才离线、别的设备占满名额时被拒），有 UDP 路径的再进 `online_devices_wiring_udp_test.go`。
- 例外：mieru 用 `core/counter.OnlineTracker`（最后活跃时间 + 5 分钟 TTL，不按连接计数）；ShadowTLS 委托内层适配器。

## 协议实现约定

- UDP 中继的上下行必须解耦，各一个 goroutine 阻塞读，收尾走 `udp_relay.go` 的 `relayUDPDirections`（SOCKS5、Trojan、VMess command=UDP 共用）。下行不能与上行在同一循环轮询，否则回程被限成几包每秒（`*DownlinkBurstWithoutUplink` 系列测试）。
- Shadowsocks AEAD 分块负载上限 0x3FFF（SIP004），读写两侧共用 `ssChunkLimit`；超长块规范客户端会断连（`shadowsocks_chunk_test.go`）。
- VLESS / VMess 的 mux（XUDP）刻意留在 NativeCore 内实现，不委托兼容内核。
- `reality_client.go` 的 `ParseRealityClientConfig` 目前只有单测消费，自检与 h3 探针都不调用它。
- XHTTP 按请求判定角色（`xhttp.go` 的 `classifyRequest`），适配器只按 `XHTTPSession.Kind` 分派。auto（节点缺省）照 Xray 服务端逐请求收：不带会话的上行请求是 stream-one；带会话的 GET 是下行；带会话与序号的是 packet-up；带会话无序号的是 stream-up。Xray 的 auto 在 REALITY 上选 stream-one（有 downloadSettings 时选 stream-up），其余选 packet-up。
  - 会话型请求共用 `xhttp_session.go`：一个会话只许一种上行，第二条 stream-up 回 409。
  - 未认证会话（协议层还没清掉读截止）按 `xhttp_budget.go` 限会话数与待读字节，超出回 503。
  - 出错只回状态码，不写错误文本。
- Vision 直通（command=2）之后，读写都改走外层的底层连接，不再经外层加解密（`vision_direct.go`）。
  - 切读方向前要交出外层已缓冲的 input 与 rawInput：REALITY 经 fork 的 `TakeBufferedForDirect`，普通 TLS 经垫在 `tls.Server` 下的 `visionTLSTap`（`vision_tls_tap.go`），它在 Vision 会话期间按记录逐条交付。
  - 直通后 Close / CloseWrite 先作用于底层 TCP；踢人（`user_sessions.go` 的 `closeAbruptly`）先关底层 TCP 再关外层，避免外层 close_notify 卡 5 秒写截止或落进裸流。
  - 回归测试 `TestVLESSRealityVisionInnerTLS13Direct` 含合包变体。
  - 读侧确定不会再切直通（`releaseDirectWatch`）后才打开 REALITY 的机会式多读（fork 的 `SetReadCoalescing`：一次 Read 解出 rawInput 里已到齐的多条记录）；切直通之前打开会把对端的裸流量当记录解密、连接即断。守卫 `TestVLESSRealityVisionPlainPassthroughBulk`。
- VMess 照 Xray：响应头首字节回显请求头的 V 字节；aes / chacha / auto 默认带 GlobalPadding（0x08）。
  - 每块先从同一条 SHAKE128(IV) 流取填充长度（%64），再取长度掩码；读写两侧次序必须一致。
  - 终止空块的长度等于「标签 + 填充」，要整块读掉。
  - Xray 的 none 带 ChunkStream + ChunkMasking：TCP 不加填充，UDP 可加填充。
  - 回归测试用 Xray 的编码库（`vmess_xray_client_test.go`）。

## Hysteria2 / TUIC 的 UDP 收发效率

QUIC 栈是 sagernet/quic-go（经 sing-quic 与 `internal/nativewire` 的 hy2 / TUIC fork），不 fork quic-go：整份 fork 要给 `connection.go`（3000 多行）加 800 行豁免，需用户授权；本地实验里「同步发送」「ACK 抽稀」对 TCP 没有收益（见 w9quic 报告）。效率改动都在 quic-go 的公开接口之外：

- DATAGRAM 分片按连接的实际上限（`nativewire/dgram`）：用必然超长的缓冲调 `SendDatagram` 问上限（只比长度、不发包），缓存 1 秒；实际只用上限减 `AckMargin`（64）。不留余量时贴上限的 DATAGRAM 碰上带 ACK 的包装不下，quic-go 试 10 次就丢（`TestLimitSizedDatagramsSurviveBidirectionalTraffic` 去掉余量即红）。不要改回上游固定的 1197。
- 上行 UDP 转发（`hysteria2_udp.go` 的 `hy2BatchWriter`）：同一目标、等长的连续包合成一条 UDP_SEGMENT（GSO）消息，经 `WriteBatch` 的 OOB 交给内核；iovec 直接指向各包不复制。私网拦截仍逐消息判定（一条 GSO 消息同一目标），被拒（EIO / EINVAL 等）时本会话关 GSO、从失败那条起逐条重发。只在 Linux，回归测试 `hysteria2_udp_gso_linux_test.go`。
- Salamander 混淆在 Linux 上走 `batchSalamanderConn`：自己实现 quic-go 认的 `OOBCapablePacketConn` 与 `ReadBatch`，quic-go 才会照常 recvmmsg 批量收、GSO 发；GSO 写时逐段加盐、段长改为原段长 + 8。它不内嵌 `*net.UDPConn`，未混淆的收发方法一个都不透出。
- hy2 / TUIC 入站起来后检查 UDP 收发缓冲（`quic_socket_linux.go`）：pdnd 无 CAP_NET_ADMIN，受 `net.core.rmem_max / wmem_max` 限制拿不到 quic-go 要的 8MB，不足一半就 Warn 一次并提示 sysctl。上限由面板的节点安装脚本写进 `/etc/sysctl.d/90-pandora-native.conf`（16MB，`pdndSysctlFunction`），手工安装见 `release/README.md`；不要给 unit 加 CAP_NET_ADMIN。
- UDP 转发的出站 socket（`newHy2UDPUpstream`）固定申请 `hy2UDPSocketBuffer`（106496，内核记账与读回 212992，即常见发行版的 rmem_default、官方 hysteria 与 sing-box 不调时的大小）。10-09 vpcnode2 实验 B：rmem_max 8MB 档下申请 4MB 让 hy2 UDP 下行输官方 hysteria 5–6 个百分点，只改这一项即拉平；持续过载时大缓冲只多收注定在 QUIC 发送侧被丢的包。数据与理由写在常量注释里，改值要带同口径的复测数据。上游 socket 每会话一个，固定申请也让每会话的 udp_mem 记账不随机器的 rmem_default 膨胀。默认拦私网时经 `outbound.UDPBatchConn` 调缓冲，不拿裸 socket。
- 空闲的 hy2 / TUIC UDP 会话不常驻收发缓冲（10-09 vpcnode2 缺陷 1：原先每会话约 2.6MB）。
  - 下行（`hysteria2_udp_downlink.go`）：冷态经 `internal/udprecv` 的收包器等 socket 可读，等待期间不占缓冲：`RawConn.Read` 的第一次回调只用 1 字节 MSG_PEEK 看有没有包、不借组，没有就交给 netpoller 等；叫醒后才借 2 包小组、非阻塞 recvmmsg。每次醒来「窥视落空 + 收到」两次系统调用、借还一次，与持有缓冲阻塞读（sing-box）同形状。
    - 冷态借小组先占冷态名额 `hy2DownlinkColdSlots`（16×GOMAXPROCS，4 核 64 组 8MB）：借到组就被调度挂起的会话会偶发地一下堆到几百个（VPC 1024 冷会话：250–720 组，RSS +60–120MB；改前的 baa6519 也偶发 580 组），靠减少借还压不住。冷态小组还组前不等 I/O（两包总能整包复制，写回在还组之后），等名额的会话不会被卡住的写回连累；以后若让小组在还组前写回，先改这条。守卫 `TestHy2DownlinkColdSlotsBoundProbeGroups`。热态持组不占冷态名额（另有热态名额）。
    - 第一次回调就借组收（落空再还）：每次醒来借还两次，VPC 1024 冷会话时存货表排队、小组存货涨到 450–514 组（RSS +70MB），守卫 `internal/udprecv` 的 `TestReadyHoldsNoBatchWhileWaiting`（等待期间借 0 次、一次醒来借 1 次）。
    - 「上次收空了就跳过第一次回调直接等」会漏包：Go 的 netpoller 在 `RawConn.Read` 开始时清掉就绪标记（`poll_runtime_pollReset`），之前到的包不再触发（Linux 实测一轮 120 包只收到 85 包）。
    - 不要改回「阻塞窥视等包、再借缓冲收」（每次醒来三次，review-r6 第 1 条：每 Gbps 多约 10% CPU），守卫 `TestHy2DownlinkRecvSyscallsPerPacket`（真 socket 数 `Receiver.Recvs`，冷态与中速每包 ≤2.05）。不要在回调里嵌套 x/net 的 ReadBatch（同一个 fd 的读锁，死锁）。
    - 醒来后小组连收两次都满才借 32 包批量组（三五包的小串不占 2MB，守卫 `TestHy2DownlinkSmallBurstStaysOnProbeGroup`）。冷态每次收完先把包复制成按长度分配的小缓冲、归还收包组再写回：写回在 QUIC 的 DATAGRAM 队列满时阻塞，零拷贝时每个卡住的会话占着 128KB；复制按实际占用的块封顶 `hy2DownlinkCopyBudget`（128KB）：sing 的 `buf.NewSize` 按 2 的幂分档（64B 起，到 64KB），`Cap()` 报的是请求长度，记账用 `hy2CopyBlockSize`（review-r5 K1，4097×35、16385×11 两档守着）。一次收到的大包超出上限时，剩下的占着收包组零拷贝写回，写完才归还收包组（批量组连同名额与份额；review-r5 K2，守卫 `TestHy2DownlinkLargeOverflowIsolation`），小组与批量组走同一段代码。每个卡住的冷态会话的常驻上限：hy2 单包 ≤4096，32 包恰好 128KB，复制不会超出；TUIC 单包可到 0xffff，复制 ≤128KB，另有全进程至多「批量名额」个会话各占一个 2MB 批量组（守卫 `TestHy2DownlinkStuckLargePacketsBounded`、`TestHy2BlockedUDPSessionHeap`）。冷态每秒不到 500 包，复制开销可忽略，热态仍零拷贝。
  - 进出热态用同一把尺子：一段 `hy2DownlinkWarmIdle`（100ms）的平均速率到每秒 500 包（`hy2DownlinkWarmMinPackets` 50 包）。段长 20ms 时段的起点落在包上，音频（每 20ms 一包）加视频（每 33ms 一串 12 包，合计约每秒 410 包）常有一段折算超过每秒 500 包，名额被占满（review-r5 K3，实测平均 62/64；「连续两段 20ms 都够」仍有 20/64）。守卫 `TestHy2DownlinkMixedAVStaysCold`、`TestHy2DownlinkPacedBurstStaysCold`、`TestHy2DownlinkPairedLowRateStaysCold`、`TestHy2DownlinkStaleColdCountDoesNotWarm`。热态省的是借还与复制（零拷贝），系统调用次数与冷态相同。
  - 热态名额 `hy2WarmLimit`（全进程一份 `hy2DownlinkWarm`，64×GOMAXPROCS 个，每用户 1/4）由会话带着（`hy2Downlink.warmLimit`）：同时处在热态的会话有上限，热态小组的常驻（含还回后留在存货里的）才有界；占不到的会话照常冷态收包。测试注入自己的名额，不替换全局变量（替换时别的会话还占着旧名额，归还会永远阻塞，复审 P2）。守卫 `TestHy2DownlinkWarmSlotsCapped`；生产入口 `hy2DownlinkUDP` 必须用共享的那一份，守卫 `TestHy2DownlinkUDPUsesSharedWarmLimit`。
  - 每次改读截止后都再看一次 ctx：回冷态清截止会盖掉收尾时 AfterFunc 刚设的截止，只能靠这次复查结束（守卫 `TestHy2DownlinkClearDeadlineRacesCancel`）。非阻塞收报读截止到点不是会话结束（`TestHy2DownlinkDeadlineDuringDrainKeepsSession`；真 socket 上非阻塞收走 `RawConn.Control`、不看截止，守的是处理）。上行用 `WaitReadPacket` 零拷贝等首包，凑批才借缓冲。
  - 借还走存货表（`hysteria2_udp_stock.go` 的 `hy2Stock`）：借时有就拿、没有才新分配，还时一律收下，闲置后由后台回收，回收时长每张表各自定：下行收包组 5 秒（组大，高峰过后不久留），上行凑批缓冲 30 秒（本机探针：5 秒时每 7 秒一阵的上行每阵都重新分配，TotalAlloc 多 2–3 倍；上行组逐条分配、高峰占用小）。守卫 `TestHy2StockJanitorPerStockIdle`、`TestHy2StockTrimKeepsRecent`。不用 sync.Pool（按 P 存放、换 P 拿不回，GC 清一半），也不用定长空闲表（表满即丢、表空即分配，审查探针里 2 秒新分配 2425 组）。守卫 `TestHy2DownlinkBuffersNotReallocated`。
  - 批量组只在占到名额时借：全局 `hy2DownlinkBatchSlots`（8×GOMAXPROCS 组），每用户最多 1/4（`hy2DownlinkBatchPerUser`，`relayHy2UDP` 按 userID 认人）。写回在 QUIC 的 DATAGRAM 发送队列满时阻塞，借来的缓冲占到返回为止；名额或份额不够时用小组收，每个卡住的会话最多 128KB。守卫 `TestHy2DownlinkPerUserShare`、`TestHy2DownlinkSlotsExhaustedOthersStillForward`、`TestHy2DownlinkSlotReturnedOnPanic`。
  - 共享缓冲的前提：nativewire 的 hy2 / TUIC `WritePacket` 返回前同步编码并复制（`SendDatagram` 自带复制），返回后不再引用负载。改成异步持有负载，借来的缓冲还回后会被别的会话（别的用户）覆盖，等于串数据。守卫 `hysteria2_udp_writecopy_test.go`（经真客户端，WritePacket 返回后涂掉缓冲、客户端仍收到原文）与 `TestHy2DownlinkCrossSessionIsolation`。
  - 其余守卫：`hysteria2_udp_mem_test.go`（空闲、活跃、卡在写回的存活堆）、`hysteria2_udp_recv_test.go`（真 socket 逐字节、IPv4 / IPv6 来源、收尾、不留 goroutine）、`TestHy2DownlinkMediumRateTwoRecvsPerPacket`。`hysteria2_udp_share_test.go`、`hysteria2_udp_warm_test.go` 用上游替身（按 udprecv 的语义实现 `Ready` / `Recv`：先窥视、空就等，`calls` 按系统调用计，窥视也算），一次收包能给多包，批量组、名额与热态在 darwin 上也走得到；真 socket 的用例在 darwin 上一次只收一包（recvfrom），recvmmsg 与批量路径只在 Linux CI 上走到。
  - 下行收包器不分协议族（IPv6 上游同样就绪收、在 Linux 上 recvmmsg 批量收）；默认拦私网时收包器由 `outbound.UDPBatchConn.Receiver()` 给出，只能收，裸 socket 不出 outbound 包；上行批量发送仍只在 IPv4 本地地址时。加密、封装类出站只有 `net.PacketConn`，下行退回阻塞 ReadFrom、常驻 64KB。
- hy2 / TUIC 每用户在途 UDP 会话上限 `quicUDPSessionsPerUser`（1024，`quic_udp_quota.go`），挡 fd 与 udp_mem 被单个用户耗尽；超出拒新不踢旧，走 OnConnError 的 limit 分类。
- `internal/nativewire` 的 hy2 / TUIC 服务端在 QUIC 连接断开时关掉其上全部 UDP 会话（`closeUDPSessions`）：会话 ctx 来自服务而非连接，不关要等 udpTimeout（5 分钟）才收尾，期间占着上游 socket、在线设备与会话名额（`TestOnlineDevicesWiringUDP` 守着）；置 `udpClosed` 与拷列表在同一把 `udpAccess` 里，之后到的包不再建会话（TUIC quic 中继模式会在单向流 goroutine 里建，`TestNoUDPSessionAfterClose`）。
- 内核里直接用包级 `slog.*` 的日志（缓冲告警、关停、panic）靠 main 的 `installLogger` 把进程 logger 设成 slog 默认才是 `level=` 格式、受 log_level 约束；标准库 log 包转进来的输出按 WARN 记，log_level=warn 时不被吞（`logger_test.go`）。能拿到注入 logger 的地方优先用注入的。
- 包装 quic-go 的监听 socket 时必须保留批量与 GSO：任何只实现 `net.PacketConn` 的包装都会让 quic-go 退回逐包 ReadFrom / WriteTo（每 Gbps 多约一核）。测量口径与 harness 见 w9quic 报告（回环、私网目标放开）；生产默认拦私网时走 outbound 的带检查批量接口，GSO 消息同样逐条过 guard。

## 传输与订阅的对口

- gRPC 的 gun Hunk 帧（`grpc_stream.go`）与 Host / `:authority` 比较不比端口（`host_match.go` 的 `requestHostMatches`）都要和订阅渲染保持同一口径：写法与守卫测试见 subscription-render 规则文件。改了这两处，跑 subscription-e2e skill 实连一遍。

## 互操作测试门

- 三个构建标签都不进默认套件：`interop`（外部 Xray 的 xhttp/mkcp、sing-anytls 客户端）、`interop_mihomo`（需 `MIHOMO_BIN` 与 `MIHOMO_SHA256`）、`interop_external`（sing-box / Juicity / Naive，各需钉住哈希的外部二进制，`*_BIN` + `*_SHA256`）。
- CI 只以非 race 方式跑 `interop` 里的 Xray（XHTTP、VMess、REALITY+Vision）与 AnyTLS 两组：外部客户端自带数据竞争，Xray 在 -race 下还会崩在自己的 checkptr 上，不能进 race 套件（原因见 `anytls_client_interop_test.go` 文件头）。新加 Xray interop 测试要同步 `pandora-native.yml` 的 `-run` 正则。

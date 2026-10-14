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
- TCP 直连类 TLS 入站（Trojan、VLESS、VMess 的 tcp+tls 与 AnyTLS）每条连接握手用的 Web ALPN 派生配置走 `inbound_tls.go` 的 `inboundWebALPNConfig`：按 base `tls.Config` 的弱指针缓存，base 被回收时 `runtime.AddCleanup` 删条目（入站重建换证书即换新 base）。不要在握手路径上直接 `withInboundWebALPN`（每条连接一次 `Clone`）；交给握手之后不得再改 base 的字段。守卫 `TestInboundWebALPNConfigCacheHit`、`TestInboundWebALPNConfigAllocsOnCacheHit`、`TestInboundWebALPNCacheDroppedAfterBaseGC`。

## 认证失败的回落

- Trojan / AnyTLS / Naive 认证不过、或不是本协议客户端，一律交给 `probe_fallback*.go`：配了 `fallback` 由回落站点应答，没配是中性 404；不回 407、不带 realm。
- HTTP 承载的回落里，CONNECT 不走 `httputil.ReverseProxy`：Go 1.26.9 起它对 CONNECT 一律走 ErrorHandler（CVE-2026-56866），回成节点合成的 502 就是指纹。改由 `probeFallbackConnect` 发 `CONNECT /`：只转请求头、不带请求体（探测方在 CONNECT 后面带的字节一个也不进回落站点），专用 Transport 关 keep-alive，回落站点回 2xx 也只转这一个响应、不建隧道。守卫 `probe_resistance_connect_test.go`（h2 与 http/1.1 都带走私字节）。
- 本机 Go 1.27.1 早于这批修复（h1 服务端 CONNECT 后不关连接，会接着服务流水线请求）：跑这组测试用 `GOTOOLCHAIN=go1.26.9`，与 CI 同版本。
- Shadowsocks（旧版 AEAD）与 VMess 认证失败（含 salt / authID 重放、请求头没读全）不立刻关，返回 `drainAfterReport`，上报之后由 `drainUntilPeerClose`（`replay_filter.go`）读到对端关（排空），关闭发 FIN 而不是带着未读字节的 RST。约束：
  - 读满 `drainMaxBytes`（16MB）即关；读缓冲 2KB（`TestDrainMemoryPerConn`）。
  - 全进程同时排空的上限 `drainMaxConcurrentDefault`（4096），名额满时踢最老的一条放新的进来，不拒新：拒新会让占满名额的一方把别的认证失败逼回 RST（`replay_filter_drain.go` 的 `drainRegistry`，守卫 `TestDrainFullEvictsOldestWithFIN`、`TestDrainRegistryCountUnderChurn`）。
  - 同一来源（IPv4 单个地址、IPv6 /64，`drainSource`）同时排空至多 `drainMaxPerSourceDefault`（16）条，同样踢本来源最老的；同源上限先于全局上限生效（`TestDrainPerSourceCap`、`TestDrainSourceKey`）。
  - 每条排空总时限在 [5, 10) 分钟里随机（`drainMaxDurationDefault` 加 `drainJitterDefault`），与对端行为无关，到点发 FIN 关（`TestDrainMaxDurationClosesWithFIN`）。
  - 各协议的排空用例：`TestShadowsocks*DrainsUntilPeerClose`、`TestVMess*DrainsUntilPeerClose`。

## 防重放

- VMess 的 authID 用按时间分代的精确表 `replayFilter`（`replay_filter.go`）：整代丢弃，每代满 `replayFilterMaxPerGen` 提前换代。
- 旧版 Shadowsocks AEAD 的 TCP salt 用全进程共用的 `saltBloom`（`replay_filter_bloom.go`，误报 1e-6）：每 `ssSaltGenPeriod`（15 分钟）换一代、整代过期，一个 salt 至少记一代、至多两代；代内当前块写满就串一块翻倍的新块（不提前换代，爬坡时不丢已记的 salt），总容量超过两块最大块（约 7.2MB）才从最老的块起丢；整整两代没有写入全部丢掉。只记认证通过的 salt。SS2022 另有时间戳与精确表，不用 Bloom（SIP022）。守卫 `TestSaltBloom*`。
- 旧版 Shadowsocks AEAD 的 UDP 每个认证通过的包都查 salt，用单独一张同类型的表（`shadowsocks_udp_replay.go` 的 `sharedSSUDPSaltBloom`，块 1<<12 起、上限 1<<21，总量约 15MB）：UDP 每包一条，与 TCP 共用会把 TCP 的保留期挤短。1 万包/秒时保留约 7 分钟，估算写在常量注释里。

## 转发的空闲回收

- `core.Relay` 两个方向都没有数据超过 `runtime.connection_idle_seconds`（默认 1800 即 30 分钟，用户 10-08 定；与 Xray connIdle 同义但 Xray 默认 300；0 不回收）即断开，整段无数据的长连接（iperf3 的控制连接、不发保活的 SSH）也在此列。

## 连接失败观测链

- 适配器在失败点经 `connErrorReporter` 调 OnConnError；`reportAdapterConnError(Addr)` 统一跳过 `io.EOF`、`net.ErrClosed`、`context.Canceled`，不要在适配器里各自过滤。
- 日志里不落完整用户 IP 与凭据：`maskRemoteAddr` 把对端截到 IPv4 /24、IPv6 /48（与面板 `middleware.ByIPPrefix` 同粒度），`sanitizeConnErrorReason` 抹掉 UUID、IP、域名、长 hex/base64 并截断。用户 UUID 就是口令，日志只记内部 ID（`core/mieru` 同此）。
- `connerror_log.go` 按 (入站, 协议, 阶段, 分类) 固定窗口限流，锁内只计数、日志写在锁外。
- 新增协议要在 `connerror_hook_test.go` 或 `connerror_hook_client_test.go`（真实 QUIC / mieru 客户端）补至少一条失败路径真的到达 OnConnError。

## 在线设备与设备数限制

- 各适配器内嵌一份 `online_devices.go` 的 `onlineDevices`（零值可用），不要再各写 `map[用户]map[IP]struct{}`。按设备键记引用计数：同一设备多条连接只占一个名额，计数到 0 才离线；设备上限按不同设备数判。
- 设备键是 `core.DeviceKey`（用户 10-09 定）：IPv4 与 IPv4 映射地址按单个地址，IPv6 按 /64 网段（写成 `2001:db8:1:2::/64`）。OnlineIPs 上报的就是设备键；面板取哈希前再按同一口径归一（`panel/internal/module/node/service/online.go` 的 `deviceKey`），新旧节点混跑也不多算。两边各有一张用例表（pdnd `core/device_key_test.go` 与面板 `panel/internal/module/node/service/metrics_test.go` 的 `TestDeviceKey`），不互相读，改口径要两边一起改。mieru 的 `core/counter.OnlineTracker` 也用它。
- 写法固定为 `if !a.online.enter(user, ip) { 拒绝 }` 紧跟 `defer a.online.leave(user, ip)`，异常退出也经 defer 撤销。新增协议要进接线用例：TCP 进 `online_devices_wiring_test.go`（同 IP 两条关一条仍在线、全关才离线、别的设备占满名额时被拒），有 UDP 路径的再进 `online_devices_wiring_udp_test.go`。
- 例外：mieru 用 `core/counter.OnlineTracker`（最后活跃时间 + 5 分钟 TTL，不按连接计数）；ShadowTLS 委托内层适配器。

## 协议实现约定

- UDP 中继的上下行必须解耦，各一个 goroutine 阻塞读，收尾走 `udp_relay.go` 的 `relayUDPDirections`（SOCKS5、Trojan、VMess command=UDP 共用）。下行不能与上行在同一循环轮询，否则回程被限成几包每秒（`*DownlinkBurstWithoutUplink` 系列测试）。
- Shadowsocks AEAD 分块负载上限 0x3FFF（SIP004），读写两侧共用 `ssChunkLimit`；超长块规范客户端会断连（`shadowsocks_chunk_test.go`）。
- Shadowsocks（经典 AEAD 与 2022 方法）同一端口同时听 TCP 与 UDP，任一 bind 失败整体失败；配置带 `network` 键按配置错误拒绝（与面板的共用约定，面板 schema 不收 `network`）。端口登记因此占 `tcp+udp` 两个键（`port_claims.go` 的 `inboundPortKeys`：登记时全部检查、全部占上，任一冲突整体不启动，释放时全部释放；冲突文案写 `443/TCP+UDP`；单边 bind 撞上本机其他进程时报撞上的那一种，如 `443/UDP`）。面板 `protocol/port_l4_test.go` 的 `TestListenL4MatchesPdndPortClaims` 用正则读 `TestInboundPortKeyDerivation` 的用例表，行格式 `{"协议", nil 或 map[string]any{...}, "L4"}` 不要改。
- 经典 Shadowsocks 的 UDP 按 (客户端地址, 用户) 建会话（`shadowsocks_udp*.go`），不逐包处理：
  - 入站只有一个读循环，不按包起 goroutine，里面只做快路径：同地址先用该会话的用户试解（用户记录 retired、会话 ctx 已取消就跳过），明文写进读循环独占的缓冲（GCM 失败会清零 dst，不能原地试解），上行同步写上游（非阻塞 UDP 写）。解不开或该地址没有会话的包复制进 sing 的池化缓冲，交给本入站的慢路径 worker 池（`shadowsocks_udp_pool.go`：GOMAXPROCS 个 worker、队列 `ssUDPSlowQueue` 256，满了丢）按 `findSSUser` 逐用户试解。灌垃圾包只占满这个池，已建会话不受影响（`TestShadowsocksUDPGarbageFloodKeepsEstablishedSessions`）；代价是被灌时新会话的首包可能被丢、靠客户端重发。
  - 认证通过的包每个都查 salt（`shadowsocks_udp_replay.go`，UDP 单独一张 `saltBloom`，块上限 1<<21、总量约 15MB，容量估算写在常量注释里），重放的包不转发、不建会话（`TestShadowsocksUDPReplayedPacketDropped`）。
  - 建会话只登记、不做 I/O：`getOrOpenUDPSession` 全程持 udpMu（查已有 → 名额 → 在线设备 → 用户会话表 → 入表），读循环与 worker 并发处理同一 (地址, 用户) 也只建一个。上游（`ListenUDP`，中转出站要握手、域名要解析）在会话自己的 goroutine 里建（`establishUDPSession`），建立期间到的包进会话的小队列（`ssUDPPendingMax` 8 个，满了丢），建好后按序发出；失败只走 `finishUDPSession` 一处收尾，名额、设备、用户会话表全部归还。守卫 `shadowsocks_udp_establish_test.go`（一个用户的 ListenUDP 卡住时别的用户照常往返、队列上限、失败归还）。
  - 每会话一个上游 socket 和一个下行 goroutine；下行直接调用 hy2 的 `hy2DownlinkUDP`（冷态不占收包缓冲、共用存货与名额），交给它的「客户端连接」`ssUDPClientWriter`（`shadowsocks_udp_downlink.go`）在 WritePacket 里按真实来源地址加密（IP 按 ATYP 1/4，中转给的域名来源按 ATYP 3）、写回入站 socket，编码缓冲从池里借、返回前还。WritePacket 的错误会让 hy2DownlinkUDP 结束整个会话，所以只有 ctx 取消或 `net.ErrClosed` 才返回错误；编码失败、超出桶容量、ENOBUFS 之类只丢这个包（计 `udpDrops.downlink`）。
  - 路由：出站按首包目标选（建上游时记下 tag）；之后目标变化时按新目标再 `Select` 一次（`shadowsocks_udp_route.go` 的 `selectUDPRoute`：DataPlane 接口没有查询，对 `*routedDataPlane` 读 current 的 Runtime，对实现了 `Select` 的测试替身直接调），结果与会话的 tag 不一致（含 block）就丢这个包，不换上游；同一目标连续发包只查第一次。私网拦截仍由直连上游逐包判定。守卫 `TestShadowsocksUDPRouteCheckedPerDestination`、`TestShadowsocksUDPPrivateTargetDroppedAfterPublicFirst`。
  - 空闲只按客户端上行判断（下行不续期，上游一直推包也照样回收）：上行空闲 `ssUDPIdleTimeout`（5 分钟，与 juicity、UoT 同值）由 `udpJanitor` 关会话；在线设备在会话建立时 enter，上行空闲 `ssUDPDeviceIdleTimeout`（60 秒）先 leave（会话保留、下行照常），再有上行时重新 enter，被拒就关会话（设备数受限的用户切网络后，旧地址至多再占一分钟）；收尾在 `finishUDPSession` 里撤。删用户经 `userSessions.revoke` 踢；入站 Close 全关；名额按 `udpSessionsPerUser`。守卫 `TestShadowsocksUDPDownlinkOnlyReleasesDevice`、`TestShadowsocksUDPDownlinkOnlySessionReclaimed`。
  - 限速：同一用户的 TCP、UDP 下行在同一个桶上 `WaitN`（先预支、桶变负数再睡）；UDP 上行不能睡（读循环与 worker 全入站共用），用 `ReserveN`，要等不超过 `ssUDPUplinkMaxDebt`（50ms）就记债立即放行，超过才 `CancelAt` 并丢包。不要改回 `AllowN`：下行跑满时桶一直在 0 以下，上行（DNS、QUIC 的 ACK）几乎全丢（`TestShadowsocksUDPUplinkPassesWhileDownlinkSaturated`）。
  - 域名目标按会话缓存最近一次解析（`ssUDPResolveTTL`，与 hy2 同值），没命中时另起 goroutine 解析、同一会话同时只解一个，期间的包丢弃，触发解析的那个包解析完补发（`TestShadowsocksUDPDomainResolve`）。
  - 丢包按原因计在 `a.udpDrops`（pending、queue、rate、route、replay、downlink），只作诊断与测试，不上报。
  - 其余守卫：`TestOnlineDevicesWiringSSUDP`、`TestShadowsocksSamePortTCPAndUDPInterop`、`TestShadowsocksUDPDownlinkBurstWithoutUplink`、`TestShadowsocksIdleUDPSessionHeap`（1000 个空闲会话每会话存活堆 ≤16KB）、`shadowsocks_udp_lifecycle_test.go`（上限、空闲回收、删用户、关入站不留 goroutine）、`shadowsocks_udp_gaps_test.go`（NAT 后两个用户不串、换用户记录换会话与限速）。SS2022 的 UDP 仍逐包处理，留给 SS2022 多用户重写（`shadowsocks_udp.go` 末尾三个函数只剩它在用，重写时一起挪走）。
- VLESS / VMess 的 mux（XUDP）在 NativeCore 内实现。
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
- hy2 / TUIC 入站起来后检查 UDP 收发缓冲（`quic_socket_linux.go`）：pdnd 无 CAP_NET_ADMIN，受 `net.core.rmem_max / wmem_max` 限制拿不到 quic-go 要的 8MB，不足一半就 Warn 一次并提示 sysctl。上限由面板的节点安装脚本写进 `/etc/sysctl.d/90-pandora-native.conf`（16MB，面板 `panel/internal/module/node/service/pdnd_install.sh.tmpl` 的系统参数段），手工安装见 `release/README.md`；不要给 unit 加 CAP_NET_ADMIN。
- UDP 转发的出站 socket（`newHy2UDPUpstream`）固定申请 `hy2UDPSocketBuffer`（106496，内核记账与读回 212992，即常见发行版的 rmem_default、官方 hysteria 与 sing-box 不调时的大小）。rmem_max 8MB 档下申请 4MB 让 hy2 UDP 下行输官方 hysteria 5–6 个百分点，只改这一项即拉平；持续过载时大缓冲只多收注定在 QUIC 发送侧被丢的包。数据与理由写在常量注释里，改值要带同口径的复测数据。上游 socket 每会话一个，固定申请也让每会话的 udp_mem 记账不随机器的 rmem_default 膨胀。默认拦私网时经 `outbound.UDPBatchConn` 调缓冲，不拿裸 socket。
- 空闲的 hy2 / TUIC UDP 会话不常驻收发缓冲。
  - 下行（`hysteria2_udp_downlink.go`）：冷态经 `internal/udprecv` 的收包器等 socket 可读，等待期间不占缓冲：`RawConn.Read` 的第一次回调只用 1 字节 MSG_PEEK 看有没有包、不借组，没有就交给 netpoller 等；叫醒后才借 2 包小组、非阻塞 recvmmsg。每次醒来「窥视落空 + 收到」两次系统调用、借还一次，与持有缓冲阻塞读（sing-box）同形状。
    - 冷态借小组先占冷态名额 `hy2DownlinkColdSlots`（16×GOMAXPROCS，4 核 64 组 8MB）：借到组就被调度挂起的会话会偶发地一下堆到几百个（VPC 1024 冷会话：250–720 组，RSS +60–120MB），靠减少借还压不住。冷态小组还组前不等 I/O（两包总能整包复制，写回在还组之后），等名额的会话不会被卡住的写回连累。这个前提由 `hysteria2_udp_downlink.go` 的编译期断言钉住（预算 ≥ 小组包数 × `hy2UDPMaxDatagram`，单包上限是 ≥64 的 2 的幂），运行期守卫 `TestHy2ProbeGroupAlwaysFullyCopied`、`TestHy2DownlinkColdSlotsSurviveStuckWriteback`（名额 +8 个会话卡在写回时名额全空、别的会话照常写回），名额上限守卫 `TestHy2DownlinkColdSlotsBoundProbeGroups`。等名额不带 ctx、发生在 `RawConn.Read` 回调里，正因为持有方不等 I/O 才有界，别为此去掉断言。热态持组不占冷态名额（另有热态名额）。
    - 第一次回调就借组收（落空再还）：每次醒来借还两次，VPC 1024 冷会话时存货表排队、小组存货涨到 450–514 组（RSS +70MB），守卫 `internal/udprecv` 的 `TestReadyHoldsNoBatchWhileWaiting`（等待期间借 0 次、一次醒来借 1 次）。
    - 「上次收空了就跳过第一次回调直接等」会漏包：Go 的 netpoller 在 `RawConn.Read` 开始时清掉就绪标记（`poll_runtime_pollReset`），之前到的包不再触发（Linux 实测一轮 120 包只收到 85 包）。
    - 不要改回「阻塞窥视等包、再借缓冲收」（每次醒来三次，每 Gbps 多约 10% CPU），守卫 `TestHy2DownlinkRecvSyscallsPerPacket`（真 socket 数 `Receiver.Recvs`，冷态与中速每包 ≤2.05）。不要在回调里嵌套 x/net 的 ReadBatch（同一个 fd 的读锁，死锁）。Linux 上窥视也用 recvmmsg（vlen 1、1 字节、MSG_PEEK），不用 recvfrom：linux/386 没有 `SYS_RECVFROM`；CI 的 linux-race job 有「Cross-platform build and vet」一步钉住 linux/386、linux/arm、darwin、freebsd 的 vet 与 windows 的 build。来源地址有意不带 IPv6 zone（改前的 x/net 路径同样丢，理由写在 `udprecv.addrPort`）。
    - 醒来后小组连收两次都满才借 32 包批量组（三五包的小串不占 2MB，守卫 `TestHy2DownlinkSmallBurstStaysOnProbeGroup`）。冷态每次收完先把包复制成按长度分配的小缓冲、归还收包组再写回：写回在 QUIC 的 DATAGRAM 队列满时阻塞，零拷贝时每个卡住的会话占着 128KB；复制按实际占用的块封顶 `hy2DownlinkCopyBudget`（128KB）：sing 的 `buf.NewSize` 按 2 的幂分档（64B 起，到 64KB），`Cap()` 报的是请求长度，记账用 `hy2CopyBlockSize`（4097×35、16385×11 两档守着）。一次收到的大包超出上限时，剩下的占着收包组零拷贝写回，写完才归还收包组（批量组连同名额与份额，守卫 `TestHy2DownlinkLargeOverflowIsolation`），小组与批量组走同一段代码。每个卡住的冷态会话的常驻上限：hy2 单包 ≤4096，32 包恰好 128KB，复制不会超出；TUIC 单包可到 0xffff，复制 ≤128KB，另有全进程至多「批量名额」个会话各占一个 2MB 批量组（守卫 `TestHy2DownlinkStuckLargePacketsBounded`、`TestHy2BlockedUDPSessionHeap`）。冷态每秒不到 500 包，复制开销可忽略，热态仍零拷贝。
  - 进出热态用同一把尺子：一段 `hy2DownlinkWarmIdle`（100ms）的平均速率到每秒 500 包（`hy2DownlinkWarmMinPackets` 50 包）。段长 20ms 时段的起点落在包上，音频（每 20ms 一包）加视频（每 33ms 一串 12 包，合计约每秒 410 包）常有一段折算超过每秒 500 包，名额被占满（实测平均 62/64；「连续两段 20ms 都够」仍有 20/64）。守卫 `TestHy2DownlinkMixedAVStaysCold`、`TestHy2DownlinkPacedBurstStaysCold`、`TestHy2DownlinkPairedLowRateStaysCold`、`TestHy2DownlinkStaleColdCountDoesNotWarm`。热态省的是借还与复制（零拷贝），系统调用次数与冷态相同。
  - 热态名额 `hy2WarmLimit`（全进程一份 `hy2DownlinkWarm`，64×GOMAXPROCS 个，每用户 1/4）由会话带着（`hy2Downlink.warmLimit`）：同时处在热态的会话有上限，热态小组的常驻（含还回后留在存货里的）才有界；占不到的会话照常冷态收包。测试注入自己的名额，不替换全局变量（替换时别的会话还占着旧名额，归还会永远阻塞）。守卫 `TestHy2DownlinkWarmSlotsCapped`；生产入口 `hy2DownlinkUDP` 必须用共享的那一份，守卫 `TestHy2DownlinkUDPUsesSharedWarmLimit`。
  - 每次改读截止后都再看一次 ctx：回冷态清截止会盖掉收尾时 AfterFunc 刚设的截止，只能靠这次复查结束（守卫 `TestHy2DownlinkClearDeadlineRacesCancel`）。非阻塞收报读截止到点不是会话结束（`TestHy2DownlinkDeadlineDuringDrainKeepsSession`；真 socket 上非阻塞收走 `RawConn.Control`、不看截止，守的是处理）。上行用 `WaitReadPacket` 零拷贝等首包，凑批才借缓冲。
  - 借还走存货表（`hysteria2_udp_stock.go` 的 `hy2Stock`）：借时有就拿、没有才新分配，还时一律收下，闲置后由后台回收，回收时长每张表各自定：下行收包组 5 秒（组大，高峰过后不久留），上行凑批缓冲 30 秒（本机探针：5 秒时每 7 秒一阵的上行每阵都重新分配，TotalAlloc 多 2–3 倍；上行组逐条分配、高峰占用小）。守卫 `TestHy2StockJanitorPerStockIdle`、`TestHy2StockTrimKeepsRecent`。不用 sync.Pool（按 P 存放、换 P 拿不回，GC 清一半），也不用定长空闲表（表满即丢、表空即分配，审查探针里 2 秒新分配 2425 组）。守卫 `TestHy2DownlinkBuffersNotReallocated`。
  - 批量组只在占到名额时借：全局 `hy2DownlinkBatchSlots`（8×GOMAXPROCS 组），每用户最多 1/4（`hy2DownlinkBatchPerUser`，`relayHy2UDP` 按 userID 认人）。写回在 QUIC 的 DATAGRAM 发送队列满时阻塞，借来的缓冲占到返回为止；名额或份额不够时用小组收，每个卡住的会话最多 128KB。守卫 `TestHy2DownlinkPerUserShare`、`TestHy2DownlinkSlotsExhaustedOthersStillForward`、`TestHy2DownlinkSlotReturnedOnPanic`。
  - 共享缓冲的前提：nativewire 的 hy2 / TUIC `WritePacket` 返回前同步编码并复制（`SendDatagram` 自带复制），返回后不再引用负载。改成异步持有负载，借来的缓冲还回后会被别的会话（别的用户）覆盖，等于串数据。守卫 `hysteria2_udp_writecopy_test.go`（经真客户端，WritePacket 返回后涂掉缓冲、客户端仍收到原文）与 `TestHy2DownlinkCrossSessionIsolation`。
  - 其余守卫：`hysteria2_udp_mem_test.go`（空闲、活跃、卡在写回的存活堆）、`hysteria2_udp_recv_test.go`（真 socket 逐字节、IPv4 / IPv6 来源、收尾、不留 goroutine）、`TestHy2DownlinkMediumRateTwoRecvsPerPacket`。`hysteria2_udp_share_test.go`、`hysteria2_udp_warm_test.go` 用上游替身（按 udprecv 的语义实现 `Ready` / `Recv`：先窥视、空就等，`calls` 按系统调用计，窥视也算），一次收包能给多包，批量组、名额与热态在 darwin 上也走得到；真 socket 的用例在 darwin 上一次只收一包（recvfrom），recvmmsg 与批量路径只在 Linux CI 上走到。
  - 下行收包器不分协议族（IPv6 上游同样就绪收、在 Linux 上 recvmmsg 批量收）；默认拦私网时收包器由 `outbound.UDPBatchConn.Receiver()` 给出，只能收，裸 socket 不出 outbound 包；上行批量发送仍只在 IPv4 本地地址时。加密、封装类出站只有 `net.PacketConn`，下行退回阻塞 ReadFrom、常驻 64KB。
- hy2 / TUIC 与经典 Shadowsocks 每用户在途 UDP 会话上限 `udpSessionsPerUser`（1024，`udp_session_quota.go`），挡 fd 与 udp_mem 被单个用户耗尽；超出拒新不踢旧，走 OnConnError 的 limit 分类。
  - Juicity 每个 UDP 目标一条路由（一个上游 socket），按同一上限计；路由没有上行也没有下行 `juicityUDPIdleTimeout`（5 分钟）即回收、归还名额（`juicity_udp.go`，守卫 `TestJuicityUDPRouteIdleReclaim`）。
  - AnyTLS 的 UoT 一个目标算一个会话，该用户在同一入站上的全部 UoT 流共用名额；超额或发往某个目标失败只丢那个包、经 onDrop 记一条，不关流；目标空闲 `uotUpstreamIdleTimeout`（5 分钟）回收（`uot_bridge.go`）。名额与该目标的 socket、读 goroutine、读缓冲同时释放：只有把目标移出表的一方归还名额，读 goroutine 靠 done 退出，客户端不读下行时也不滞留。守卫 `TestUOTTargetsBoundedByUserUDPQuota`、`TestUOTQuotaSharedAcrossStreamsOfOneUser`、`TestUOTReclaimedTargetsReleaseReaderWhenConsumerStalls`、`TestUOTIdleTargetsReclaimed`。
- Hysteria2 认证前的约束在 `internal/nativewire/hysteria2/service.go`（fork 改动明细见 pdnd-forks 规则文件）：认证前每条请求流读请求头限时 10 秒（`DefaultPreAuthHeaderTimeout`，超时只拒那条流，不关连接）；未认证且没有在途请求的连接空闲 75 秒关（`DefaultPreAuthIdleTimeout`，取 nginx keepalive_timeout，有请求在途不计时）；认证前在途请求至多 32 条（`preAuthMaxInflight`，多出的 H3_REQUEST_REJECTED）；HEADERS 声明长度超过 32KB 拒（`preAuthMaxHeaderBytes`，H3_EXCESSIVE_LOAD）。没有「握手后 10 秒未认证即关」：那样会误杀伪装站点的正常长连接（`TestPreAuthActiveMasqueradeConnSurvives`）。其余守卫 `preauth_test.go`、`preauth_idle_test.go`。
- `internal/nativewire` 的 hy2 / TUIC 服务端在 QUIC 连接断开时关掉其上全部 UDP 会话（`closeUDPSessions`）：会话 ctx 来自服务而非连接，不关要等 udpTimeout（5 分钟）才收尾，期间占着上游 socket、在线设备与会话名额（`TestOnlineDevicesWiringUDP` 守着）；置 `udpClosed` 与拷列表在同一把 `udpAccess` 里，之后到的包不再建会话（TUIC quic 中继模式会在单向流 goroutine 里建，`TestNoUDPSessionAfterClose`）。
- 内核里直接用包级 `slog.*` 的日志（缓冲告警、关停、panic）靠 main 的 `installLogger` 把进程 logger 设成 slog 默认才是 `level=` 格式、受 log_level 约束；标准库 log 包转进来的输出按 WARN 记，log_level=warn 时不被吞（`logger_test.go`）。能拿到注入 logger 的地方优先用注入的。
- 包装 quic-go 的监听 socket 时必须保留批量与 GSO：任何只实现 `net.PacketConn` 的包装都会让 quic-go 退回逐包 ReadFrom / WriteTo（每 Gbps 多约一核）。测量口径与 harness 见 w9quic 报告（回环、私网目标放开）；生产默认拦私网时走 outbound 的带检查批量接口，GSO 消息同样逐条过 guard。

## 传输与订阅的对口

- gRPC 的 gun Hunk 帧（`grpc_stream.go`）与 Host / `:authority` 比较不比端口（`host_match.go` 的 `requestHostMatches`）都要和订阅渲染保持同一口径：写法与守卫测试见 subscription-render 规则文件。改了这两处，跑 subscription-e2e skill 实连一遍。

## 互操作测试门

- 三个构建标签都不进默认套件：`interop`（外部 Xray 的 xhttp/mkcp、sing-anytls 客户端）、`interop_mihomo`（需 `MIHOMO_BIN` 与 `MIHOMO_SHA256`）、`interop_external`（sing-box / Juicity / Naive，各需钉住哈希的外部二进制，`*_BIN` + `*_SHA256`）。
- CI 只以非 race 方式跑 `interop` 里的 Xray（XHTTP、VMess、REALITY+Vision）与 AnyTLS 两组：外部客户端自带数据竞争，Xray 在 -race 下还会崩在自己的 checkptr 上，不能进 race 套件（原因见 `anytls_client_interop_test.go` 文件头）。新加 Xray interop 测试要同步 `pandora-native.yml` 的 `-run` 正则。

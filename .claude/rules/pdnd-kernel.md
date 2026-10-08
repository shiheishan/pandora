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

## 连接失败观测链

- 适配器在失败点经 `connErrorReporter` 调 OnConnError；`reportAdapterConnError(Addr)` 统一跳过 `io.EOF`、`net.ErrClosed`、`context.Canceled`，不要在适配器里各自过滤。
- 日志里不落完整用户 IP 与凭据：`maskRemoteAddr` 把对端截到 IPv4 /24、IPv6 /48（与面板 `middleware.ByIPPrefix` 同粒度），`sanitizeConnErrorReason` 抹掉 UUID、IP、域名、长 hex/base64 并截断。用户 UUID 就是口令，日志只记内部 ID（`core/mieru` 同此）。
- `connerror_log.go` 按 (入站, 协议, 阶段, 分类) 固定窗口限流，锁内只计数、日志写在锁外。
- 新增协议要在 `connerror_hook_test.go` 或 `connerror_hook_client_test.go`（真实 QUIC / mieru 客户端）补至少一条失败路径真的到达 OnConnError。

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
- hy2 / TUIC 入站起来后检查 UDP 收发缓冲（`quic_socket_linux.go`）：pdnd 无 CAP_NET_ADMIN，受 `net.core.rmem_max / wmem_max` 限制拿不到 quic-go 要的 8MB，不足一半就 Warn 一次并提示 sysctl。
- 包装 quic-go 的监听 socket 时必须保留批量与 GSO：任何只实现 `net.PacketConn` 的包装都会让 quic-go 退回逐包 ReadFrom / WriteTo（每 Gbps 多约一核）。测量口径与 harness 见 w9quic 报告（回环、私网目标放开）；生产默认拦私网时走 outbound 的带检查批量接口，GSO 消息同样逐条过 guard。

## 传输与订阅的对口

- gRPC 的 gun Hunk 帧（`grpc_stream.go`）与 Host / `:authority` 比较不比端口（`host_match.go` 的 `requestHostMatches`）都要和订阅渲染保持同一口径：写法与守卫测试见 subscription-render 规则文件。改了这两处，跑 subscription-e2e skill 实连一遍。

## 互操作测试门

- 三个构建标签都不进默认套件：`interop`（外部 Xray 的 xhttp/mkcp、sing-anytls 客户端）、`interop_mihomo`（需 `MIHOMO_BIN` 与 `MIHOMO_SHA256`）、`interop_external`（sing-box / Juicity / Naive，各需钉住哈希的外部二进制，`*_BIN` + `*_SHA256`）。
- CI 只以非 race 方式跑 `interop` 里的 Xray（XHTTP、VMess、REALITY+Vision）与 AnyTLS 两组：外部客户端自带数据竞争，Xray 在 -race 下还会崩在自己的 checkptr 上，不能进 race 套件（原因见 `anytls_client_interop_test.go` 文件头）。新加 Xray interop 测试要同步 `pandora-native.yml` 的 `-run` 正则。

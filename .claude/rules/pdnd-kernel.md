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

## 互操作测试门

- 三个构建标签都不进默认套件：`interop`（外部 Xray 的 xhttp/mkcp、sing-anytls 客户端）、`interop_mihomo`（需 `MIHOMO_BIN` 与 `MIHOMO_SHA256`）、`interop_external`（sing-box / Juicity / Naive，各需钉住哈希的外部二进制，`*_BIN` + `*_SHA256`）。
- CI 只以非 race 方式跑 `interop` 里的 Xray XHTTP 与 AnyTLS 两组：外部客户端自带数据竞争，不能进 race 套件（原因见 `anytls_client_interop_test.go` 文件头）。

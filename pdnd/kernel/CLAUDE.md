# pdnd/kernel/
> L2 | 父级: /pdnd/CLAUDE.md

NativeCore：协议无关的数据面运行时。每个入站拥有一个 route engine 和一代 outbound；协议以 Adapter 注册进 AdapterRegistry，通过 DataPlane 拨号与监听，流量在此按用户计量。能力矩阵是唯一事实源：--capabilities 打印它，selfcheck 校验它，面板拿它做编排前协商。未在矩阵里的组合 fail closed，不回落到 core/ 的兼容内核。

成员清单
runtime.go: 包文档所在。协议无关运行时，持有每入站的 route engine 与 outbound 代
nativecore.go: NativeCore 与 nativeInbound：按 InboundSpec 启动入站，routedDataPlane 把 DialTCP/ListenUDP 接到路由与出站，swap 支持不重启的热更新
adapter.go: 协议适配器契约：InboundSpec、DataPlane、AdapterHooks、ConnError、Adapter/AdapterFactory/AdapterRegistry
capabilities.go: 能力矩阵：Capability、NativeCapabilities、NativeCapabilityFor、NativeProtocolNames、NativeCapabilityReport(For)；CI capability smoke 按字面量守 reality-h3-experimental 与 external-reality-xhttp-h3-unverified
selfcheck.go: 启动自检：ValidateNativeCapabilityMatrix 对照默认注册表，不开监听
proxy.go: socks 与 http 入站共用的 proxyAdapter：SOCKS4/4a/5 CONNECT 与 UDP ASSOCIATE、HTTP CONNECT 与正向 GET，认证绑定面板下发的用户 UUID
vless.go: VLESS 入站主体：请求头解析、TCP 转发、传输分派
vless_flow.go: VLESS addons 编解码与 flow 协商
vless_mux.go: 原生 XUDP/mux 帧，刻意留在 NativeCore 内而不委托兼容内核
vless_udp.go: VLESS command=UDP：两字节长度帧的数据报经 DataPlane 路由并计量
vless_xhttp_packet.go: VLESS 在 XHTTP packet 模式下的收发
vision.go: XTLS Vision：VisionConn 与 padding/直通状态机
vmess.go: VMess 入站：原生 gRPC（h2c、TLS+h2）、XHTTP stream 与 packet-up/reconnect
vmess_xhttp_packet.go: VMess 在 XHTTP packet 模式下的收发
trojan.go: Trojan 入站 TCP 路径
trojan_udp.go: Trojan UDP ASSOCIATE：地址/长度/CRLF 帧转发并计量
shadowsocks.go: Shadowsocks AEAD 入站
shadowsocks2022.go: Shadowsocks 2022 入站
shadowtls.go: ShadowTLS 组合入站
hysteria2.go: Hysteria2 入站（QUIC）
tuic.go: TUIC 入站（QUIC）
juicity.go: Juicity 原生 QUIC/认证/TCP/UDP 数据面
anytls.go: AnyTLS 入站
naive.go: Naive 入站
mieru.go: mieru 接入 NativeCore 的薄层（TCP/UDP）
reality.go: REALITY 服务端配置解析：dest、xver、短 ID、私钥、超时
reality_listener.go: REALITY 监听器与会话上下文，握手错误上报钩子
reality_client.go: REALITY 客户端配置，供自检与探针
inbound_tls.go: 普通 TLS 入站证书加载
xhttp.go: XHTTP 配置与模式解析，请求元数据编解码
xhttp_server.go: XHTTP 会话与双工连接，HTTP/1、2、3 承载
xhttp_packet.go: XHTTP packet 队列：乱序入队、ResumeFrom 重连续传
grpc_stream.go: gRPC 双工流连接，接受 gzip 请求、以 identity 帧响应
websocket_netconn.go: WebSocket 承载的 net.Conn
httpupgrade_netconn.go: HTTP Upgrade 承载的 net.Conn
native_transport_server.go: WebSocket 与 HTTP Upgrade 的服务端分派
mkcp_transport.go: mKCP 传输：配置与掩码解析、监听、MTU 校验、socket 缓冲调优
uot_bridge.go: UDP-over-TCP 桥：把 uot 数据报接到路由后的 PacketConn
*_test.go: 各协议单测，nativecore_test.go 跨协议契约测试，-tags interop 的非 race 互操作门：外部 Xray/mihomo 客户端，以及 anytls_client_interop_test.go（sing-anytls v0.0.11/v0.0.13 客户端内部自带 closeLocally/Write 数据竞争，移出默认 race 套件）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

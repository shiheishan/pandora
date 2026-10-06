# pdnd/internal/nativewire/shadowtls/
> L2 | 父级: /pdnd/CLAUDE.md

ShadowTLS 线格式，fork 自 sagernet/sing-shadowtls（GPL-3.0，见 LICENSE）
  - 唯一生产调用方是 kernel/shadowtls.go，只走 v3；v1/v2 与客户端保留给测试与对拍，不对外暴露
  - 本仓库在 service.go 加了判定前限时（HandshakeTimeout）；文件划分保持上游原样，以便对照上游
  - 限时原则：认证判定之前等对端的步骤按 HandshakeTimeout 超时；回落诱饵与对外看起来就是诱饵会话的中继一律不限时，否则 10 秒掐断空闲 TLS 会话本身就是指纹

成员清单
service.go: 服务端入口 Service.NewConnection，按版本分派伪装握手；v1 整段握手限时，v2/v3 只限时读 ClientHello，v3 校验通过后与诱饵交换 ServerHello 也限时（relayServerHello），之后的诱饵中继与等首个 HMAC 帧不限时；缺省超时 DefaultHandshakeTimeout 只是兜底，kernel 显式传 inboundHandshakeTimeout
v1_server.go: v1 握手中继状态机，双向逐记录转发直到 ChangeCipherSpec 之后一帧（只认 TLS 1.2）
v2_server.go: v2 握手中继，逐帧比对应用数据前 8 字节与诱饵下行哈希，匹配即认证，超过两帧未匹配判回落
v2_hash.go: v2 的 HMAC 读写包装，服务端对诱饵下行记哈希，客户端对读入记哈希
v2_conn.go: v2 认证后的数据连接，按 TLS 应用数据帧封包与拆包
v2_client.go: v2 客户端数据连接，首帧带握手哈希
v3_server.go: v3 帧工具与握手中继：extractFrame 读一个 TLS 记录、ClientHello 的 SNI 提取与 session ID HMAC 校验、ServerRandom 提取、双向逐帧中继（客户端侧等首个 HMAC 帧，诱饵侧改写应用数据）
v3_conn.go: v3 认证后的数据连接 verifiedConn，每帧带 4 字节 HMAC，校验失败发 alert
v3_client.go: v3 客户端：在 ClientHello 的 session ID 里嵌 HMAC，读 ServerHello 取 ServerRandom 并校验服务端改写帧
v3_constrat.go: TLS 记录与握手常量、各字段下标
client.go: 客户端入口 Client.DialContext / DialContextConn，三版本共用
tls_wrapper.go: 客户端 TLS 握手函数类型与基于 utls 的缺省实现（v3 经它注入 session ID 生成器）
service_test.go: 判定前限时的回归：三版本静默客户端按超时返回 os.ErrDeadlineExceeded，v3 诱饵不回 ServerHello 也超时；v2/v3 回落中继、v1 握手后交给 Handler 的连接静置超过超时仍可双向收发
README.md / LICENSE / Makefile: 上游原样保留

法则: 成员完整·一行一文件·父级链接·技术词前置

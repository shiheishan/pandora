# pdnd/core/
> L2 | 父级: /pdnd/CLAUDE.md

core.go 定义 core.Core 抽象（入站生命周期、UpsertUsers 用户热更新、流量读取），kernel/ 的 NativeCore 与这里的兼容适配器都实现它
  - multi/ 是过渡期分派器，生产 NativeCore-only 路径不经过它
  - sing/ 与 xray/ 只在 -tags compat 构建里被链接。

成员清单
core.go: Core 接口与入站配置抽象，UpsertUsers 热更新契约
ratelimit.go: 按用户限速：SpeedLimiters 注册表、SpeedLimitedConn 包装 net.Conn，字节/秒与 burst 参数
counter/counter.go: 按用户流量统计、用户表与在线 IP 记录
multi/multi.go: 过渡期 core.Core 分派器，把多个内核组合成一个
sing/: 基于 sing-box 的内核层
  - managed.go 生命周期与包文档，sing.go 装配，routing.go 出站与分流翻译
  - anytls/hysteria2/shadowsocks/shadowtls/socks/trojan/vless/vmess 各协议入站构造
  - naive.go + naive_conn.go padding 连接 + naive_masquerade.go 伪装回落
  - quic.go TUIC 与 Hysteria v1
xray/: 基于 xray-core 的内核层。xray.go 生命周期，registry.go 模块注册，protocols.go 协议配置构造，routing.go 出站与分流翻译
mieru/mieru.go: mieru 协议入站。默认构建里也被链接：kernel/mieru.go 把它包成 NativeCore 适配器，经 SetTransport 注入的 Transport 让出站走 NativeCore 的 DataPlane 路由，经 SetConnErrorHandler 把已认证连接的失败（ErrUserRevoked / ErrDeviceLimit / 拨号）交给 OnConnError；未注入时（compat 构建经 core/multi）才直接拨号。日志只记用户内部 ID，不记作口令用的 UUID
mieru/listener.go: mieru TCP 监听的 Accept 韧性层。上游 mux 的 Accept 循环遇任何错误即 break（EMFILE 一次入站就永久不再接客），Start 经上游注入点 Mux.SetStreamListenerFactory 把底层监听包成 retryListener：非关闭错误按 5ms 起翻倍、封顶 1s 退避重试（与 kernel/accept_loop.go 同参，因 kernel 依赖本包只能各写一份），只在关闭后把错误交回上游；Close 立刻唤醒退避中的 Accept；一段连续失败只记一次 Warn
external/juicity.go: 以独立进程托管的 juicity（AGPL，按许可证只能进程外运行、不能按用户计流量）
  - 配置目录缺省 DefaultJuicityWorkDir = /var/lib/pandora-native/juicity，落在 pandora-native.service 的可写目录里（ReadWritePaths 只有 /var/lib/pandora-native 与 /var/log/pandora-native）
*_test.go: 适配器契约测试随包放置；mieru/accept_retry_test.go 用会报 EMFILE 的监听器跑真实 mieru 客户端端到端，钉住入站恢复接客、不空转、Close 及时；external/juicity_test.go 钉住 juicity 缺省目录在 systemd 单元的 ReadWritePaths 之内

法则: 成员完整·一行一文件·父级链接·技术词前置

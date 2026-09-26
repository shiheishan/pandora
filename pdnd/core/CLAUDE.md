# pdnd/core/
> L2 | 父级: /pdnd/CLAUDE.md

core.go 定义 core.Core 抽象（入站生命周期、UpsertUsers 用户热更新、流量读取），kernel/ 的 NativeCore 与这里的兼容适配器都实现它。multi/ 是过渡期分派器，生产 NativeCore-only 路径不经过它；sing/ 与 xray/ 只在 -tags compat 构建里被链接。目录源自已删除的旧版 nodeagent/core。

成员清单
core.go: Core 接口与入站配置抽象，UpsertUsers 热更新契约
ratelimit.go: 按用户限速：SpeedLimiters 注册表、SpeedLimitedConn 包装 net.Conn，字节/秒与 burst 参数
counter/counter.go: 按用户流量统计、用户表与在线 IP 记录
multi/multi.go: 过渡期 core.Core 分派器，把多个内核组合成一个
sing/: 基于 sing-box 的内核层。managed.go 生命周期与包文档，sing.go 装配，routing.go 出站与分流翻译；anytls/hysteria2/shadowsocks/shadowtls/socks/trojan/vless/vmess 各协议入站构造；naive.go + naive_conn.go padding 连接 + naive_masquerade.go 伪装回落；quic.go TUIC 与 Hysteria v1
xray/: 基于 xray-core 的内核层。xray.go 生命周期，registry.go 模块注册，protocols.go 协议配置构造，routing.go 出站与分流翻译
mieru/mieru.go: mieru 协议入站
external/juicity.go: 以独立进程托管的 juicity（AGPL，按许可证只能进程外运行、不能按用户计流量）；配置目录缺省 DefaultJuicityWorkDir = /var/lib/pandora-native/juicity，落在 pandora-native.service 唯一可写的状态目录里
*_test.go: 适配器契约测试随包放置；external/juicity_test.go 钉住 juicity 缺省目录在 systemd 单元的 ReadWritePaths 之内

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

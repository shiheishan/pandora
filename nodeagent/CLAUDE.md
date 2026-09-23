# nodeagent/
> L2 | 父级: /CLAUDE.md

aegis-nodeagent：独立 Go module 的节点端，定位同 XrayR/V2bX。不 fork 内核，用 sing-box/xray 的注册表覆盖需要用户管理的入站，流量按用户在连接层统计，一个进程可带多个节点。core/、node/、panel/、tools/ 与 pdnd 同名目录一一对应，pdnd 在此基础上加入 kernel/ 的 NativeCore。

成员清单
main.go: 入口，装配 panel 客户端、node 循环与 multi 内核
core/core.go: Core 内核抽象
core/hoststat.go: 宿主机 CPU/内存/磁盘采集，供状态上报
core/counter/counter.go: 按用户流量统计、用户表与在线 IP 记录
core/multi/multi.go: 把多个内核组合成一个 core.Core
core/sing/: sing-box 内核层：managed/sing/routing 骨架，anytls/hysteria2/naive(+conn/masquerade)/quic/shadowsocks/shadowtls/socks/trojan/vless/vmess 入站
core/xray/: xray-core 内核层：xray 生命周期、registry 模块注册、protocols 协议配置、routing 出站与分流
core/mieru/mieru.go: mieru 协议入站
core/external/juicity.go: 以独立进程托管的 juicity
node/node.go: 拉配置、同步用户、上报流量
panel/client.go: 节点端与面板之间的通信层
tools/: vlesscheck / mierucheck / naivecheck / shadowtlscheck 最小客户端，验证入站真的在转发
*_test.go: 1 个测试文件
go.mod / go.sum: 独立 module

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

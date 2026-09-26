# pdnd/
> L2 | 父级: /CLAUDE.md

Pandora node 二进制（pandora-native / pdnd）。默认构建只链接 kernel/ 的 NativeCore：runtime_native.go 在 !compat 下提供 newRuntime，未验收组合直接失败；只有 -tags compat 构建时 runtime_compat.go 才把 core/ 的 sing-box/xray 适配器链进来。kernel/ 与 core/ 都实现 core.Core 抽象，node/ 负责与面板的闭环。

成员清单
main.go: 入口。子命令 bootstrap / enrollment / verify-identity / validate-install；flag -c 配置路径、-version、-capabilities 打印能力矩阵后退出、-self-check 校验能力注册表；装配 panel 客户端、node 循环与运行时
runtime_native.go: go:build !compat，生产默认运行时，只链接 NativeCore
runtime_compat.go: go:build compat，迁移构建专用，显式 native_only:false 时允许回落兼容内核
kernel/: NativeCore 自研数据面，13 协议入站、传输、REALITY、能力矩阵；见 kernel/CLAUDE.md
core/: core.Core 抽象与兼容适配层（sing-box/xray/mieru/外部进程/multi 分派/流量计数/限速）；见 core/CLAUDE.md
internal/: NativeCore 底层实现。nativewire/ 协议线格式（anytls/hysteria2/mkcp/shadowtls/tuic/udpmask）；reality/ fork 的 crypto/tls（TLS 1.2 + 1.3）承载 REALITY 握手；realityquic/ 自带 LICENSE 的 QUIC 栈（fork 自 quic-go），193 文件；这两个 fork 目录保持上游文件划分，整目录豁免 800 行规则
node/: node.go 把面板与内核粘起来：拉配置、同步用户、上报流量
panel/: 与面板通信层。client.go 基础客户端、enrollment.go 节点注册、signed.go 签名配置校验、effective_release.go 生效发布版本、config_key_transition.go 配置签名密钥轮换、stream.go 面板 SSE 订阅、status.go + status_linux/other.go 主机状态采集
outbound/: 出站层。outbound.go 抽象与选择，direct.go 直连，shadowsocks.go 出站加密，relay_socks.go SOCKS 中继，tls.go/utls.go TLS 与指纹
route/: rule.go 分流引擎
release/: build.sh 双架构发布与 manifest、check_native_panel_parity.py 对齐检查、check_native_stdout.sh、runtime-acceptance.sh 与 staging-acceptance.sh 验收、verify.sh、pandora-native.service systemd 单元、README
cmd/pandora-h3-probe/: 独立进程的 REALITY-over-HTTP/3 探针；见 cmd/pandora-h3-probe/CLAUDE.md
tools/: vlesscheck / mierucheck / naivecheck / shadowtlscheck 最小客户端，验证入站真的在转发
*_test.go: main_test.go 与 runtime_*_test.go 覆盖 flag 解析和运行时选择；linelimit_test.go 是全 module 的行数守卫：除 internal/reality、internal/realityquic 两个 fork 目录外，任何 .go 文件（含测试）超 800 行即红，豁免目录不存在了也红
go.mod / go.sum: Go 1.26.5 module

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

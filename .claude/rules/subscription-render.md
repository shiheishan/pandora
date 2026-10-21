---
paths:
  - "panel/internal/module/subscription/render/**"
  - "panel/internal/module/node/protocol/kernel_config*.go"
  - "pdnd/kernel/grpc_*.go"
  - "pdnd/kernel/host_match*.go"
---

# 订阅渲染与节点传输对口

## 先翻译，再渲染
- 库里 `protocol_config` 是 xboard 表单形状；订阅与下发都用内核字段名。node 在 `DeliverableNodes` 里 `protocol.KernelConfig`（`kernel_config.go` / `toKernelConfig`），与节点配置文档同一映射表
- `render` 包只读已翻译的 `Node.Config`，不碰库、不读 `protocol_config` 原始形状（`render/render.go` 包注释）
- 夹具：`render/testdata/nodes.json` 的 form 组是表单形状；legacy 组是翻译后的内核形状。新增跳过必须在 `render/matrix_test.go` 的 `renderMatrix()` 登记

## 格式与 UA
- 拉取路径：`render.DetectFormat(ua, target)` → `render.RenderRequest`（`service/pull.go` 经 `liveRequest` 组 `render.Request`）；sing-box 模板选项来自装配时的 `render.Options`（配置 `subscription.singbox`），渲染包内不读环境变量
- 格式：Clash Meta、Clash Premium、Stash、sing-box、Loon、Surfboard、SIP008、Shadowrocket（URI + 开头 STATUS 行）、URI。Surge、Quantumult X 不单做，照旧给 URI（xboard-parity 4.2 定）
- 分派顺序（`DetectFormat`）：显式 `target`/`flag` 先认（`clash` 遇 Stash UA 给 Stash）；UA 按 sing-box 家族 → shadowrocket → loon → surfboard → stash → shadowsocks（不含 shadowsocksx/shadowsocksr，给 SIP008）→ URI 名单 → clash 家族（`clashFlavor`）→ URI。分支全在 `formats_test.go`
- Clash Premium 与 Meta：`clashFlavor`，拿不准的「clash」给 Premium（`render/render.go`）；Stash 单独一类（`render/stash.go`，在 Meta 形状上按 Stash 文档改，去掉 naive 与 httpupgrade）
- 连不上的组合跳过节点，不让一条坏出站拖垮整份订阅（`render/render.go`）；原因清单在 `matrix_test.go` 的 `renderMatrix()`，新格式的在 `matrix_loon_surfboard_test.go`、`matrix_stash_sip008_test.go`，form + legacy 夹具每个格式都要有一行
- 前处理（`render/request.go` 的 `prepare`）只改副本：`types`（超过 200 字不过滤）/`filter`（超过 20 字不过滤）参数过滤 → 协议前缀（后台开关；照 Xboard 与名字之间不加空格，如 `[vless]香港 01`）→ 信息节点插最前（后台开关；地址 127.0.0.1:1，Shadowsocks）→ Loon/Surfboard 名字（`lineSafeName`）去掉 `= , " [ ]`、换行与行首注释符 `#`、`;`、`//` → 去重名。一个真实节点都写不出时信息节点也不写，返回的节点数只算真实节点。单测在 `render/request_test.go`
- Loon/Surfboard 一行里其它字段（路径、SNI、密码等）含换行时跳过该节点（`nodeToLoon`/`nodeToSurfboard` 包的一层）；逗号未处理，这些值只由后台填写
- SS 的 udp：Loon `udp=true`、Surfboard `udp-relay=true`，与 Clash 的 `udp: true`、sing-box 不写 `network` 同一口径；ShadowTLS 只承载 TCP，Loon 仍 `udp=false`
- 共享节点缓存不被改：`render/shared_test.go` 覆盖全部 9 种格式与带前处理（前缀、信息节点、types/filter）的 `RenderRequest`，检查机跑 race

## 内置模板与占位替换
- 后台自定义模板 10-21 起整条删掉（编辑接口先删，拉取侧的 `Request.Template` 与表 `subscription_templates` 随迁移 00068 删）；订阅一律用内置模板，URI、Shadowrocket、SIP008 本来就没有模板
- 内置模板：`defaultClashTemplate`（Clash 系三种共用）、`defaultSurfboardTemplate`、`defaultLoonTemplate`；sing-box 不走文本模板，直接 `singboxFrame` 出 JSON（见下一节）
- 占位是纯文本替换（`fillTemplate`，`render/request.go`）：Clash 系与 Surfboard 的 `{{proxies}}`、`{{proxy_groups}}`、`{{proxy_names}}`，Surfboard 的 `{{subscribe_url}}`（订阅链接为空时 `dropLinesWith` 去掉那行），Loon 只有 `{{proxies}}`
- 内置 Clash 模板必须与 golden 逐字节一致（`defaultClashTemplate`）

## sing-box 附带模板（用户 2026-10-07）
- 只有 `FormatSingbox` 走 `singbox_template.go`：TUN、DNS 分流、`strategy: ipv4_only` + `default_domain_resolver` 的 `prefer_ipv4`、远程 rule-set
- 规则集下载出口：`singboxDialectFor(ua)` — 认得出 sing-box ≥1.14 用 `http_clients` + `default_http_client`，否则 `download_detour`（`render/singbox_template.go`）
- 引用必须可解析；无节点时出站退回 `direct`（`template_test.go` 的 `assertSingboxReferencesResolve`）

## ShadowTLS / 协议写法
- Shadowsocks 节点同端口 TCP、UDP 都听（用户 2026-10-14 定）：Clash 系 `udp: true`，sing-box 不写 `network`，URI（SIP002）无 UDP 开关（矩阵 `ss-aes128`、`ss-chacha`）
- sing-box：外层 `shadowtls` + 内层 ss `detour`；握手端口在 `kernel_config.go` 的 shadowtls 分支折进 `server`（监听 `server_port` 占住）；ShadowTLS 只承载 TCP，Clash 仍 `udp: false`、sing-box 内层仍 `network: tcp`
- Shadowsocks 2022（SIP022 + SIP023 多用户，共用约定 2026-10-14）：node 在 `DeliverableNodes` 里给 2022 节点的 `Node.Config` 注入 `password` = base64(服务端身份密钥 iPSK)（与节点配置文档同键同值，`node/service/ss2022.go`；中转浅拷贝父节点 Config，用父节点那把）。render 的 `ssPassword`（`render/opts.go`）拼客户端密码 `base64(iPSK):base64(uPSK)`，uPSK = SHA-256("pandora/ss2022/user-psk/v1" || 0x00 || 小写代理 UUID) 取前 keyLen 字节，与 pdnd 各自实现，向量钉在 `render/ss2022_test.go`；iPSK 缺失或长度不符跳过节点
  - 格式：Clash Meta、sing-box、Shadowrocket/URI、SIP008 写冒号密码；Clash Premium 跳过（不认 2022）；Surfboard 跳过（文档要求 2022 密码是单个定长 base64）；Loon、Stash 暂时跳过（官方文档的 2022 只有单个 password，没提 SIP023 冒号多密钥写法；真机确认能连后再放开，放开时记下客户端版本与证据，同步改 `ss2022CipherHint`）；`2022-blake3-chacha20-poly1305` 一律跳过（SIP023 身份头用 AES，chacha20 版做不了多用户，面板 schema 也不开放）。
  - URI（SIP002）：2022 的 userinfo 不做 base64，`url.QueryEscape(method) + ":" + url.QueryEscape(password)`；经典 AEAD 仍是 base64url
  - 夹具：nodes.json 的 form 组 `ss2022-aes128`/`ss2022-aes256` 的 config 带 `password`（低熵虚构值，免得 gitleaks 误报；KernelConfig 不产出，`kernel_config_parity_test.go` 对照时剥掉），legacy 组 `ss2022-chacha` 钉住跳过
- httpupgrade 在 Clash 里常写 `ws` + `v2ray-http-upgrade`；URI 里 mKCP 用 `type=kcp`（矩阵 `vless-mkcp`）
- naive + `allow_insecure`：sing-box / URI 跳过（`naive-insecure`）；TLS+gRPC 且 SNI≠Host：Clash/sing-box 跳过，URI 可用 `authority`（`trojan-tls-grpc-sni`）

## pdnd 与订阅同一口径
- gRPC 传输是 gun 帧（`pdnd/kernel/grpc_stream.go`）；不要当裸流读写。REALITY+gRPC 时客户端 `:authority` 是 REALITY server name
- `requestHostMatches`（`pdnd/kernel/host_match.go`）：Host / `:authority` 不比端口、IPv6 去括号、大小写不敏感；没配 host 不检查
- 改 gun 或 Host 匹配后跑 subscription-e2e 实连（见 `.claude/rules/pdnd-kernel.md` 互操作一节）

## ECH 与中转（迁移 00045）
- `Node.ECHConfigList` 非空才写 ECH：Clash Meta `ech-opts {enable: true, config: base64(ECHConfigList)}`，sing-box `tls.ech {enabled: true, config: PEM 行数组}`（与 Xboard `ManageController` 生成的同格式）；只给 vless/vmess/trojan 的普通 TLS（非 REALITY）、AnyTLS、naive（sing-box），Clash Premium 与 Stash 不写（`render/clash.go`、`singbox.go`、`stash.go`，单测 `render/ech_test.go`）。node 侧只在 `protocol.ECHCapable` 时填它，渲染侧再按协议挡一次。
- 中转线路在 node 侧由父节点派生（`relayRenderNode`），渲染层看到的就是普通 `Node`。节点标签只在后台用，`render.Node` 不带标签，订阅文件与门户预览都不出。

## 与 subscription 模块的边界
- 配置名：`render.ProfileName` + `render.ContentDisposition`（`render/profile.go`，调用处 `service/pull.go`、`service/portal.go`）；门户 `client_name` 同口径。改名不 `notifyRoster`
- 主人账号非 active：拉取 `ErrPullDenied`（伪装 404），不给过期提示（`service/pull.go`）

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
- 前处理（`render/request.go` 的 `prepare`）只改副本：`types`/`filter` 参数过滤 → 协议前缀（后台开关）→ 信息节点插最前（后台开关；地址 127.0.0.1:1，Shadowsocks）→ Loon/Surfboard 名字去掉 `= , " [ ]` 与换行 → 去重名。一个真实节点都写不出时信息节点也不写，返回的节点数只算真实节点
- SS 的 udp：Loon、Surfboard 走 `ssNodeUDP`（`render/loon.go`，现为 false），与 `clash.go`、`singbox.go` 的写法同一口径；节点端 SS 支持 UDP 时几处一起改

## 后台模板（`render/template.go`，表 `subscription_templates`）
- 可编辑：clash、clash-premium、stash、singbox、surfboard、loon（`TemplateFormats`）；URI、Shadowrocket、SIP008 没有模板。库里没有行就用内置模板（`DefaultTemplate`），恢复缺省 = 删行
- 占位是纯文本替换（`fillTemplate`，拉取时不解析模板）：Clash 系与 Surfboard 的 `{{proxies}}`、`{{proxy_groups}}` 必需且单独一行顶格；`{{proxy_names}}`、`{{subscribe_url}}` 可选；Loon 只有 `{{proxies}}`；sing-box 是 JSON 数组里带引号的 `"{{outbounds}}"`（必需）、`"{{proxy_tags}}"`
- 保存前 `ValidateTemplate`：≤256KB（与迁移 00039 的 CHECK 一致）、UTF-8、无 NUL、必需占位、用示例节点与零节点各渲染一次后 YAML/JSON 能解析；内置 Clash 模板必须与 golden 逐字节一致（`defaultClashTemplate`）
- 自定义 sing-box 模板对所有版本原样下发，按 1.14 切换规则集写法只有内置模板做

## sing-box 附带模板（用户 2026-10-07）
- 只有 `FormatSingbox` 走 `singbox_template.go`：TUN、DNS 分流、`strategy: ipv4_only` + `default_domain_resolver` 的 `prefer_ipv4`、远程 rule-set
- 规则集下载出口：`singboxDialectFor(ua)` — 认得出 sing-box ≥1.14 用 `http_clients` + `default_http_client`，否则 `download_detour`（`render/singbox_template.go`）
- 引用必须可解析；无节点时出站退回 `direct`（`template_test.go` 的 `assertSingboxReferencesResolve`）

## ShadowTLS / 协议写法
- Shadowsocks 节点同端口 TCP、UDP 都听（用户 2026-10-14 定）：Clash 系 `udp: true`，sing-box 不写 `network`，URI（SIP002）无 UDP 开关（矩阵 `ss-aes128`、`ss-chacha`）
- sing-box：外层 `shadowtls` + 内层 ss `detour`；握手端口在 `kernel_config.go` 的 shadowtls 分支折进 `server`（监听 `server_port` 占住）；ShadowTLS 只承载 TCP，Clash 仍 `udp: false`、sing-box 内层仍 `network: tcp`
- httpupgrade 在 Clash 里常写 `ws` + `v2ray-http-upgrade`；URI 里 mKCP 用 `type=kcp`（矩阵 `vless-mkcp`）
- naive + `allow_insecure`：sing-box / URI 跳过（`naive-insecure`）；TLS+gRPC 且 SNI≠Host：Clash/sing-box 跳过，URI 可用 `authority`（`trojan-tls-grpc-sni`）

## pdnd 与订阅同一口径
- gRPC 传输是 gun 帧（`pdnd/kernel/grpc_stream.go`）；不要当裸流读写。REALITY+gRPC 时客户端 `:authority` 是 REALITY server name
- `requestHostMatches`（`pdnd/kernel/host_match.go`）：Host / `:authority` 不比端口、IPv6 去括号、大小写不敏感；没配 host 不检查
- 改 gun 或 Host 匹配后跑 subscription-e2e 实连（见 `.claude/rules/pdnd-kernel.md` 互操作一节）

## 与 subscription 模块的边界
- 配置名：`render.ProfileName` + `render.ContentDisposition`（`render/profile.go`，调用处 `service/pull.go`、`service/portal.go`）；门户 `client_name` 同口径。改名不 `notifyRoster`
- 主人账号非 active：拉取 `ErrPullDenied`（伪装 404），不给过期提示（`service/pull.go`）

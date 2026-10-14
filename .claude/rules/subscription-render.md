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
- 拉取路径：`render.DetectFormat(ua, target)` → `render.Render`（`service/pull.go`）；sing-box 模板选项来自装配时的 `render.Options`（配置 `subscription.singbox`），渲染包内不读环境变量
- Clash Premium 与 Meta：`clashFlavor`，拿不准的「clash」给 Premium（`render/render.go`）
- 连不上的组合跳过节点，不让一条坏出站拖垮整份订阅（`render/render.go`）；原因清单在 `matrix_test.go` 的 `renderMatrix()`

## sing-box 附带模板（用户 2026-10-07）
- 只有 `FormatSingbox` 走 `singbox_template.go`：TUN、DNS 分流、`strategy: ipv4_only` + `default_domain_resolver` 的 `prefer_ipv4`、远程 rule-set
- 规则集下载出口：`singboxDialectFor(ua)` — 认得出 sing-box ≥1.14 用 `http_clients` + `default_http_client`，否则 `download_detour`（`render/singbox_template.go`）
- 引用必须可解析；无节点时出站退回 `direct`（`template_test.go` 的 `assertSingboxReferencesResolve`）

## ShadowTLS / 协议写法
- sing-box：外层 `shadowtls` + 内层 ss `detour`；握手端口在 `kernel_config.go` 的 shadowtls 分支折进 `server`（监听 `server_port` 占住）
- httpupgrade 在 Clash 里常写 `ws` + `v2ray-http-upgrade`；URI 里 mKCP 用 `type=kcp`（矩阵 `vless-mkcp`）
- naive + `allow_insecure`：sing-box / URI 跳过（`naive-insecure`）；TLS+gRPC 且 SNI≠Host：Clash/sing-box 跳过，URI 可用 `authority`（`trojan-tls-grpc-sni`）

## pdnd 与订阅同一口径
- gRPC 传输是 gun 帧（`pdnd/kernel/grpc_stream.go`）；不要当裸流读写。REALITY+gRPC 时客户端 `:authority` 是 REALITY server name
- `requestHostMatches`（`pdnd/kernel/host_match.go`）：Host / `:authority` 不比端口、IPv6 去括号、大小写不敏感；没配 host 不检查
- 改 gun 或 Host 匹配后跑 subscription-e2e 实连（见 `.claude/rules/pdnd-kernel.md` 互操作一节）

## 与 subscription 模块的边界
- 配置名：`service.ProfileName` + `render.ContentDisposition`（`service/label.go`、`service/pull.go`）；门户 `client_name` 同口径。改名不 `notifyRoster`
- 主人账号非 active：拉取 `ErrPullDenied`（伪装 404），不给过期提示（`service/pull.go`）

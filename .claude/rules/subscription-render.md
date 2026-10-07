---
paths:
  - "panel/internal/domain/subscription/render*.go"
  - "panel/internal/domain/nodefabric/xboard_*.go"
  - "panel/internal/domain/nodefabric/protocol_schema.go"
  - "panel/internal/domain/nodefabric/uniproxy_config.go"
  - "pdnd/kernel/grpc_*.go"
  - "pdnd/kernel/host_match*.go"
---

# 订阅渲染与节点传输的对口（2026-10 w2render 立，w5retain 补 sing-box 模板）

- **先翻译，再渲染**：库里存的是后台表单写出的 xboard 形状（`tls` 三态、`reality_settings.*`、`network_settings.*`、`cipher`、`utls`），渲染器读的是内核字段名。`subscription.Render` 入口先经 `nodefabric.KernelConfig` 统一翻译，与下发给节点（`BuildNodeConfig`）的是同一张映射表。新增字段只改映射表，不要在某个格式里直接读原始 `protocol_config`
  - 渲染测试的夹具一律用表单形状（`render_fixtures_test.go`），扁平内核形状只作回归组：当初 REALITY 被渲染成普通 TLS，就是因为夹具用了库里从不存在的形状，测试一直是绿的
  - `render_matrix_test.go` 是「每种协议 × 每种格式 渲染 / 跳过及原因」的事实清单：新增跳过要在那里登记原因，否则就是渲染器悄悄丢了节点；改渲染跑 subscription-e2e skill 做实连旁证
- **节点端 gRPC 是 gun 帧**：Xray / sing-box / mihomo 的 gRPC 传输每条消息都是 protobuf `Hunk{bytes data = 1}`（TunMulti 是 `MultiHunk`），线格式是若干个「0x0a + varint 长度 + 数据」。pdnd 收发都按它拆包、封包（`pdnd/kernel/grpc_stream.go` 的 `decodeGunHunk` 与写路径），不要当裸数据读写：那样 VLESS 报版本无效、VMess 认证失败。守卫 `grpc_gun_test.go`、`grpc_gun_interop_test.go`
- **Host 比较不比端口**：ws / httpupgrade / gRPC / xhttp 的 Host（含 gRPC 的 `:authority`）在非默认端口上带端口，节点端经 `requestHostMatches`（`host_match.go`）去掉端口与 IPv6 方括号、大小写不敏感地比主机名；配了 Host 只放行这个主机名，没配不检查。后台没配 `network_settings.headers.Host` 时下发与订阅都写节点地址，两边同一口径
- **sing-box 订阅附带模板**（用户 2026-10-07 定，`render_singbox_template.go`）：TUN 入站、DoH 国内外分流、嗅探 → 劫持 DNS → 私网直连 →（可选广告拦截）→ 国内直连、其余走「节点选择」，远程规则集经代理下载、开 cache_file。字段按 sing-box 1.12 / 1.13（带 type 的 DNS 服务器、规则 action），不要写回 geoip / geosite 数据库或 block / dns 特殊出站
  - 所有引用（路由出站、DNS detour、规则集下载出口、规则集 tag、DNS 服务器 tag）都必须指向存在的东西，没节点时退回 direct：一处悬空 sing-box 整份起不来（守卫 `render_singbox_template_test.go` 的 `assertSingboxReferencesResolve`）
  - 规则集地址前缀与广告拦截开关经 `platform/config` 的 `SingboxTemplate`（`AEGIS_SINGBOX_*`），aegis-public 启动时 `subscription.ConfigureSingboxTemplate` 设一次；渲染里不读环境变量
  - 只改 sing-box 格式：Clash、URI 不经过模板；Hiddify、Karing 这类第三方 sing-box 客户端只取出站

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
- **节点端 gRPC 是 gun 帧**：Xray / sing-box / mihomo 的 gRPC 传输每条消息都是 protobuf `Hunk{bytes data = 1}`（TunMulti 是 `MultiHunk`），线格式是若干个「0x0a + varint 长度 + 数据」。pdnd 收发都按它拆包、封包（`pdnd/kernel/grpc_stream.go` 的 `decodeGunHunk` 与写路径），不要当裸数据读写：那样 VLESS 报版本无效、VMess 认证失败。REALITY 加 gRPC 时客户端发的 `:authority` 是 REALITY 的 server name，不是节点地址。守卫 `grpc_gun_test.go`、`grpc_gun_interop_test.go`
- **Host 比较不比端口**：ws / httpupgrade / gRPC / xhttp 的 Host（含 gRPC 的 `:authority`）在非默认端口上带端口，节点端经 `requestHostMatches`（`host_match.go`）去掉端口与 IPv6 方括号、大小写不敏感地比主机名；配了 Host 只放行这个主- **ShadowTLS 必须成对出站**：sing-box 里外层 `shadowtls`（v3、外层密码、`tls.server_name` 填握手站点、开 uTLS）加内层 ss 经 `detour` 串上去，外层不进选择组，tag 要唯一（撞名时 `uniqueTag` 改名）；少了外层整份订阅都起不来（`dependency[…] not found`）。表单里填的握手端口在翻译时折进 `server`（host:port，`xboard_field_names.go`），因为下发时 `server_port` 被入站监听端口占住。守卫 `TestSingboxShadowTLSEmitsBothOutbounds`
- **客户端不支持就跳过，并在矩阵登记原因**：不跳过的话客户端不报错，只是拿一个残缺出站去连
  - sing-box：xhttp（只有 Clash 的 `xhttp-opts` 和 URI 有）、mKCP（`TestRenderSingboxSkipsMKCPNodes`）、TLS 加 gRPC 且 SNI≠Host（sing-box 与 mihomo 都设不了 `:authority`，`grpcAuthorityMismatch`；URI 用 `authority` 参数可以）
  - naive：sing-box 的 naive 出站不支持跳过证书校验，勾了 allow_insecure 的在 sing-box 与 URI 里跳过，mihomo 本来没有 naive
  - 都登记在 `render_matrix_test.go`（如 `naive-insecure`、`vless-mkcp`、`vless-reality-xhttp`、`trojan-tls-grpc-sni`）
- **Clash Premium 与 Meta 分开给**：Premium 内核（ClashX、CFW）只认 ss / vmess / trojan / socks5 / http，传输只有 tcp / ws / grpc 且不含 REALITY，不认 vless、hy2、tuic、anytls、mieru，整份 YAML 会加载失败。`FormatClashPremium` 按 UA 分，拿不准的「clash」一律给 Premium。守卫 `TestClashPremiumOnlyGetsProxiesItUnderstands`
- **协议在各客户端的写法**：httpupgrade 在 mihomo 里写 `network: ws` 加 `ws-opts.v2ray-http-upgrade: true`（mihomo 没有 httpupgrade 这个 network，矩阵 `trojan-tls-httpupgrade`）；分享链接里 mKCP 写 `type=kcp`，v2rayN 系不认 `mkcp`（`render_uri.go`，矩阵 `vless-mkcp`）。面板内部统一存 `mkcp`，`isMKCPNetwork` 同时认 `kcp`、`m-kcp`
机名，没配不检查。后台没配 `network_settings.headers.Host` 时下发与订阅都写节点地址，两边同一口径
- **sing-box 订阅附带模板**（用户 2026-10-07 定，`render_singbox_template.go`）：TUN 入站、DoH 国内外分流、嗅探 → 劫持 DNS → 私网直连 →（可选广告拦截）→ 国内直连、其余走「节点选择」，远程规则集经代理下载、开 cache_file。字段按 sing-box 1.12 / 1.13（带 type 的 DNS 服务器、规则 action），不要写回 geoip / geosite 数据库或 block / dns 特殊出站
  - 所有引用（路由出站、DNS detour、规则集下载出口、规则集 tag、DNS 服务器 tag）都必须指向存在的东西，没节点时退回 direct：一处悬空 sing-box 整份起不来（守卫 `render_singbox_template_test.go` 的 `assertSingboxReferencesResolve`）
  - 规则集地址前缀与广告拦截开关经 `platform/config` 的 `SingboxTemplate`（`AEGIS_SINGBOX_*`），aegis-public 启动时 `subscription.ConfigureSingboxTemplate` 设一次；渲染里不读环境变量
  - 只改 sing-box 格式：Clash、URI 不经过模板；Hiddify、Karing 这类第三方 sing-box 客户端只取出站
- **配置名只有一个来源**（购买模型统一 2.8，2026-10-07）：订阅下载的 Content-Disposition 文件名与门户订阅列表的 `client_name` 都经 `subscription.ProfileName(站点名, 备注名, 套餐名)`（`label.go`）：「站点名 · 备注名」，没起名时「站点名 · 套餐名」。`ContentDisposition` 的入参是完整配置名，`filename` 只留安全 ASCII 并把连续空白并成一个，`filename*` 是 UTF-8 原名；备注名字符规则只在 `purchase.NormalizeLabel`。pull 的 `pullAuthSQL` 按主键带出 `s.label` 与套餐名，不另查。改名不推进节点下发纪元

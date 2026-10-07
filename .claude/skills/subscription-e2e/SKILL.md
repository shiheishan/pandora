---
name: subscription-e2e
description: pandora 订阅渲染的本地端到端矩阵：把后台表单形状的节点夹具渲染成 clash / clash-premium / sing-box / uri，sing-box 离线校验、Clash 与 URI 静态检查，再起本地 pdnd NativeCore 用订阅里的 sing-box 出站真连一次，出一张矩阵。改订阅渲染（panel/internal/domain/subscription/render*）、协议 schema 或字段翻译（nodefabric/protocol_schema.go、xboard_*、uniproxy_config.go）、pdnd kernel 传输层，以及上真节点验证之前使用；比修前修后、导出前端 node-schemas.ts 也用它。
---

# 订阅渲染端到端矩阵

目标：用户拿到的订阅能连上节点。仓库里的渲染测试只看字符串，这里看的是三件事：
1. 渲染出的配置能被客户端内核接受；
2. 下发给节点的配置（真实的 `BuildNodeConfig` 输出）能让 NativeCore 起来；
3. 两边对得上，经代理能拉到目标。

全程离线、在本机跑，不碰仓库文件（临时测试经 `go test -overlay` 注入）。Go 构建缓存是热的时候，跑一次约 10 秒；第一次编译 sing-box（含 cronet）要慢得多。

## 什么时候跑

- 改了 `panel/internal/domain/subscription/render*.go`、`render_fixtures_test.go`
- 改了 `nodefabric` 的协议 schema、校验或字段翻译：`protocol_schema.go`、`xboard_field_names.go`、`xboard_validate.go`、`protocol_validate_*.go`、`uniproxy_config.go`
- 改了 `pdnd/kernel` 的入站或传输：ws、httpupgrade、grpc、xhttp、REALITY、Host 校验
- 上真节点测试机之前先跑一遍。这里不通的，真节点上也不会通
- 审计或验收某一路的渲染改动时，比一次修前修后

## 步骤

1. 在仓库根（或 worktree 根）跑 `bash .claude/skills/subscription-e2e/scripts/run.sh --work <scratchpad>/sube2e`。
   - `--work` 指到会话 scratchpad；不给时用 `$TMPDIR/pandora-subscription-e2e`。
   - 退出码 0 表示没有 FAIL，1 表示有。
2. 读矩阵：
   - 每个夹具一行，列依次是 clash、premium、sing-box 校验、sing-box E2E、uri、下发。
   - 有 FAIL 或 WARN 时看下面的「明细」。每个用例的客户端配置在 `<work>/e2e/<id>.client.json`，可以拿去手跑。
3. 修了再跑，直到没有 FAIL。
   - 新出现的 skip 要在 `render_matrix_test.go` 里有登记的跳过原因，否则就是渲染器悄悄丢了节点。
4. 交付报告里贴矩阵的汇总行和明细。
   - 写清哪几列是真连通（sing-box E2E），哪几列只是静态核对（clash、premium、uri）。

### 比修前修后

1. 把基点导出成快照放进 scratchpad：`git archive <基点> panel pdnd | tar -x -C <scratchpad>/base`。不要用 `git worktree add`，那会改仓库的 worktree 登记。
2. 用当前的夹具文件跑基点：
   ```
   run.sh --repo <scratchpad>/base --work <scratchpad>/sube2e-before \
     --fixtures-file panel/internal/domain/subscription/render_fixtures_test.go \
     --formats clash,singbox,uri --extra none
   ```
   - `--fixtures-file` 会替换基点包里的 `render_fixtures_test.go`，没有就补进去。
   - 基点没有 `FormatClashPremium` 时，用 `--formats` 把 premium 去掉。
3. 修后的结果用主线或分支跑一遍，两张矩阵对照。
4. 实例（2026-10-07）：w2render 之前的基点 sing-box E2E 失败 23 个，主线 ddf39ec 是通 28、skip 15、失败 0。

### 其他用法

- **额外夹具**：`fixtures/extra.json` 每次默认都跑。格式是 `[{id, type, port, config}]`。
  - 现有五条：旧扁平形状的 REALITY（回归组）、无证书 AnyTLS（应全格式 skip）、cipher 与 method 冲突的 ss（应全格式 skip，下发被拒），以及两条 2026-10 起存不进去的存量明文（vless / vmess 裸 tcp 不加密：后台校验不过，但库里的照常下发、照常渲染，E2E 要通）。
  - 想试一个还没进 `formFixtures` 的形状，就另写一个文件用 `--extra` 传入。
- **只跑几个 E2E 用例**：`--only vless-ws,trojan-tls-grpc --log debug`，sing-box 客户端日志打到 `results/e2e.stderr`。
- **导出前端假后端的 schema**：`scripts/export-schemas.sh` 把 `nodefabric.ProtocolSchemas()` 原样写进 `panel/frontend/dev/mock/admin/node-schemas.ts`，然后用 `git diff` 看变化。这份文件没有自动同步，也没有守卫，改了 schema 必须重新导出。
- **REALITY 密钥**：`cd tools && go run ./kp` 生成一对新密钥，`go run ./kp <私钥>` 核对私钥和公钥是否成对。

## 脚本与文件

| 路径 | 作用 |
|---|---|
| `scripts/run.sh` | 一条命令跑完 5 步，参数见文件头（`--repo --work --extra --fixtures-file --formats --only --no-e2e --no-naive --log`） |
| `scripts/matrix.py` | 把 `out/summary.json` 和四类检查结果拼成矩阵；有 FAIL 时退出码为 1 |
| `scripts/export-schemas.sh` | 导出 node-schemas.ts |
| `overlay/zz_e2e_dump_test.go` | 注入 subscription 包：渲染每个夹具和整份（ALL、EMPTY），写出 `fixtures_index.json`、`summary.json` |
| `overlay/zz_e2e_nodeconfig_test.go` | 注入 nodefabric 包：真实 `BuildNodeConfig` 生成 `node_config.json`；被拒的记进 `node_config_errors.json` |
| `overlay/zz_e2e_schemas_test.go` | 注入 nodefabric 包：导出 schema |
| `tools/`（独立 Go module） | `e2e`、`sbcheck`（离线 sing-box check，含 Start；Start 用去掉 TUN 与远程规则集的离线副本）、`yamlcheck`、`uricheck`、`kp` |
| `fixtures/extra.json` | 常备的额外夹具；REALITY 密钥写成占位符 `@REALITY_PRIVATE@` / `@REALITY_PUBLIC@` |

`tools/go.mod` 锁 sing-box v1.13.14，与 pdnd 同版本。`replace` 指向 `../../../../pdnd`，即本仓库的 pdnd。run.sh 会把 tools 拷到工作目录，再把 replace 改成 `--repo` 的 pdnd，所以测 worktree 或基点快照时不用改 skill。

构建 tags 固定为 `with_quic,with_utls,with_gvisor,with_naive_outbound`，写在 run.sh 里。

全程只用本机 Go 模块缓存（`GOPROXY=off`、`GOSUMDB=off`、`GOTOOLCHAIN=local`），不要为它打开联网下载：
- 编译报模块缺失，说明 pdnd 自己的依赖还没在本机拉齐。先按 verify 流程把 pdnd 构建通过，再回来跑。
- pdnd 升级依赖后，run.sh 会在工作副本里离线补齐 go.mod，并提示把 `go.mod`、`go.sum` 拷回 skill。拷回时 replace 行要改回相对路径。

## 矩阵怎么读

- **OK**：写出了节点，检查通过。E2E 列的 OK 是真连通。
- **skip**：渲染器没写出这个节点（计数为 0），文件本身仍然合法。
- **WARN**：静态检查有疑点。字段缺了客户端不会报错，但那个节点连不上。
- **FAIL**：解析、校验或启动失败；或 E2E 不通；或渲染与 E2E 的结论对不上（订阅里有这个节点，E2E 却找不到）；或表单夹具没过后台校验。
- **-**：这一项没跑。
- 「下发」列是 `BuildNodeConfig` 的结论。订阅里给了、节点端却被拒的，E2E 记 `NODE-CONFIG-REJECTED`，算 FAIL。
- ALL 行是整份订阅。sing-box 里只要有一个节点起不来，整份配置都会失效，所以 ALL 行要单独看。

## 坑

- **夹具必须是后台表单写进库的形状**（xboard：`tls` 三态、`reality_settings.*`、`network_settings.*`、`cipher`、`utls`、`tls_settings.*`）。
  - 渲染器先经 `nodefabric.KernelConfig` 翻译成内核形状，和下发给节点的是同一张映射表。
  - 当初的事故就是夹具用了内核扁平形状：库里从来不存这种形状，测试照样全绿，REALITY 却被渲染成普通 TLS。
  - 额外夹具的「来源」列会标出没过后台校验的，扁平形状只用作回归组。
- **naive**
  - sing-box 要带 `with_naive_outbound` 构建（cgo 加 cronet），否则报 `naive outbound is not included in this build`，naive 行和 ALL 行都变 FAIL。`--no-naive` 只在 cronet 链接不了时用。
  - sing-box 的 naive 出站不支持跳过证书校验，带上 insecure 整份配置都起不来。所以勾了 allow_insecure 的 naive 在 sing-box 和 URI 里跳过。mihomo 本来就没有 naive。
- **shadowtls 必须成对出站**：外层 shadowtls（v3、外层密码、`tls.server_name` 填握手站点、开 uTLS），内层 ss 经 `detour` 串上去，tag 要唯一。
  - 少了外层，Start 报 `dependency[<名>-stls] not found`，整份订阅一起失效。
  - 握手端口曾被 `BuildNodeConfig` 用监听端口覆盖。现在的做法是在翻译时把表单里填的握手端口折进 `server`（host:port）。E2E 只把 `server` 换成本地站点，没写出 `server` 时记 `E2E-PRECONDITION`。
- **节点端 gRPC 是 gun 的 Hunk 帧**：Xray、sing-box、mihomo 的消息体都先包一层 Hunk。
  - 当成裸数据收发时，Host 修好之后 vless 报版本无效，vmess 报 auth id rejected。
  - REALITY 加 gRPC 时，客户端发的 `:authority` 是 REALITY 的 server name，不是节点地址。
- **Host 校验不比端口**：节点端比较前先 `SplitHostPort`，IPv6 的方括号也要处理。
  - E2E 把端口统一加了 20000，Host 和 `:authority` 里会带上非默认端口，这个坑就是这样测出来的。
  - 后台没配 `network_settings.headers.Host` 时，下发和订阅都写节点地址，两边口径一致。
- **sing-box 跳过 xhttp**：它没有 xhttp，这类节点只在 Clash（`xhttp-opts`）和 URI 里有。
  - mKCP 在 Clash 和 sing-box 里都跳过。
  - TLS 加 gRPC、且 SNI 与 Host 不同时，sing-box 和 mihomo 设不了 `:authority`，也跳过；URI 用 `authority` 参数可以。
  - 所以 mieru、juicity、socks over TLS、xhttp、mKCP 都没有 E2E 覆盖，只有静态检查。
- **sing-box 订阅带 TUN、远程规则集与 cache_file（w5retain 起）**：原样 Start 要 root 建 TUN、要联网下载规则集。
  - `sbcheck` 先对原样配置跑 `box.New`（TUN 与规则集的写法照样校验），`-start` 时 Start 的是离线副本：去掉 TUN 入站与 cache_file，远程规则集换成同 tag 的内联规则集，`download_detour` 另行核对必须指向存在的出站。
  - 真客户端第一次启动时规则集下载失败（经「节点选择」下载，节点不通）整份配置起不来，下载成功一次后靠 cache_file 缓存。这一条这里测不到，上真机验。
  - E2E 只取订阅里的出站，自建入站，不受模板影响。
- **E2E 环境注入了信任，通了不等于真环境能用**：
  - 客户端信任本次的自签证书；
  - REALITY 的 dest 和 ShadowTLS 的握手站点换成了本地 TLS 站点；
  - `node.example.com` 经 hosts DNS 指到 127.0.0.1。
  - 自签证书、缺 SNI、server_host 是 IP 的节点，这里能通，真环境里不一定。
  - 修前 naive「通」就是这个原因：面板根本没下发 insecure。上线前仍要上真节点验。
- **Clash 和 URI 两列只是静态核对**：本机没有 mihomo 和 Xray 客户端。
  - mihomo 对空的 `proxies` 组会整份拒载，`yamlcheck` 查这条。
  - Clash Premium 内核不认 vless、hy2、tuic、anytls、mieru，必须按 UA 和 Meta 分开给（`FormatClashPremium`）。
  - httpupgrade 在 mihomo 里要写成 `network: ws` 加 `ws-opts.v2ray-http-upgrade: true`。
  - 分享链接里 mKCP 要写 `kcp`，v2rayN 系不认 `mkcp`。
- **overlay 依赖包内的名字**：
  - subscription 包：`Render`、`Node`、`formFixtures`、`fixtureUUID`、`fixtureHost`、`fixtureRealityPri`、`fixtureRealityPub`；
  - nodefabric 包：`BuildNodeConfig`、`ServingNode`、`ValidateAdminProtocolConfig`、`StableProtocolSchemaVersion`、`ProtocolSchemas`。
  - 这些改名后，overlay 要同步改，否则第 1 步编译失败。
  - overlay 的路径必须是绝对路径；`zz_e2e_*` 文件名不能和磁盘上的文件重名。
  - 测试读环境变量和文件，所以一律 `-count=1`，免得命中测试缓存。
- **夹具 id 不能叫 ALL 或 EMPTY，也不能重复**。端口加 20000 后不能超过 65535。用例串行跑，上一个用例是异步关闭的，所以端口别复用。
- **夹具里不写密钥**：REALITY 用占位符，换成包里那对测试专用的虚构密钥。要加新密钥，用 `kp` 生成，再按完整值登记进 `.gitleaks.toml` 的放行清单。
- **skip 本身不算失败**，但每个 skip 都要在 `render_matrix_test.go` 有原因。渲染矩阵测试是事实清单，这张矩阵是它的实连旁证。
- 不要和 `npm ci` 同时跑：`node_modules` 里带 Go 包，并发时 go 会假失败。

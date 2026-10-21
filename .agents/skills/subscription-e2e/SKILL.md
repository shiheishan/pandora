---
name: subscription-e2e
description: pandora 订阅渲染的本地端到端矩阵：把后台表单形状的节点夹具渲染成 clash / clash-premium / sing-box / uri，sing-box 离线校验、Clash 与 URI 静态检查，再起本地 pdnd NativeCore 用订阅里的 sing-box 出站真连一次，并用官方 mihomo 按 Clash 订阅真连一次（给了 MIHOMO_BIN 时），出一张矩阵。改订阅渲染（panel/internal/module/subscription/render）、协议 schema、后台校验或字段翻译（panel/internal/module/node/protocol 的 schema.go、validate*.go、kernel_config.go）、节点配置文档（node/service/config_docs.go）、pdnd kernel 传输层，以及上真节点验证（node-e2e）之前先用本 skill，它通过后再上真机；比修前修后也用它。
---

# 订阅渲染端到端矩阵

目标：用户拿到的订阅能连上节点。仓库里的渲染测试只看字符串，这里看的是三件事：
1. 渲染出的配置能被客户端内核接受；
2. 下发给节点的配置文档（真实的 `buildNodeConfigDoc` 输出，面板经 gRPC 下发的就是它）能让 NativeCore 起来；
3. 两边对得上，经代理能拉到目标。

本机默认离线（只用本机 Go 模块缓存），不碰仓库文件（临时测试经 `go test -overlay` 注入 `node/protocol`、`subscription/render`、`node/service` 三个包）；CI 里用 `--online` 联网取模块。Go 构建缓存是热的时候，跑一次约 10 秒；第一次编译 sing-box（含 cronet）要慢得多。

## 步骤

1. 在仓库根（或 worktree 根）跑 `bash .agents/skills/subscription-e2e/scripts/run.sh --work <scratchpad>/sube2e`。
   - `--work` 指到会话 scratchpad；不给时用 `$TMPDIR/pandora-subscription-e2e`。
   - 要跑 mihomo E2E，先下载官方 mihomo 并核对下载包哈希（版本与下载包 SHA-256 与 `.github/workflows/subscription-e2e.yml`、`pandora-native.yml` 的 External Mihomo 一步相同），再设 `MIHOMO_BIN=<可执行文件>` 与 `MIHOMO_SHA256=<可执行文件本身的 sha256>`；没设 `MIHOMO_BIN` 时 mihomo E2E 列显示 `-`，设了 `MIHOMO_BIN` 没设 `MIHOMO_SHA256` 时 run.sh 直接报错退出。
   - 退出码 0 表示没有 FAIL，1 表示有。
2. 读矩阵：
   - 每个夹具一行，列依次是 clash、premium、mihomo E2E、sing-box 校验、sing-box E2E、uri、下发。
   - 有 FAIL 或 WARN 时看下面的「明细」。sing-box 用例在 `<work>/e2e/<id>.client.json`，mihomo 用例在 `<work>/e2e/<id>.mihomo.yaml`（日志 `<id>.mihomo.log`），都可以拿去手跑。
3. 修了再跑，直到没有 FAIL。
   - 新出现的 skip 要在 `panel/internal/module/subscription/render/matrix_test.go` 里有登记的跳过原因，否则就是渲染器悄悄丢了节点。
4. 交付报告里贴矩阵的汇总行和明细。
   - 写清哪几列是真连通（sing-box E2E、mihomo E2E），哪几列只是静态核对（clash、premium、uri）。

### 比修前修后

1. 把基点导出成快照放进 scratchpad：`git archive <基点> panel pdnd nodeapi | tar -x -C <scratchpad>/base`。不要用 `git worktree add`，那会改仓库的 worktree 登记。基点必须是新栈（有 `panel/internal/module/node/protocol`），旧栈提交跑不了。
2. 用当前的夹具文件跑基点：
   ```
   run.sh --repo <scratchpad>/base --work <scratchpad>/sube2e-before \
     --fixtures-file panel/internal/module/node/protocol/kernel_config_parity_test.go \
     --extra none
   ```
   - `--fixtures-file` 会替换基点 `node/protocol` 包里的 `kernel_config_parity_test.go`（表单夹具 `parityFixtures()` 在这里）。它只参与编译，注入的测试只取 `form` 组。
3. 修后的结果用主线或分支跑一遍，两张矩阵对照。

### CI

- `.github/workflows/subscription-e2e.yml` 在推 `main`、`feat/panel-redesign**` 与 PR 时、改到订阅渲染 / 协议 schema / 节点配置文档 / pdnd kernel 与 internal / pdnd go.mod / 本 skill / 该 workflow 时跑；
- 它下载钉版本、核对 digest 的官方 mihomo，跑 `run.sh --online --work "$RUNNER_TEMP/sube2e"`，矩阵贴进 job summary，`results/`、渲染输出与每个用例的客户端配置和 mihomo 日志作为 artifact 上传；
- `--online`：不设 `GOPROXY=off`（联网取模块），`GOTOOLCHAIN=local`（用 setup-go 装好的 go.mod 版本），只给 CI 用，本机不要加；
- mihomo 版本升级：与 `pandora-native.yml` 的 External Mihomo 一步成对改版本和下载包 SHA-256。
- `--no-naive`：只在 CI 链接不了 cronet 时加；这时 naive 行的 sing-box 两列和（含 naive 出站的）ALL 行 sing-box 列显示 `-`，明细写明「构建不带 cronet」，不算 FAIL，但也就没有 naive 的覆盖。

### 其他用法

- **额外夹具**：`fixtures/extra.json` 每次默认都跑。格式是 `[{id, type, port, config}]`。
  - 想试一个还没进 `parityFixtures()` 的 form 组的形状，就另写一个文件用 `--extra` 传入。
- **按某个客户端的 UA 渲染**：`--ua '<User-Agent>'`，只影响 sing-box 的版本写法（`singbox_template.go`），缺省空是兼容写法。
- **只跑几个 E2E 用例**：`--only vless-ws,trojan-tls-grpc --log debug`，sing-box 客户端日志打到 `results/e2e.stderr`。
- **REALITY 密钥**：`cd tools && go run ./kp` 生成一对新密钥，`go run ./kp <私钥>` 核对私钥和公钥是否成对。

## 脚本与文件

`scripts/run.sh` 一条命令跑完（参数见文件头），`matrix.py` 拼矩阵，`fixtures/extra.json` 是常备额外夹具。

`tools/go.mod` 自己 require sing-box（pdnd 自 2026-10-14 起不依赖 sing-box，版本不必再对齐）。两条 `replace` 指向 `../../../../pdnd` 与 `../../../../nodeapi`，即本仓库的 pdnd 与 nodeapi（pdnd 自己对 nodeapi 的 replace 不会传递过来）。run.sh 会把 tools 拷到工作目录，再把两条 replace 改成 `--repo` 下的目录，所以测 worktree 或基点快照时不用改 skill。

构建 tags 固定为 `with_quic,with_utls,with_gvisor,with_naive_outbound`，写在 run.sh 里。

全程只用本机 Go 模块缓存（`GOPROXY=off`），不要为它打开联网下载（`--online` 例外，只给 CI 用）。Go 版本由 `scripts/toolchain.sh` 从 panel 与 pdnd 的 go.mod 取较高者设 `GOTOOLCHAIN`（本机缺省 1.27.1 下，本目录 `tools` 模块依赖的 sing-box 链接失败，见根 CLAUDE.md「环境与工具坑」）；本机不要改回 `GOTOOLCHAIN=local`，也不要加 `GOSUMDB=off`（与 `GOPROXY=off` 一起会让缓存里的工具链校验报错）：
- 编译报模块缺失，说明 pdnd 自己的依赖还没在本机拉齐。先按 verify 流程把 pdnd 构建通过，再回来跑。
- pdnd 升级依赖后，run.sh 会在工作副本里离线补齐 go.mod，并提示把 `go.mod`、`go.sum` 拷回 skill。拷回时 replace 行要改回相对路径。

## 矩阵怎么读

- **OK**：写出了节点，检查通过。E2E 两列的 OK 是真连通。
- **skip**：渲染器没写出这个节点（计数为 0），文件本身仍然合法。
- **SKIPPED**：mihomo 自己明确报不支持的组合（登记在 `tools/e2e/mihomo.go` 的 `mihomoUnsupported`，如 REALITY + XHTTP/H3），不算失败也不算通过，明细写原因。
- **WARN**：静态检查有疑点。字段缺了客户端不会报错，但那个节点连不上。
- **FAIL**：解析、校验或启动失败；或 E2E 不通；或渲染与 E2E 的结论对不上（订阅里有这个节点，E2E 却找不到）；或表单夹具没过后台校验。
- **-**：这一项没跑。
- mihomo E2E 列与 sing-box E2E 列规则相同，计数看 clash 列；`skip` 是渲染器没把节点写进 Clash（naive、juicity 等，原因在 `render/matrix_test.go`）。
- 「下发」列是 `buildNodeConfigDoc` 的结论。订阅里给了、节点端却被拒的，E2E 记 `NODE-CONFIG-REJECTED`，算 FAIL。
- ALL 行是整份订阅。sing-box 里只要有一个节点起不来，整份配置都会失效，所以 ALL 行要单独看。

## 坑

协议渲染本身的规则见 `.claude/rules/subscription-render.md` 与 `pdnd-kernel.md`；下面只记这套工具的坑。

- **夹具必须是后台表单写进库的形状**，不是内核扁平形状：库里存表单形状，订阅与下发都经 `protocol.KernelConfig` 翻译。表单夹具在 `node/protocol/kernel_config_parity_test.go` 的 `parityFixtures()` form 组，它同时是 KernelConfig 对旧实现逐项对照的输入，加夹具要连 `subscription/render/testdata/nodes.json` 一起考虑。额外夹具的「来源」列会标出没过后台校验（`protocol.Validate`）的，扁平形状只用作回归组。
- **naive 要带构建 tag**：sing-box 要带 `with_naive_outbound` 构建（cgo 加 cronet），否则报 `naive outbound is not included in this build`，naive 行和 ALL 行都变 FAIL。`--no-naive` 只在 cronet 链接不了时用，这时矩阵把这几格记为 `-`（见「CI」一节）。
- **ShadowTLS 的 E2E 前提**：E2E 只把外层的 `server` 换成本地站点，表单没写出 `server` 时记 `E2E-PRECONDITION`。
- **有些协议没有 E2E 覆盖**：mieru、socks over TLS 在 mihomo E2E 里有真连（如果 Clash 订阅写出了它们），sing-box E2E 仍然没有；xhttp 若 mihomo 写出了也会真连；mKCP、juicity 两边都没有 E2E。
- **sing-box 订阅带 TUN、远程规则集与 cache_file（w5retain 起）**：原样 Start 要 root 建 TUN、要联网下载规则集。
  - `sbcheck` 先对原样配置跑 `box.New`（TUN 与规则集的写法照样校验），`-start` 时 Start 的是离线副本：去掉 TUN 入站与 cache_file，远程规则集换成同 tag 的内联规则集，`download_detour` 另行核对必须指向存在的出站。
  - 真客户端第一次启动时规则集下载失败（经「节点选择」下载，节点不通）整份配置起不来，下载成功一次后靠 cache_file 缓存。这一条这里测不到，上真机验。
  - E2E 只取订阅里的出站，自建入站，不受模板影响。
- **pdnd 默认拒绝回环与内网目标**（w4kernel 起，`outbound.SetBlockPrivateDestinations`）：E2E 的目标站点在 127.0.0.1，run.sh 在被测仓库有这道闸时往工作副本的 e2e 里加一个 `init` 放开它；不放开时全部用例报 EOF / SOCKS general failure。
- **E2E 环境注入了信任，通了不等于真环境能用**：
  - 客户端信任本次的自签证书；
  - REALITY 的 dest 和 ShadowTLS 的握手站点换成了本地 TLS 站点；
  - `node.example.com` 经 hosts DNS 指到 127.0.0.1。
  - mihomo 不能加 CA，非 REALITY 的 TLS 和 ShadowTLS 的 plugin-opts 加 `skip-cert-verify: true`（只在测试环境）；`server` 保持 `node.example.com`，用 mihomo 配置里的 `hosts` 指到 127.0.0.1；端口一样 +20000。
  - 自签证书、缺 SNI、server_host 是 IP 的节点，这里能通，真环境里不一定。
  - 修前 naive「通」就是这个原因：面板根本没下发 insecure。上线前仍要上真节点验。
- **clash、premium、uri 三列只是静态核对**：Clash 订阅的真连看 mihomo E2E 列（只测 `.clash`，即 Meta 格式，mihomo 客户端按 UA 拿到的就是它；`.clash-premium` 是 Premium 内核的子集，Premium 内核闭源停更，这里只做静态核对）。
  - mihomo 对空的 `proxies` 组会整份拒载，`yamlcheck` 查这条。
- **overlay 依赖包内的名字**：`node/protocol` 的 `parityFixtures`、`parityRealityPri/Pub`、`Validate`、`KernelConfig`、`CanonicalType`、`schemaVersion`；`subscription/render` 的 `RenderRequest`、`Request`、`Node`、`fixtureUUID`、`fixtureHost`；`node/service` 的 `buildNodeConfigDoc`。这些改名后 overlay 要同步改，否则第 1 步编译失败。overlay 的路径必须是绝对路径；测试读环境变量和文件，所以一律 `-count=1`。
- **夹具 id 不能叫 ALL 或 EMPTY，也不能重复**。端口加 20000 后不能超过 65535。用例串行跑，上一个用例是异步关闭的，所以端口别复用。
- **夹具里不写密钥**：REALITY 用占位符 `@REALITY_PRIVATE@` / `@REALITY_PUBLIC@`，注入的测试把它换成 `node/protocol` 包里那对测试专用的虚构密钥。要加新密钥，用 `kp` 生成，再按完整值登记进 `.gitleaks.toml` 的放行清单。
- **skip 本身不算失败**，但每个 skip 都要在 `render/matrix_test.go` 有原因。渲染矩阵测试是事实清单，这张矩阵是它的实连旁证。

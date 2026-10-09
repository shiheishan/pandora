---
name: deps-upgrade
description: pandora 的依赖与工具链升级和漏洞扫描：govulncheck 扫 panel、pdnd、subscription-e2e/tools 三个模块（只看被调用的，另出只含发布物的视图）；升 Go 补丁版或小版本时要同步的全部位置；升级后必须重点跑的测试（防探测回落、REALITY H3、interop、runtime-acceptance、PG18）；go mod tidy 与新依赖审查；npm audit 与前端依赖；pdnd 里 fork 来的代码合上游；interop 与真机客户端的版本和 SHA 在哪改。用户或总协调说「扫漏洞」「govulncheck」「升 Go」「升依赖」「go.mod 不 tidy」「引入新库」「npm audit」「合上游」「升 Xray / sing-box / mihomo 客户端」时使用。改完后的常规本地检查与等 CI 见 verify，CI 红了见 ci-triage。
---

# 依赖与工具链升级

本 skill 管「改哪些地方、额外跑哪些测试」；常规本地检查和推送后等哪个结论照 verify skill，CI 红了用 ci-triage 分流。

背景：CI 不跑 govulncheck，也没有依赖更新机器人，漏洞全靠手动扫。d157159 把 go 指令从 1.26.5 升到 1.26.9 后，pdnd 的 `TestNaiveProbesServeFallback` 因标准库行为变化变红。所以每次升级都要按第 3 节先跑基线、再比对。

## 1. 扫漏洞

```bash
bash .claude/skills/deps-upgrade/scripts/vulncheck.sh [输出目录]   # 会话里输出放 scratchpad
```

- 要联网：拉 govulncheck、漏洞库和缺的工具链。沙箱里跑不通就关掉沙箱。
- 退出码：0 表示没有被调用的漏洞，3 表示有，1 表示扫描出错。
- 输出分两段，每条漏洞列出编号、模块版本、修复版本和第一条调用链：
  1. 三个模块各跑一次 `./...`。
  2. 发布物视图：`GOOS=linux` 下只扫 panel 的 `./cmd/...`，以及 pdnd 的 `.` 与 `./cmd/pandora-h3-probe`。
- 脚本替你避开了三个坑：
  - **工具链**：本机默认的 Go 比 CI 新（10-09 本机 1.27.1，CI 1.26.9）。不设 `GOTOOLCHAIN=go<go.mod 版本>`，govulncheck 会按本机版本报标准库漏洞，和 CI、发布物对不上。
  - **tools 模块不 tidy**：`subscription-e2e/tools` 经 replace 跟随 pdnd，pdnd 的依赖一变它就不 tidy，`-mod=readonly` 下根本加载不了。脚本用 `-mod=mod` 扫，扫前备份 go.mod 和 go.sum，扫后还原。
  - **GOOS=linux 下的 `go run`**：会编出本机跑不了的 govulncheck，所以脚本先 `go install` 成文件再跑。

怎么读结果：

- 「被调用」指 `=== Symbol Results ===` 里的条目，是 govulncheck 默认文本输出的唯一内容。只 import 未调用、只 require 未 import 的，只在末尾计数，加 `-show verbose` 才列出来。
- **先看发布物视图**，它就是会上线的代码。
  - 第一段里调用链从 `tools/`、`core/xray`、`core/sing`（只有 `-tags compat` 才链进来，见 `rules/pdnd-build-boundary.md`）或测试辅助进来的，不上线。照样要升，但不算紧急。
  - 调用链经接口方法分派时是保守估计，像 `io.WriteString → http2.responseWriter.WriteString` 这类。判断能不能真触发，要看完整调用链：`grep -n -A40 '<GO-编号>' <输出目录>/<名字>.txt`。
- 「Found in: …@go1.x」是标准库的漏洞，修法是升 Go（第 2 节）。模块的漏洞就升那个模块（第 4 节）。
- **govulncheck 看不到 fork**：fork 进仓库的代码不是模块依赖，漏洞库对不上。下面这些上游出公告时，要人工去对应的 fork 目录里查有没有同样的代码：

  | 上游出公告的 | 要查的 fork 目录（都在 pdnd/） |
  |---|---|
  | crypto/tls（标准库） | `internal/reality`（XTLS/REALITY，本身基于 crypto/tls）。升 Go 不会顺带修到它 |
  | quic-go | `internal/realityquic` |
  | sing-shadowtls | `internal/nativewire/shadowtls` |
  | sing-anytls | `internal/nativewire/anytls` |
  | sing-quic | `internal/nativewire/hysteria2`、`internal/nativewire/tuic` |

  fork 清单与约束见第 6 节。
- 10-09 在 c257146+tidy 上的基线（go1.26.9）：
  - panel：0。
  - pdnd：`./...` 18 个，发布物 6 个（x/net v0.52.0 → v0.60.0，x/text v0.35.0 → v0.39.0）。
  - tools：15 个。
  - 下次扫完对照这个基线，看是新增还是旧账。

## 2. 升 Go 版本

**机制**：
- CI 的 setup-go 按 `go-version-file` 读 go.mod 的 `go` 指令，装的就是这个精确版本；go.mod 里没有 `toolchain` 行。
- 本机与测试机在 `GOTOOLCHAIN=auto` 下取「本机版本」和「go 指令」中较高的那个。
- 所以升补丁版的本质是抬 go 指令。测试机、检查机按系列装最新补丁，会自动跟上。

要改的位置如下表。用这条命令复核，fork 目录里的上游注释不用管，`platform.sh` 的数字比较 grep 不到，要单独看：

```bash
git grep -nE 'go1\.2[0-9]|golang:1\.|GO_SERIES|Go 1\.2[0-9]|go 1\.2[0-9]' -- ':!pdnd/internal/reality*' ':!pdnd/internal/nativewire'
```

| 位置 | 补丁版（1.26.x） | 小版本（→1.27） |
|---|---|---|
| `panel/go.mod`、`pdnd/go.mod` 的 `go` 指令 | 改 | 改 |
| `.claude/skills/subscription-e2e/tools/go.mod` 的 `go` 指令：不能低于 pdnd，在该目录 `go mod tidy` 会自动抬 | 改 | 改 |
| `.claude/skills/node-accept/SKILL.md` 构建命令里的 `GOTOOLCHAIN=go1.26.x` | 改 | 改 |
| ops-local 的压测 harness（`nodeaccept/harness-src`、`nodescale/harness-src`、`vpcnode/harness` 的 go.mod）：不入库，replace 到 pdnd，用前在各自目录 `go mod tidy` | 用前 tidy | 用前 tidy |
| `.claude/skills/test-machine/scripts/install-toolchain.sh` 的 `GO_SERIES` 默认值与第 3 行用法注释；test-machine SKILL.md「装与 CI 同小版本的 Go（go.mod 的 1.26 系列）」 | 不用改：按系列取最新 | 改 |
| ops-local `memoh-ci/bootstrap.sh` 的 `GO_SERIES` 与第 9 行注释（检查机，不入库） | 不用改 | 改，并在检查机重跑 bootstrap |
| `panel/deploy/test-*-pg18.sh`：6 个文件的 `GO_IMAGE` 默认 `golang:1.26`，以及 checkout-atomic-00039、dashboard-performance、dashboard-read-models、node-config-legacy（两处）、order-release-00040（两处）的 `go1.26.*` 判断。这些单跑的 runner CI 不调；CI 的 panel-pg18 走 `run-pg18-gates.sh`，它的 `require_go_version` 按 go.mod 的主次版本动态比，不用改 | 不用改。注意本机 docker 里缓存的 `golang:1.26` 可能是旧补丁，先 `docker pull` | 改，否则手动跑这些 runner 会直接退出 |
| `panel/deploy/platform.sh` 的 `pandora_go_version_ok`（`-ge 26`）、`build-release.sh:34` 与 `migrate-to-new-host.sh:74` 的「Go 1.26+」提示 | 不用改 | 改，跟 go 指令的最低小版本一致 |
| `README.md:9`、`README.md:215`、根 `CLAUDE.md:5` 的「Go 1.26」 | 不用改 | 改 |
| `.github/workflows/*.yml`：全部用 `go-version-file`，没有写死版本 | 不用改 | 不用改 |

**其他注意**：
- 小版本升级要在新版本下跑一遍 `go vet ./...`：新版本可能带新的 vet 检查，CI 的 vet 会跟着变红。
- subscription-e2e 用 `GOTOOLCHAIN=local` 离线跑。go 指令一旦高过本机 Go，它就跑不了，要先升本机 Go。

## 3. 升级后重点跑的测试

**先跑基线**：在升级前的提交上，用旧工具链跑一遍下面这些测试，记下哪些本来就红。然后换上新版本再跑，两边对比。

本机 Go 比 CI 新，所以复现 CI 一律显式写工具链：

```bash
GOTOOLCHAIN=go<旧版本> go test ...    # 升级前的基线
GOTOOLCHAIN=go<新版本> go test ...    # 升级后
```

**对标准库敏感的地方**：Go 补丁版常改 net/http 的 CONNECT/HTTP2 处理、crypto/tls 的握手细节，下面这些代码最容易被波及：

| 地方 | 依赖的标准库或 x/net 行为 | 守卫 |
|---|---|---|
| naive 入站：`kernel/naive.go`（`http2.ConfigureServer`） | net/http 的 CONNECT 与 HTTP/2。1.26.9 后，错误口令的 CONNECT 回 502，不再回落 | `kernel/probe_resistance_test.go` 的 `TestNaiveProbesServeFallback` |
| 防探测回落：`kernel/probe_fallback.go`（`http2.Server.ServeConn`）、`probe_fallback_http.go`（`httputil.ReverseProxy`） | 回落站点的 HTTP/1.1、HTTP/2 响应形状 | 同一文件里的其余测试：Reality / TLS / Trojan 回落、`TestParseProbeFallback` |
| ShadowTLS 诱饵中继 | TLS 记录层与超时 | `internal/nativewire/shadowtls/service_test.go` |
| REALITY over QUIC / H3 | `internal/reality` 与 `internal/realityquic` 对标准库 crypto 的引用 | `kernel` 的 `TestNativeRealityH3RoundTrip`、`cmd/pandora-h3-probe` 的 `TestProcessProbeRoundTrip` |
| XHTTP 服务端：`kernel/xhttp_server.go` | net/http 的 HTTP/2 响应写出 | `-tags interop` 的 Xray XHTTP 测试 |
| 面板网关流式响应：`panel/internal/platform/server/stream.go`；证书签发 HTTP 客户端：`domain/certs` | net/http | panel 全量单测与 PG18 |

**本机命令**（在 `pdnd/` 下；不要和 `npm ci` 并发）：

```bash
go test -count=1 ./kernel/ ./internal/reality/... ./internal/nativewire/... ./cmd/pandora-h3-probe/
go test -count=1 -tags interop -run 'TestExternalXray|TestAnyTLSNativeClientTCPAndUOTUDP$' ./kernel   # 本机可跑，约 3 秒；CI 的确切 -run 见 pandora-native.yml
go test -count=1 -tags compat -p 1 ./...                                                     # compat 构建也会被依赖升级波及
```

**只在 CI 跑的**：
- 推送后照 verify skill 等 `wait-status.sh` 和 `wait-github.sh`。
- runtime-acceptance（发布二进制在 Linux 上冷启动、SIGTERM 收尾）、ARM64 race、PG18 全部域门禁只有 GitHub 跑，升 Go 或升 pdnd、panel 依赖都要等到 `wait-github.sh` 退出 0。

**红了怎么判**：
- 用 ci-triage 分流。
- 只在新版本红、基线绿的，是升级引起的：修代码适配，或者说明理由后回退版本，不要放宽断言。
- 基线就红的，是旧账，在报告里写明，不归本次升级。

## 4. Go 依赖：升级、tidy、新依赖审查

**升级**：
- 在模块目录 `go get <模块>@<版本>`，然后 `go mod tidy`。看 `git diff go.mod`：会顺带抬哪些间接依赖（例如 w9cert 引入 lego 时一起抬了 x/crypto、x/net、x/text）。
- pdnd 的依赖一变，再到 `.claude/skills/subscription-e2e/tools` 跑一次 `go mod tidy`（联网）。它经 replace 跟随 pdnd，sing-box 版本也要和 pdnd 一致（见 subscription-e2e skill）。这一步顺便把新版本拉进本机模块缓存，subscription-e2e 离线跑（`GOPROXY=off`）才找得到。
- 升完重跑 `vulncheck.sh`，确认目标漏洞消失，也没有新增。

**tidy 检查**：
- CI 不查 tidy，三个模块各跑一次 `go mod tidy -diff`，没有输出才算 tidy。
- 10-09 前的教训：pdnd 一度把 xtls/reality、x/time 标成 indirect（实际是直接 import），go.sum 里还留着换版前的 sing-anytls v0.0.11。
- tools 模块在 10-09 仍不 tidy（sing-anytls 应为 v0.0.13），下次动它时一并 tidy。

**新依赖审查**（参照 w9cert 引入 lego）：
- 只 import 用到的子包。例如 lego 只引 cloudflare、alidns、tencentcloud 三个提供方，不引全量 providers。版本锁死。
- **它会不会自己读进程环境变量**：panel 里只有 platform/config 能读环境变量，守卫 `envaccess_test.go` 只扫本仓库代码，管不到第三方库。库会读的变量要登记，先例是 `panel/internal/platform/config/acme.go` 的 `legoEnvExact` 及其启动告警；文档写进 `docs/node-certificates.md`。
- 日志接到 slog：先例是 `domain/certs/issuer.go` 把 lego 的 `[WARN]`、`[INFO]` 前缀分别映射成 Warn、Info。HTTP 客户端能不能注入，决定了超时、代理和测试怎么做。
- 许可证：AGPL 不能链进二进制（先例：juicity 只能跑在进程外，见 `rules/pdnd-build-boundary.md`）；GPL 的 fork 要保留 LICENSE。
- pdnd 的默认构建只许 NativeCore：新依赖不能把 `core/xray`、`core/sing`、sing-box 拉进 `go list -deps .`。CI 的「Default dependency boundary」步骤会拦，本机先自查。
- 引入后跑一遍 `vulncheck.sh`，结果写进任务报告。

## 5. npm 依赖

- 约束见 `rules/frontend-build-csp.md`：
  - 运行时依赖只有 4 个；
  - `package.json` 精确锁版本，不写 `^` 或 `~`；
  - 只用 `npm ci` 装；
  - typescript 停在 6.0.x。
- 扫描在 `panel/frontend/` 下：
  - 先跑 `npm audit --omit=dev`，它只看进页面的运行时依赖，这一项必须为 0；
  - 再跑 `npm audit`：开发依赖只在构建机、CI 上运行，按严重度排期。10-09 的结果：运行时 0；开发依赖 1 个 high（source-map-js，经构建链进来）。
  - `.claude/skills/web-perf/` 有自己的 package-lock，同样扫。
- 升级流程：
  1. 用 `npm install <包>@<版本> --save-exact` 改 package.json 与 lock；
  2. 再 `npm ci`，确认 lock 能复现；
  3. 按 verify 跑 lint、typecheck、test；
  4. 推送后等 `wait-github.sh`，里面有双入口构建、冒烟和 Playwright。
- 升 `@playwright/test` 时，CI 浏览器缓存的键会跟着变，第一次会慢。
- `npm ci` 不要和 `go build`、`go test` 并发（见根 CLAUDE.md「环境与工具坑」）。

## 6. fork 合上游

pdnd 里共有 6 处 fork：

- `rules/pdnd-forks.md` 管 3 处：`internal/reality`（XTLS/REALITY）、`internal/realityquic`（apernet/quic-go）、`internal/nativewire/shadowtls`（sagernet/sing-shadowtls）。
- `internal/nativewire/README.md` 另记了 3 处：`anytls`（sing-anytls）、`hysteria2` 与 `tuic`（sing-quic）。这 3 处不在规则的 paths 里，改它们时不会自动加载规则。

各 fork 都**没有记录上游基点**。go.mod 里的同名模块只是对照测试或 compat 构建在用，比如 `TestRealityProbesFallBackToDestLikeUpstream` 用 xtls/reality 作对照。它们的版本只能当线索，不能当基点。

步骤：

1. **定基点**：拿 fork 目录与候选上游版本逐文件 diff，差异最小的那个就是基点。把本仓库加过的改动列出来，从 `git log -- <目录>` 找：
   - reality：拒绝的握手转发到 dest（d3119d3）、Vision 切原始 socket（eb24309）；
   - shadowtls：认证前限时（f8173f0）；
   - hysteria2 / tuic：用户表同步发布、`dgram` 分片、TUIC UDP 热路径（74d7256）；
   - anytls：口令查找加锁。
2. **合**：把上游「基点..目标」的 diff 打进 fork 目录，保持上游的文件划分（这两个目录整目录豁免 800 行规则）。
   - realityquic 只改 import 路径和 `go:generate` 的包路径，网页链接仍指向 quic-go（见目录内 README）。
   - 许可证文件原样保留。
3. **同步对照模块**：go.mod 里对应的上游模块一起升，否则对照测试比的还是旧上游。比如合 REALITY 就一起升 `github.com/xtls/reality`。
4. **测**：
   - `TestNativeRealityH3RoundTrip`、`TestProcessProbeRoundTrip`；
   - `kernel/probe_resistance_test.go`、shadowtls 的 `service_test.go`；
   - `-tags interop` 的 AnyTLS 与 Xray 组；
   - 改到 hysteria2 / tuic 的，还要跑 `internal/nativewire/...`，并按 node-accept 在 Linux 上复测 UDP。
5. **提交说明写上游基点和目标 commit**，下次合上游就有据可查。

## 7. interop 与真机客户端的版本和 SHA

| 客户端 | 版本在哪 | 谁在用 | 升级时同步 |
|---|---|---|---|
| Xray（作 Go 模块） | `pdnd/go.mod` 的 `xtls/xray-core` | CI interop（`pandora-native.yml` 的 `-run` 正则）、compat 构建 | 新加的 Xray interop 测试要补进 `-run`（`rules/pdnd-kernel.md`） |
| sing-anytls 客户端 | `pdnd/go.mod` | CI 的 AnyTLS interop | yaml 注释里写着版本，要一起改 |
| sing-box（作 Go 模块） | `pdnd/go.mod` 与 `subscription-e2e/tools/go.mod`，两处必须一致 | subscription-e2e | tools 模块跑 tidy |
| mihomo、sing-box、Juicity、Naive 外部二进制 | 不入库，跑测试时经 `*_BIN` + `*_SHA256` 环境变量传入（`interop_mihomo`、`interop_external` 标签） | 本机或测试机手动门禁 | SHA 用 GitHub release 页公布的值，不用自己下载后算出来的；`pdnd/release/README.md` 里的 mihomo 实测记录是历史结论，换版本后要重测 |
| node-e2e 真客户端（sing-box、mihomo、Xray、Juicity、mieru） | ops-local `vultr-test2/node-e2e/scripts/install-clients.sh` 里的版本变量和逐个写死的 SHA-256 | node-e2e skill | 版本与 SHA 成对改，sing-box 用 `-glibc` 包 |
| 官方 pdnd 发布包 | 面板 `release-artifact.env` 钉的版本与 SHA | panel-install、node-e2e 的接入证据 | 见 panel-install |

## 坑

- 本机 Go 与 CI 不同，「本机过了」不代表 CI 过。复现 CI 或扫漏洞时都要显式写 `GOTOOLCHAIN=go<go.mod 版本>`，切换要联网下载工具链。
- `GOFLAGS=-mod=mod` 跑 tools 模块会改写它的 go.mod 和 go.sum。扫描时用脚本（会还原），要改就正式 `go mod tidy` 并提交。
- 升级的提交只放版本改动和必要的适配。顺手修的旧账（如 tidy）单独提交，验收时才能把升级引起的红和旧账分开。

# 面板重构 · 第 5 阶段 · 超长文件重构

> 协调会话写于 2026-09-26。worktree `../pandora-refactor`，分支 `feat/panel-redesign-refactor`，不写迁移。
> 先读 `phase3-common.md`（第 1–12 节，尤其第 12 节的文件分界），再读本文件。

## 目标与授权

项目规则是单文件 ≤800 行。用户 2026-09-26 **授权重构超限文件，代码与文档一起改**。协调会话调查的现状：

- 超 800 行的非测试 `.go` 共 36 个：panel 17 个（业务 12、Linux 平台层 5）、pdnd 自研 5 个（`kernel/` 协议）、移植栈 14 个（`pdnd/internal/reality` 是 crypto/tls 的 fork，`pdnd/internal/realityquic` 是 quic-go 的 fork）。另有 5 个超长测试文件。根 `CLAUDE.md` 里「41 个，多为移植的 TLS/QUIC 栈」不准确。
- **真正的难点不在拆，在测试**：42 个测试按文件名读 Go 源码做契约检查（多是「从函数 A 到函数 B」截一段源码窗口再断言），函数一挪文件就红。其中 `domain/subscription/heartbeat_delivery_test.go` 约 125 行是**否定检查**，目标函数挪走后会静默失效、永远通过。`platform/httpx/message_zh_contract_test.go` 的豁免表按「文件 → 函数」登记（`domain/nodefabric/service.go`、`api/public/handlers.go` 等）。

## 铁律（本会话专属，与第 1–11 节冲突时以这里为准）

- **只挪代码，不改行为**：不改任何函数体、不改名（导出与未导出都不改）、不改签名、不合并重复代码、不顺手修 bug。发现问题写进报告，协调会话另派。
- **每个包一个提交**（单个包太大可拆两个），提交里只有「挪动 + 新文件的 L3 头 + L2 成员清单」。
- **纯挪动要自证**：写一个小脚本（放 scratchpad 或 `tools/`，你定），用 `go/ast` 对每个改动的包收集拆分前后全部顶层声明、按名字对齐、比较 gofmt 后的源码，**两边集合必须完全相同**（只允许 import 块与文件头注释变化）。每个提交的报告里附上这个比对结果。
- 新文件：照包里已有的命名习惯（例如 admin 包已有 `node_routing.go`、`pools.go`，public 包已有从 `handlers.go` 拆出的 `my_subscriptions.go`，adminops 已有从 `service.go` 拆出的 `users.go`）；**新文件不能叫 `router_*.go`**（`api/admin/router_source_test.go` 会把它收进路由源码）；带 `//go:build` 的文件拆出去的新文件要复制同样的构建约束。每个新文件写 L3 头（`[INPUT]`/`[OUTPUT]`/`[POS]`/`[PROTOCOL]`），所在目录的 L2 成员清单同步；没有 L2 的目录（`middleware`、`ca42runner`、`cmd/pandora-cic-journal`）顺手补建。
- **文件分界**：`phase3-common.md` 第 12 节列出的文件，在后端四对应步骤合入主线之前不碰。步骤顺序已按这个排好。
- 每个提交：本机 `go build ./... && go vet ./... && go test -p 1 -count=1 -timeout 15m ./...`；Linux 专属文件本机是 darwin 编不到，另跑 `GOOS=linux GOARCH=amd64 go vet ./...`（pdnd 另加 `GOARCH=arm64`）。推送后看三组 CI，PG18 必须 0 SKIP。

## 步骤（每步做完停下报告）

| 步 | 内容 |
|---|---|
| ① 测试读源码去文件名化（只改测试） | 加一个测试用的源码定位 helper（放 `internal/platform/` 下新包，或各包 `_test.go`，你定并说明）：能按包读全部非测试源码，最好能用 `go/ast` 按函数名取函数源码。把上面那 42 个按文件名读 `.go` 的测试改用它，**每个断言的强度不能变弱**：原来是「A 到 B 的窗口里必须有 X」的，改成对相应函数的源码断言；挑三四个做变异验证（临时改坏被测代码，确认测试会红，再恢复）。`heartbeat_delivery_test.go` 那条否定检查先断言目标函数存在再做否定。`message_zh_contract_test.go` 的豁免表改成按「包 + 函数名」登记（豁免清单过期即红的机制保留）。这一步不动任何非测试代码。 |
| ② 拆不与后端四冲突的包 | `nodefabric`（`service.go` 1675、`protocol_schema.go` 1333、`uniproxy.go` 1036、`node_admin.go` 1014）、`support/service.go` 1365、`adminops`（`catalog.go` 1010、`service.go` 838）、`middleware/idempotency.go` 1081、`api/public/handlers.go` 1068、Linux 平台层（`cmd/pandora-cic-journal/main_linux.go` 1453、`platform/releasejournal/store_linux.go` 1219、`ca42runner` 三个 811–903）。协调会话的调查给过切分建议，供参考：`support/service.go` 已有分节注释，可按用户侧工单、客服侧工单、升级三块拆；`protocol_schema.go` 保留 schema 与脱敏，校验拆出去；`uniproxy.go` 把配置构建挪到 `uniproxy_config.go`（包里已有同名测试）；`node_admin.go` 拆出克隆 / 迁移 / 排序与生命周期；`catalog.go` 把版本、价格、归档拆到 `catalog_version.go`；public `handlers.go` 把套餐目录与邀请佣金拆出去。两个 Linux 包里有一批重复的文件系统辅助函数，**不合并**（安全关键）。 |
| ③ 后端四合入后再拆 | 等协调会话通知后端四 ⑧–⑪ 已合入主线，先 `git merge feat/panel-redesign`，再拆 `billing/checkout.go` 1908、`billing/release.go` 808、`api/admin/handlers.go` 1568。`checkout.go` 里 `HandlePaymentWebhook` 到 `fulfillOrder` 这一段是结算主链，保持在同一个文件里。 |
| ④ pdnd kernel | `vmess.go` 1504、`vless.go` 935、`shadowsocks2022.go` 899、`proxy.go` 891、`shadowsocks.go` 838。照 `kernel/CLAUDE.md` 里 vless 已拆出 `vless_flow.go` / `vless_mux.go` / `vless_udp.go` 的做法。`pdnd/core/external/**` 不碰（后端四 ⑫）。pdnd 的 CI 包括 race、原生 ARM64、interop，全部要绿。 |
| ⑤ 超长测试文件 | `api/admin/node_config_legacy_pg18_test.go` 3731、`billing/order_release_pg18_test.go` 1831、`billing/settlement_pg18_test.go` 1541、`middleware/idempotency_pg18_test.go` 1297、`middleware/idempotency_test.go` 1260，按主题拆成同包多个文件。先确认 `deploy/run-pg18-gates.sh` 的门禁是按测试名还是按文件选的，拆完 PG18 的 PASS 总数必须与拆前相同。billing 两个等 ③ 之后。 |
| ⑥ 豁免、守卫与文档 | 根 `CLAUDE.md` 那一行改成准确数字，并明确写 `pdnd/internal/reality/**`、`pdnd/internal/realityquic/**` 是 fork 来的第三方代码、**豁免** ≤800 行（拆了难合上游），`pdnd/CLAUDE.md` 同步。加一道守卫：CI 里（或 panel、pdnd 各一个 Go 测试）扫描非测试与测试 `.go` 文件，除豁免目录外超过 800 行即失败，免得以后再长回去。 |

## 报告

每步报告写清：提交号、每个文件拆成了哪几个文件（新旧行数）、纯挪动比对结果、改了哪些测试（①）与变异验证、本机与三组 CI 结果（PG18 的 PASS / SKIP / FAIL 计数）、更新了哪些 L2 / L3。发现的 bug 或可疑代码单列，不修。

## 进度与补充事项（协调会话维护，接力的新会话从这里接上）

**进度**：① `a7a67c3`（合并 2f562c9；PG18 36213610655：232 PASS / 0 SKIP / 0 FAIL，NativeCore 36213610687、panel-smoke 36213610649 全绿；主线合并后 vet、linux vet、全量通过）已验收合入。**下一步 ②**（先做下面「② 之前」两件）。

补充事项（与上文冲突时以这里为准）：
- 契约修订已到 R117（本会话不改接口，一般用不到修订号）。
- ① 的验收结论：`platform/sourcetest`（只被测试引用，照 pg18test 先例，名字找不到或重名即失败）、窗口换成它恰好覆盖的声明、整文件「必须有」收窄到承载函数、「不许有」放宽到整包、heartbeat 否定检查先正向取 `handlers.nodeList`、豁免表改按「包目录 + 函数名」、打散验证（266 → 2932 个单声明文件全量通过）与 6 组变异验证，都认可。按文件名读源码的实际是 47 个（开工说明写的 42 不准），billing 的 7 个留到 ③。放宽到整包的几条通用字面量否定检查（`FOR KEY SHARE` 等）将来可能误报，但方向是变红不是静默，接受。
- **② 之前先做两件**：
  1. **工具进仓库**：纯挪动 AST 比对工具和打散工具放进 `panel/tools/refactorcheck/`（Go 程序，`go run` 使用，不被任何生产包 import，写 L2 与用法），协调会话验收时会用它复核。单独一个提交。
  2. **修一条近乎空转的断言**（只改测试）：`TestLegacyConfigPublishRiskReductionContract` 的锁序原来比的是 `lockLegacyConfigRelease` 函数体在文件里排在 `PublishConfig` 前面，这是排版不是调用顺序；改成比 `PublishConfig` 内部各加锁调用处的先后，做一次变异验证。单独一个提交。
- 后端四 ⑪ 可能也会改 `api/admin/handlers_test.go` 与 `message_zh_contract_test.go`：你的 ① 已先合入主线，它合主线时自己解决冲突，你不用管。
- 后端四同时在 `../pandora-be4` 做 ⑧–⑫，它要改的文件见 `phase3-common.md` 第 12 节，③ 之前别碰。

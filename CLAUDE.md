# Pandora Panel - Xboard 类代理订阅面板 + 自研 NativeCore 节点端

Go 1.26 + PostgreSQL 18 + Valkey 8 + React/TypeScript/Vite 面板前端（panel/frontend，按设计稿重写完成，嵌入 panel/web 后在两个网关根 / 下发）

<directory>
panel/ - 面板：public/admin/node 三个 HTTP 网关（节点接入由 pdnd 的 pandora-native 两阶段承担，面板不带节点代理），计费账本、节点编排、审计、安装发布链 (9子目录: cmd, internal, migrations, deploy, frontend, web, docs, tests, tools)
pdnd/ - Pandora node：NativeCore 数据面，一个二进制承载 13 个协议，兼容内核仅在 compat 构建下按需链接 (10子目录: kernel, core, internal, node, panel, outbound, route, release, cmd, tools)
docs/ - 全仓库级文档：配置签名密钥轮换、发布物绑定 (0子目录)
.githooks/ - 提交前闸门 pre-commit：gitleaks 按 .gitleaks.toml 与本机可选的 ops-local/gitleaks-private.toml 扫暂存区，未装 gitleaks 也拒绝提交；clone 后执行 git config core.hooksPath .githooks 启用 (0子目录)
ops-local/ - 被 git 忽略、只在维护者本机存在的私有运维资料：现有 memoh-ci/（CI 回放检查机的脚本与本机端 wait-status.sh、wait-github.sh，推送后用它们等结论）
  - 以后放真实服务器 IP、域名、gitleaks 私有规则 gitleaks-private.toml 也在这里
  - 仓库公开，这些永不入库
  - pre-commit 钩子在该文件存在时才加载它
.github/workflows/ - CI：默认 shell: bash（-eo pipefail，`| tee` 不再吞掉失败） (0子目录)
  - pdnd 的 Ubuntu race/vet、原生 ubuntu-24.04-arm 的 ARM64 race 门、-tags interop 的非 race 外部客户端门与 amd64/arm64 双架构构建门禁，release-manifest 任务对刚构建的 amd64 发布二进制跑 runtime-acceptance.sh（Go 模拟面板，signed 与 compat 两种接入各冷启动两次）
  - panel 的 nodefabric 契约、前端嵌入与根下发契约（占位入口）、表登记簿、权限字典
  - panel-frontend 任务对新前端跑 lint/typecheck/vitest/双入口构建，再 make frontend-embed 用真实产物跑 web、webapp、api 的 Go 契约，占位页未被替换即失败
  - PG18 集成门禁单独在 panel-pg18.yml（触发面是整棵 panel/internal 加 cmd 与 web 的 Go 源码，不拖 pdnd 的重任务），runner 自带 Docker 跑 run-pg18-gates.sh，goose 版本跟 build-release.sh
  - 同一 workflow 的 panel-unit 任务跑 panel 全量 build/vet/go test（PG18 用例在此跳过），这是 CI 上唯一跑 panel 全部单元测试的地方
  - panel-smoke.yml 是新前端对真实网关的联调冒烟（面板重构第 4 阶段），触发面含 panel/frontend/src 与 panel/tests，经 deploy/run-smoke-stack.sh 起一次性 PG18 + 网关，读表先于写路径，最后经 deploy/run-smoke-e2e.sh 在同一栈上跑 tests 下的六个 e2e 脚本（含内鬼检测评估 risk_e2e.sh；全部跑完再判，任一失败即 job 变红），再用 panel/tools/loadtest 做一次 200 用户、5 节点、60 秒的压测工具试跑（零 5xx、零签名失败）
  - panel-deploy.yml 按 panel/deploy 与 panel/migrations 路径触发，逐个点名跑无需数据库与 root 的 deploy 桩测试，迁移脚本拿真实迁移目录校验编号（严格递增、不重复、允许历史空号）
</directory>

<config>
README.md - 项目全貌：架构、功能、部署、验证状态、进度、路线图，给人看的唯一入口
panel/go.mod、pdnd/go.mod - 两个独立 Go module，面板为 github.com/aegispanel/aegis，pdnd 沿用旧 module 名 github.com/aegispanel/nodeagent
panel/Makefile - 本地开发入口：up/migrate/check-migrations/invariants/build/test/e2e/verify/frontend-check/frontend-embed，CGO_ENABLED=0
panel/deploy/.env.example - 运行配置模板，敏感项 CHANGE_ME 由 install.sh 首装生成
panel/deploy/docker-compose.yml - 本地数据基座 PostgreSQL 18 + Valkey 8，只绑 127.0.0.1:5433/6380
panel/migrations/RESERVED-TABLES.md - 迁移留存但 Go 从不引用的 21 张表及锁定原因，platform/db 契约测试按 Up 段重放守同构
pdnd/release/build.sh - Linux amd64/arm64 发布包与 SHA-256 manifest
pdnd/release/check_native_panel_parity.py - NativeCore/Panel Schema/serving allowlist 13 协议静态对齐检查
LICENSE - GPL-3.0 全文（GNU 官方 gpl-3.0.txt 原样），覆盖 panel 与 pdnd；fork 目录 pdnd/internal/reality、realityquic 保留各自的 LICENSE
.gitattributes - 全仓库 LF，仅 *.ps1 CRLF
.gitleaks.toml - 公开的泄露规则：gitleaks 内置规则 + Komari 密钥与后台隐藏前缀两种格式，误报按完整值放行；只写格式不写真实值
</config>

法则: 极简·稳定·导航·版本精确

---

# GEB 分形文档系统协议（本项目启用）

来源：https://chunxiang.space/geb-system 。按本项目需要精简，原文见来源链接。

## 交互

- 每次回答以「哥」开头。
- 思考用英文，交互用中文，代码注释用中文（可配 ASCII 分块）。

## 三层文档

代码与文档必须同构：任一方变化，另一方同步改，否则任务未完成。

| 层 | 位置 | 内容 | 何时更新 |
|---|---|---|---|
| L1 | 根 `CLAUDE.md` | 技术栈、顶级目录地图、关键配置 | 架构变更、顶级模块增删 |
| L2 | `{模块}/CLAUDE.md` | 成员清单与模块职责 | 文件增删、重命名、接口变更 |
| L3 | 文件头部注释 | INPUT / OUTPUT / POS 契约 | 依赖、导出、职责变更 |

L1 格式：`# {项目名} - {一句话定位}`、技术栈一行、`<directory>`（`{目录}/ - {职责} ({N}子目录: …)`）、`<config>`（`{文件} - {一句话用途}`）、末行 `法则: 极简·稳定·导航·版本精确`。

L2 格式：

```
# {模块名}/
> L2 | 父级: {父路径}/CLAUDE.md
成员清单
{文件}.{ext}: {职责}，{技术细节}，{关键参数}
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
```

成员清单必须完整，一行一文件。

L3 格式（其他语言换成各自的注释语法，Go 的写法见下方适配说明）：

```
/**
 * [INPUT]: 依赖 {模块/文件} 的 {具体能力}
 * [OUTPUT]: 对外提供 {导出的函数/组件/类型/常量}
 * [POS]: {所属模块} 的 {角色定位}，{与兄弟文件的关系}
 * [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
 */
```

L2、L3 都带上面这行 `[PROTOCOL]`，写法固定不变。

## 怎么写 L2 / L3

- 用架构师视角写：职责边界、依赖方向、数据流、为何这样设计。回答「它是什么、为何存在、与谁协作」，不罗列字段和函数签名。
- 每一句都必须是理解这个局部所必需的，删掉会丢信息；写不出这样的句子就不写。

## 工作流

- 进入一个目录工作前：先读它的 `CLAUDE.md`，再读要改文件的 L3 头部。
- 改完代码后：依次检查 L3 → L2 → L1，与代码不符的当场改掉。

## 规则

- 新增、删除、重命名文件，同一次改动里更新所在目录 L2 的成员清单。
- 新建模块目录时同时建它的 L2，并在父级 L2 或 L1 登记。
- 碰到的文件缺 L3、碰到的目录缺 L2，顺手补上；没碰到的不做全仓库一次性补齐（见适配说明）。
- L2 的父级链接必须指向真实存在的文件。

## 大任务拆子 agent

- 工作量大、能按互不重叠的文件或主题切开的任务（审计、迁移、批量删除、多模块修复），拆给子 agent 并行做；小任务不拆，拆分本身有成本。
- 每个子 agent 必须交回：改了哪些文件、跑了什么命令、关键输出。
- 主会话核对证据后才接受：自己读 diff、重跑关键命令，不只信转述。
- 收尾附一张表：子任务 | 子 agent | 结论 | 证据。

---

# 本项目适配说明

- L2 是各模块目录的 CLAUDE.md，父级链接用仓库根相对路径
  - 已播种：panel、panel/internal 及其 api（含 admin、public）/domain/platform、panel/internal/platform/webapp、panel/internal/domain 下的 identity/notify/subscription/nodefabric/billing/appearance/support/adminops/plugin/content、panel/internal/platform/pg18test、panel/internal/platform/config、panel/internal/platform/sourcetest、panel/internal/platform/crypto、panel/internal/platform/realtime、panel/internal/platform/profiling、panel/internal/platform/server、panel/internal/domain 下的 dbbackup/giftcard、panel/internal/middleware、panel/tools/refactorcheck、panel/tools/loadtest、panel/tests、panel/internal/platform/httpx、panel/internal/api/node、panel/cmd 下的 aegis-admin/aegis-adminctl/aegis-node/aegis-public、panel/web、panel/deploy、panel/frontend 及其 dev（含 dev/mock、dev/mock/admin、dev/mock/portal）与 src 下的 admin（含 admin/screens 及已做页面的模块目录 dash/tickets/marketing/users/nodes/content/plans/system/billing/security）/portal（含 portal/screens 及已做页面的 common/overview/subs/plans/checkout/orders/wallet/referral/tickets/messages/help/account）/shell/core/ui/styles/showcase、panel/frontend/tests/smoke、pdnd、pdnd/kernel、pdnd/core、pdnd/cmd/pandora-h3-probe、pdnd/release/acceptancepanel
  - 其余目录在进入时补建。
- L3 在 Go 文件里写成 package 子句之前的 `//` 注释块，四行 [INPUT]/[OUTPUT]/[POS]/[PROTOCOL]
  - TS/TSX 用模板里的 `/** */`
  - Go 文件多已带中文设计注释，L3 加在其上方（中间空一行，不成为包文档），不改写原注释
  - 带 `//go:build` 的文件，L3 放在构建约束与空行之后。
- L3 渐进补齐：进入哪个目录、改哪个文件，就补那个目录和文件，不做全仓库一次性补齐。TS/TSX 已全部带 L3；Go 只有部分文件带，其余渐进补。
- 测试文件在 L2 成员清单中按 `*_test.go` 合并为一行。
- 单文件 ≤800 行：第 5 阶段重构之后，豁免之外没有超限的 .go 文件（含测试）
  - 豁免两类：① `pdnd/internal/reality/**`（fork 自 XTLS/REALITY，MPL-2.0，其本身基于 Go crypto/tls，见该目录 LICENSE；34 个文件）与 `pdnd/internal/realityquic/**`（fork 自 apernet/quic-go，见该目录 README；193 个文件）是 fork 来的第三方代码，保持上游的文件划分以便合上游，整目录豁免，目前其中 14 个文件超限（reality 8、realityquic 6）
  - ② 两个只剩一个超长测试函数、纯挪动拆不开的 PG18 测试 `panel/internal/middleware/idempotency_pg18_test.go`、`panel/internal/domain/billing/order_release_pg18_test.go`，逐个登记，拆到 800 行以内即须移出
  - 守卫：panel 的 `tools/refactorcheck/linelimit_test.go` 与 pdnd 的 `linelimit_test.go` 随 `go test ./...` 扫描全部 .go，豁免外超限即红，豁免过期也红
  - 守卫变红先按主题拆分（只挪代码，用 `panel/tools/refactorcheck` 自证），不要加豁免
  - 改豁免表要用户授权。
- 先找系统里已有的做法并沿用，不另起炉灶。本仓库的既定范式：
  - 日志用 platform/logging（log/slog）
  - 响应与错误用 platform/httpx
  - 配置只经 platform/config（panel 里只有它读进程环境变量；守卫 `panel/internal/platform/config/envaccess_test.go` 扫 panel/internal 与 panel/cmd 的非测试 Go 文件，豁免逐文件登记在同文件的 `envAccessExemptions`，豁免失效也红）
  - 前端 HTTP 只经 src/core/api.ts。
- 仓库公开，只放产品：不写入任何具体部署的值（服务器 IP、域名、后台路径前缀、密钥、从生产导出的数据）
  - 测试夹具只用虚构数据，模板只含占位符
  - gitleaks 命中必须停下处理，扫描不得与提交、推送串在同一条命令里。

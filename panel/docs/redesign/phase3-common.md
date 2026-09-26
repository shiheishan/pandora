# 面板重构 · 第 3 阶段公共规则

> 协调会话写于 2026-09-23。第 3 阶段每个会话开工前先读本文件，再读自己的开工说明（phase3-*.md）。
> 你看不到协调会话的项目记忆，决策以本文件、`phase2-brief.md` 第 3 节和 `api-contract.md` 为准；与 docs/CONSTRAINTS.md 冲突时以铁律为准。

## 1. 必读

1. `panel/docs/redesign/phase2-brief.md` 第 1、3、4 节：背景、已定决策（含 6 条保留后端规则）、部署约束。
2. `panel/docs/redesign/api-contract.md`：
   - 第 1 节横切约定、第 2 节非前端接口；
   - **第 5.A 节已决**（8 条阻塞项的最终结论，优先于 5.1–5.6 与各条目里的方案）；
   - 第 6 节迁移预估、第 7 节既有缺陷；
   - 与你负责模块对应的第 3 / 4 节条目。
3. 根 `CLAUDE.md`（GEB 文档协议）、`docs/CONSTRAINTS.md`、你要进入的目录的 L2 `CLAUDE.md`。

## 2. 会话与分工

| 会话 | worktree / 分支 | 开工说明 | 迁移号段 |
|---|---|---|---|
| 后端一 · 财务与商品 | `../pandora-be1` / `feat/panel-redesign-be1` | phase3-backend-1.md | 00068–00073 |
| 后端二 · 平台、运营与安全 | `../pandora-be2` / `feat/panel-redesign-be2` | phase3-backend-2.md | 00074–00085 |
| 门户前端 | `../pandora-fe-portal` / `feat/panel-redesign-fe-portal` | phase3-frontend-portal.md | 不写迁移 |
| 后台前端一（01–05） | `../pandora-fe-admin1` / `feat/panel-redesign-fe-admin1` | phase3-frontend-admin1.md | 不写迁移 |
| 后台前端二（06–09） | `../pandora-fe-admin2` / `feat/panel-redesign-fe-admin2` | phase3-frontend-admin2.md | 不写迁移 |

第 2 阶段（`../pandora-p2`）已于 2026-09-24 结束（外框 e687e4d）。前端会话的公共规则另见第 10 节；第 1–9 节里只针对后端的条款（迁移、PG18、router.go）前端会话不涉及。第 2 阶段第 ⑤ 步会改 `platform/httpx` 的错误码和 `middleware.RequireRecentReauth`（新增 `reauth_required`），后端会话**不要改这两处**，需要时写进报告。

## 3. 契约

- 契约是前后端并行的唯一依据。实现与契约不一致时，**先报告协调会话改契约**，不要自己偏离；契约明显写错（与代码事实不符）同样报告。
- 契约第 5 节的 21 条非阻塞待决已于 2026-09-25 全部定案（5.A.2，修订 R98），第 5 节不再有「未决前」。之后再遇到契约没写、设计与后端冲突的事，报告协调会话，不要替用户拍板。

## 4. 迁移

- 只用分给你的号段，从号段起点连续往上编；号段用完先报告。合并时协调会话会把各号段压紧成连续编号，文件内容不要依赖自己的编号。
- 每个迁移都要有可执行的 Down；新表开租户 RLS（仿 `app.enable_tenant_rls`）；新表要过表登记簿与权限字典两条契约测试（见 `panel/internal/platform/db` 的契约测试与 `panel/migrations/RESERVED-TABLES.md`）。
- 本机没有 PostgreSQL，也没有 Docker：迁移与 SQL 只能在 CI 上执行。每个带 SQL 的改动都要有 PG18 集成测试，并登记进 `panel/deploy/run-pg18-gates.sh`（后端二第 ⓪ 步把它接进 CI 之前，登记好、等 CI 接上再验）。

## 5. 推送与 CI

- 用户已长期授权：**只允许把自己的分支推到 origin，用来跑 CI**（`git push origin <你的分支>`）。不推 `main`，不开 PR，不 force push，不推别人的分支。
- 每次推送后用 `gh run list --branch <你的分支>` / `gh run view` 查结果，写进报告；CI 红了先修再往下做。
- **panel 全量单元测试在 `panel-pg18.yml` 的 panel-unit 任务里跑**（2026-09-24 起；此前没有任何 CI 跑，后端一 `077eb27` 因此漏过表登记簿测试）。报告里写 Panel PostgreSQL 18 gates 结果时，两个任务（panel-unit、panel-pg18）都要绿；推送前本机全量仍要对最后一个提交跑，CI 是兜底不是替代。

## 6. 共享文件

- `api/admin/router.go`、`api/public/router.go`：两个后端会话都会加路由，只在自己负责模块的那一段里加，不重排、不改别人的段落；合并冲突由协调会话处理。
- 同一个文件两边都要改时（例如 `nodefabric/uniproxy.go`），在报告里写清改了哪几个函数。
- 前端目录 `panel/frontend` 后端会话不碰。
- 仓库已有的 3 处 gofmt 问题（billing/checkout.go、nodefabric/enrollment.go、nodefabric/node_admin.go）：你改到这个文件时可以顺手 gofmt，其余不动。

## 7. 质量与文档

- 每个缺陷修复和新行为都要有测试；能用单元测试证明的用单元测试，涉及 SQL 的加 PG18 测试。
- 新文件 ≤ 800 行；GEB：新目录建 L2，改到的 Go 文件补 L3 头，文件增删同步 L2。
- 本机验证：`go build ./...`、`go vet ./...`、`go test -p 1 -count=1 -timeout 10m ./...`（PG18 测试会跳过，属预期，但要在报告里写明哪些没在本机跑）。本机 Go 是 1.27.1，`run-pg18-gates.sh` 会拒绝与 go.mod 主次版本不一致的 Go，所以它只在 CI 跑。

## 8. 节奏与报告

- 每做完开工说明里的一步就提交并停下报告，等协调会话验收后再继续。
- 提交信息英文祈使句，结尾带协作者行。
- 报告内容：提交号；跑了哪些检查及结果（失败原样贴输出）；没跑什么；CI 运行号与结果；契约需要改的地方；给协调会话的待办（router 冲突风险、全局文档、新发现的问题）。

## 9. 上下文接力

会话上下文快满时，由协调会话在某一步验收后安排接力：旧会话结束，同一个 worktree 开新会话。新会话读本文件和自己的开工说明——开工说明末尾的「进度与补充事项」一节记着已完成的步骤、下一步和途中定下的补充事项，是接力的全部依据。不在一步做到一半时接力。

## 10. 前端会话（门户 / 后台一 / 后台二）

### 10.1 先读

1. `phase2-brief.md` 第 2、3、4 节：设计稿位置与索引、已定决策（6 条保留后端规则）、部署约束（CSP、相对路径、hash 路由、Bearer、SSE 只能 fetch 流）。
2. `panel/frontend/CLAUDE.md` 及其下各目录的 L2：`src/core`（api / query / router / format）、`src/ui`（组件库）、`src/shell`（运行时）、`src/admin` 或 `src/portal`（外框）、`dev`（假后端）。**不要自造底层**：HTTP 只经 `core/api.ts`，查询只经 react-query 并用 `meta.topics` 接实时失效，金额与时间只用 `core/format.ts`，组件只从 `ui/index.ts` 取。
3. `api-contract.md`：第 1 节横切约定、5.A 已决、**第 9 节修订记录（条目里「修订 Rn」行优先于原文）**，以及你负责模块的第 3 / 4 节条目和第 5 节对应待决。
4. 设计稿：`~/Desktop/Pandora前端代码-20260923/设计稿/` 下你负责模块的 `*.dc.html`，外加 `设计规范.dc.html`。视觉与交互按设计稿；数据与规则按契约；两者冲突且不在 6 条保留规则里的，报告协调会话，不要自己拍板。

### 10.2 写在哪里

- 页面：后台写在 `src/admin/screens/<模块>/`，门户写在 `src/portal/screens/<页面>/`。登记表 `screens/index.ts` 已为每个模块 / 页面预留好懒加载入口（后台前端二第 ⓪ 步建），**你只改自己负责的那几个目录**，不改登记表、不改别人的目录。
- 假后端：每个模块一个文件 `dev/mock/<admin|portal>/<模块>.ts`（同样由第 ⓪ 步预建空壳），你只往自己的文件里加接口。形状严格照契约（含修订），错误码、reauth、幂等键的行为也照契约模拟，这样页面在假后端上走通就等于按契约走通。
- 共享层（`src/core`、`src/ui`、`src/shell`、`src/styles`、两个外框文件、`dev/mock-api.ts` 本体）：
  - **只许新增**：新组件放新文件，并在 `ui/index.ts` 加一行导出；新增在报告里单列。
  - **改已有文件的行为或签名**：先报告协调会话，由协调会话指定哪个会话改，别的会话等合并后再用。
  - 只有一个页面用的东西先放在自己的目录里；发现另一个会话也需要时报告协调会话，由协调会话决定提升到 `ui/` 并指定一个会话来做，避免两边各写一份。图表（趋势、用量、排行）同理：设计规范里没有图表库，一律手写 SVG，不引入图表依赖。
- 不加运行时依赖；确有必要先报告理由，等协调会话同意。
- 不碰 Go 代码、迁移、`router.go`；契约写错或后端缺接口，写进报告。

### 10.3 后端没做完的接口

契约里标「待补·后端」的接口由两个后端会话并行实现，前端**照契约形状照写**，在自己的假后端文件里模拟。每一步的报告里列出本步用到的「待补·后端」接口及其当时在主线上是否已实现（查 `api-contract.md` 第 9 节与后端开工说明的进度）。后端实现与契约不一致会以新的修订 Rn 出现，协调会话会通知你改。

本机没有 PostgreSQL，前端不会在本机对接真网关，zod schema 是和后端对账的唯一防线：**schema 按契约写全、写严**，可选字段就写可选，不要为了让假后端通过而放宽。

### 10.4 状态与交互的共同要求

- 每个列表 / 卡片都要有加载（`Skeleton`）、空（`Empty`，一句现状 + 一句能做什么）、错误三种状态；缺权限的接口回 404，按「无权限或不存在」处理，不显示成报错。
- 后台写操作：带 reauth 的接口由常驻对话框接管，页面不自己弹框；用户取消时请求以 `reauth_required` 失败，页面静默忽略（R34）。要幂等键的接口，一次用户意图生成一个 key，重试与重放复用它（`api.ts` 已支持，页面按契约传 `idempotencyKey`）。
- 幂等键（契约 1.5 与 R85）：后端只重放 2xx，非 2xx 同 key 会重新执行或回 409。所以动作成功后必须丢弃 key，收到 4xx 业务拒绝后也丢弃，断网与 5xx 保留 key 重试；下单类动作成功后要防重复下单（门户 `common/intent.ts` 的 `usePlacedOrder`）。
- 破坏性操作用 `ConfirmModal`，先说后果、按钮用动词。
- 第 5 节待决已全部定案（5.A.2）：「维持」的照第 3 阶段现有做法，要改的以对应修订（R99–R104）为准。
- 金额以最小单位传输，显示用 `formatMoney`；时间用 `relativeTime` 或设计稿给的格式；邮箱、订阅地址等隐私字段按契约与保留规则 2、3 处理。

### 10.5 验收（每步）

- `cd panel && make frontend-check`（lint、typecheck、vitest、双入口构建）通过。
- `make frontend-embed && go test ./web/... ./internal/platform/webapp/...` 通过，然后 `git checkout -- web && git clean -fdX web` 恢复占位页，不提交产物。
- 纯逻辑（数据映射、表单校验、筛选排序、金额计算、状态文案）写 vitest 单元测试；界面在浏览器里对假后端实测：门户看 1280、960、375 三个宽度；**后台只支持 960 及以上（2026-09-24 用户定），看 1280 与 960 两个宽度，窄于 960 允许横向滚动、不做窄屏适配**；明暗两种主题，加载 / 空 / 错误 / 无权限四种状态，每个写操作走一遍（含 reauth 与幂等重放）。报告里写清实测了什么、没测什么。
- 浏览器检查工具（find / read_page）读表单控件名字时只认 `label[for]` 的直接文字，包裹式 label、`aria-labelledby` 会被读成 value（如复选框的「on」）。这是工具的局限，不是组件缺陷；ui 的 `Checkbox` 已改为 `htmlFor` 关联。自己写的表单控件按同样方式关联标签，按名字查找才可靠。
- 开发服务器端口按开工说明，`--strictPort`，避免三个会话互相抢端口；做完一步关掉。
- 新文件 ≤ 800 行；GEB：新目录建 L2，每个 TS/TSX 文件带 L3 头，文件增删同步 L2。

### 10.6 推送与报告

与第 5、8 节相同：每步一个提交、推送自己的分支跑 CI（Pandora NativeCore 里的 panel-frontend 任务必须绿）、停下报告等验收。只改前端和文档的推送不会触发 Panel PostgreSQL 18 gates（它只看 Go 路径），这时报告只写 NativeCore 的运行号即可。做事前先 `git merge feat/panel-redesign` 同步主线。

## 11. 第 4 阶段（收尾，2026-09-25 起）

第 3 阶段开发已全部合入（主线 b145067）。第 4 阶段落实用户 2026-09-25 的定案（契约 5.A.2、修订 R98–R104）、修后端遗留、建真实联调冒烟。第 1–10 节照旧有效，本节只写不同的地方。

| 会话 | worktree / 分支 | 开工说明 | 迁移号段 |
|---|---|---|---|
| 后端三 · 数据修复与商品 | `../pandora-be3` / `feat/panel-redesign-be3` | phase4-backend-3.md | 00087–00092 |
| 后端四 · 交付语义 | `../pandora-be4` / `feat/panel-redesign-be4` | phase4-backend-4.md | 00093–00098 |
| 联调冒烟 | `../pandora-smoke` / `feat/panel-redesign-smoke` | phase4-smoke.md | 不写迁移 |
| 前端收尾 | `../pandora-fe-final` / `feat/panel-redesign-fe-final`（dev 端口 5241） | phase4-frontend.md | 不写迁移 |

- 契约修订从 **R98** 起，编号仍由协调会话统一分配；R99–R104 是这次定案的形状，前后端按它并行，前端照契约写、在假后端里模拟（第 10.3 节）。
- 两个后端会话的分界：**`nodefabric/uniproxy.go`、`subscription/service.go`、`api/admin/pools.go`、`usergroup.go`、`devices.go` 归后端四**；套餐目录（`adminops/catalog.go`、`plan_wizard*.go`）、门户套餐目录、计费、通知、工单、身份归后端三。`api/admin/handlers.go` 两边都会改：后端三只动重置密码与工单详情，后端四只动节点列表的在线统计，报告里写清改了哪几个函数。`nodefabric/node_admin.go` 的节点 PATCH（R78）归后端三。
- 冒烟会话发现的形状不一致，一律报告协调会话，由协调会话写成修订并指派给后端或前端会话修；冒烟会话自己只修冒烟脚本。
- 两个超 800 行的文件（admin `handlers.go`、`nodefabric/node_admin.go`）仍不重构，除非用户另行授权。
- **收尾状态（2026-09-26）**：四个会话全部完成并合入主线（后端三 ①–⑤、后端四 ①–⑦、前端收尾 ①–⑤、联调冒烟 ①–⑥），契约修订到 R116。CI 上 panel-smoke 用页面 zod schema 解析真网关 120 行、写路径抽样、五个 e2e 脚本失败即红。下一步是测试机人工点（部署要用户授权），部署注意：没划进节点池的节点不服务任何人（R104），先划进池；新发布包不再带 aegis-agent，旧机器上的要人工清理；节点改状态、上线、退役的报错理论上可能露出英文 CHECK 约束原句，人工点时留意。

## 12. 第 5 阶段（遗留修复与重构，2026-09-26 起）

第 4 阶段已收尾（第 11 节末）。用户 2026-09-26 定：修协调会话查出的遗留小问题；**授权重构超 800 行的文件，代码与文档一起改**；**暂不做任何真机（测试机）操作**，nodeagent 定性与测试机登记都往后放。第 1–11 节照旧有效。

| 会话 | worktree / 分支 | 开工说明 | 迁移号段 |
|---|---|---|---|
| 后端四 · 遗留修复（新会话接力） | `../pandora-be4` / `feat/panel-redesign-be4` | phase4-backend-4.md 的第 ⑧–⑫ 步 | 00095–00098 |
| 重构 | `../pandora-refactor` / `feat/panel-redesign-refactor` | phase5-refactor.md | 不写迁移 |

- 契约修订从 **R117** 起，仍由协调会话分配。
- **两个会话的文件分界**：后端四这一轮要改的文件，重构会话在后端四对应步骤合入主线之前**不碰**：`domain/billing/**`、`domain/notify/**`、`domain/identity/service.go`、`api/admin/mail.go`、`api/admin/handlers.go`、`domain/nodefabric/node_activate.go`、`node_retire.go`、`platform/crypto/**`、`cmd/aegis-admin/main.go`、`cmd/aegis-public/main.go`、`middleware/middleware.go`、`pdnd/core/external/**`。重构的排序已按这个分界安排（见 phase5-refactor.md）。
- 真机相关（测试机上的 aegis-nodeagent 定性与迁移、删 `nodeagent/`、把测试机登记进 `~/ai/servers`）**本阶段不做**。
- **收尾状态（2026-09-26）**：后端四 ⑧–⑭（R117，迁移用到 00095）与重构 ①–⑥ 全部合入主线（合并 d3d9f74）。panel 与 pdnd 除两个 fork 目录和两个单函数超长的 PG18 测试外，已无超 800 行的 `.go` 文件；两道行数守卫随 `go test ./...` 运行，豁免规则写在根 `CLAUDE.md`。剩下的都待用户拍板：两个 PG18 测试是否另派改函数体的重构、真机（测试机）人工点与 nodeagent 定性、审计 IP 哈希换 key、`feat/panel-redesign` 何时进 `main`、收尾后的 worktree 与远端分支清理。


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
| 门户前端 | 第 2 阶段完成后开 | 待写 | 不写迁移 |
| 后台前端一（01–05） | 同上 | 待写 | 不写迁移 |
| 后台前端二（06–09） | 同上 | 待写 | 不写迁移 |

第 2 阶段（`../pandora-p2`）的 ③–⑥ 与两个后端会话并行。第 2 阶段第 ⑤ 步会改 `platform/httpx` 的错误码和 `middleware.RequireRecentReauth`（新增 `reauth_required`），后端会话**不要改这两处**，需要时写进报告。

## 3. 契约

- 契约是前后端并行的唯一依据。实现与契约不一致时，**先报告协调会话改契约**，不要自己偏离；契约明显写错（与代码事实不符）同样报告。
- 契约第 5 节里没有定案的 22 条非阻塞待决，一律按条目里的「未决前」处理，不要替用户拍板。

## 4. 迁移

- 只用分给你的号段，从号段起点连续往上编；号段用完先报告。合并时协调会话会把各号段压紧成连续编号，文件内容不要依赖自己的编号。
- 每个迁移都要有可执行的 Down；新表开租户 RLS（仿 `app.enable_tenant_rls`）；新表要过表登记簿与权限字典两条契约测试（见 `panel/internal/platform/db` 的契约测试与 `panel/migrations/RESERVED-TABLES.md`）。
- 本机没有 PostgreSQL，也没有 Docker：迁移与 SQL 只能在 CI 上执行。每个带 SQL 的改动都要有 PG18 集成测试，并登记进 `panel/deploy/run-pg18-gates.sh`（后端二第 ⓪ 步把它接进 CI 之前，登记好、等 CI 接上再验）。

## 5. 推送与 CI

- 用户已长期授权：**只允许把自己的分支推到 origin，用来跑 CI**（`git push origin <你的分支>`）。不推 `main`，不开 PR，不 force push，不推别人的分支。
- 每次推送后用 `gh run list --branch <你的分支>` / `gh run view` 查结果，写进报告；CI 红了先修再往下做。

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

---
name: new-migration
description: pandora 新写或修改一个 goose 迁移（panel/db/migrations）时按项目规范一次写对并自证：取号、选样板（可逆的结构变更、改已有函数、只修数据、大表并发建索引）、检查表（每个都有 Down、表属主唯一、约束进库、追加写守卫、授权与 SECURITY DEFINER 登记、锁超时与预检、种子与代码对照）、本地与 CI 往返自证，以及往返红了怎么修。要「加表」「加列」「改约束」「写迁移」「补 Down」「往返红了」「迁移号用哪个」时使用。
---

# 写一个迁移

迁移是 goose 版本化 SQL：`panel/db/migrations/NNNNN_<名>.sql`，经 `migrations.go` 的 `//go:embed *.sql` 打进二进制，由 `pandora migrate up | down | status` 执行（`internal/app/migrate.go`，连 `PANDORA_DATABASE_MIGRATION_URL`，即库属主 `pandora_owner`，不是超级用户）。不用 GORM AutoMigrate。

- 现有迁移大多是各模块的基线（一个模块一个文件，在空库上建），夹着几个在基线上改的增量迁移（如 00016、00024、00033）。新写的都是增量迁移，按增量的规矩写：锁超时、预检、失败回滚。
- 按路径的迁移规则在 `.claude/rules/` 里，改 `panel/db/migrations/**` 时自动加载；运维侧的升级与回退见 `panel/deploy/RUNBOOK.md` 的迁移一节。

脚本都只读，在仓库根目录跑：

| 脚本 | 做什么 |
|---|---|
| `bash .agents/skills/new-migration/scripts/next-number.sh` | 主线最近 5 个迁移、当前工作区最大号、各任务分支上主线还没有的迁移、空号，以及下一个可用号 |
| `python3 .agents/skills/new-migration/scripts/find-def.py <对象名> [--body] [--before <号>]` | 哪些迁移的 Up 段对这个函数、表、约束做过 DDL；`--body` 打印最后一次 CREATE 的原文，写 Down 时逐字还原用 |

## 1. 取号

- 跑 `next-number.sh`，用它给的号。主线最大号以下的空号不回填。
- 文件名 `NNNNN_<模块>_<主题>.sql`（如 `00016_commission_review.sql`、`00024_node_alive_by_subscription.sql`）。一个迁移只动一个模块的表。
- 几路并行：号段由主 Agent 派工时分好写进任务说明，只用自己那段；两路撞号时 goose 报重复版本，后合的那路改号。
- 进了主线的迁移号不改、Up 不改（开发库、测试机的 `goose_db_version` 已经记着）；要改结构就再写一个迁移。

## 2. 套样板

样板在 `templates/`，复制成新文件后替换所有 `<…>`：

| 场景 | 样板 | 参考的真迁移 |
|---|---|---|
| 建表、加列、换约束、小表建索引等能逆的结构变更 | `reversible.sql` | `00016_commission_review.sql`（加列、换 CHECK 与部分唯一索引，Down 带数据守卫）、`00030_order_paid_notify.sql`（建表、建索引、补种子） |
| CREATE OR REPLACE 已有的函数（保留期清理 `purge_*`、`deny_*`、`*_guard` 触发器函数） | `replace-function.sql` | `00023_node_retention.sql`（SECURITY DEFINER 清理函数的写法） |
| 只修数据、补数据，Down 无法还原 | `irreversible.sql` | （还没有） |
| 已经很大的表上建索引 | `concurrent-index.sql` | 小表不用它，照 `00024_node_alive_by_subscription.sql` 在事务里普通建 |

- **文件头**：`-- +goose Up` 下面先写注释：做什么、谁定的、属哪个模块；锁与耗时；预检；失败后怎么回滚（根 CLAUDE.md「架构规矩」第 3 条）。
- **超时**：增量迁移的 Up 和 Down 第一批语句就是 `SET LOCAL lock_timeout` 与 `SET LOCAL statement_timeout`。NO TRANSACTION 的文件里 SET LOCAL 无效，用会话级 SET 并在段尾 RESET。
- **函数体、DO 块**里有分号，必须包 `-- +goose StatementBegin` / `-- +goose StatementEnd`。
- **Down**：每个迁移都有。结构变化写真的逆操作；退回后放不下的数据用 DO 块守卫（有行就 RAISE，没有就照常退）。只修数据、确实不可逆的，Down 里只写一段注释说明为什么（goose 把空 Down 当空操作），**不要无条件 RAISE**：CI 会在测试库上 down 到底再 up。

## 3. 检查表：项 → 守卫

「评审」表示没有自动守卫，写完自己对一遍。

| 项 | 守卫 |
|---|---|
| 有 Down，结构逐项退回上一版 | CI test job：`db/migrations` 的 `TestRoundTrip`（up → down 到底 → up）与二进制 `migrate up → status → down → up` |
| 表属主唯一：新表归一个模块（对照 `docs/architecture.md` 4.1 并补进去），只有那个模块的 repo 读写；别的模块调属主的 service | depguard（lint job）挡跨模块 import；表归属靠评审 |
| 约束进库：NOT NULL、CHECK、UNIQUE、外键能加就加；状态枚举写 CHECK | 评审；PG 用例 |
| 钱用 `numeric(20,2)`，余额变动走只追加的账本（ledger 模块） | 评审 |
| 只追加的表挂 `deny_mutation()`，不许删的表挂 `deny_delete()`（`00001_base.sql`） | `configure-app-role.sql` 第 7 段按这两个触发器自动收回运行角色的 UPDATE / DELETE；`TestDenyMutation` 钉守卫函数本身 |
| 迁移里不写 GRANT：安装与升级在 `migrate up` 之后跑 `configure-app-role.sql`，统一给 `pandora_app` 授权再按守卫收回 | panel-deploy 的 deploy-db-roles-pg18 job（`db-roles_pg18_test.sh`：属主迁移、收权限、以 pandora_app 跑 serve） |
| 新增、改签名、删除 SECURITY DEFINER 函数：同步改 `configure-app-role.sql` 第 9 段的登记（未登记的一律拒绝）；固定 `search_path` | 同上 job |
| 需要超级用户的 DDL（建角色、改库参数、`GRANT SET ON PARAMETER`）不进迁移，放 `panel/deploy/bootstrap.sh roles` | 同上 job；评审 |
| 种子与 Go 常量一致：通知模板对 notify 的 registry，降级开关对 settings | `TestSeedTemplatesMatchRegistry`（notify/service）、`TestSeedSwitchesMatchMigration`（settings/service），CI 的 PG 用例 |
| 数据层不变量（账本配平、订单恒等式等） | `panel/tests/invariants.sql`，CI e2e job；加了新的库内约束口径，同一提交补用例 |
| 会锁大表、失败代价高的 DDL：文件头写预检、超时与失败回滚；换约束先 `NOT VALID` 再另起语句 `VALIDATE`；建索引用 CONCURRENTLY（`concurrent-index.sql`）；回填分批、能重跑 | 评审 |
| 删列、改名分两步：先加新的、代码切过去，下一个版本再删旧的 | 评审 |
| 动态 SQL（`EXECUTE format(...)`） | 评审 |
| 注释里不写真实部署的值（IP、域名、密钥） | pre-commit 的 gitleaks |

## 4. 自证

### 本地

本机没有 PG（见根 CLAUDE.md「环境与工具坑」），迁移本身跑不了，只能静态核对：

```bash
f=panel/db/migrations/<文件>.sql
grep -cE '^-- \+goose (Up|Down)$' "$f"                                           # 应为 2
echo "$(grep -c 'StatementBegin' "$f") $(grep -c 'StatementEnd' "$f")"             # 两数相等
cd panel && GOTOOLCHAIN=go1.26.9 go build ./... && GOTOOLCHAIN=go1.26.9 go vet ./db/...
```

再对读写这些表的模块包跑 `go test`（非 PG 用例）。PG 用例与往返在本机跳过，跳过不等于通过。

有 Docker 的机器（开发机或测试机）上可以真跑一遍往返，在 `panel/` 下（先按 `dev/.env.example` 建好 `dev/.env`）：

```bash
make up && make migrate
set -a && . ./dev/.env && set +a
go run ./cmd/pandora migrate down && go run ./cmd/pandora migrate up && go run ./cmd/pandora migrate status
```

### 推送后（GitHub）

迁移改动触发 panel-newstack.yml，必须等 `wait-github.sh`（verify skill）。看这几处：

- test job 的 Tests 步骤里 `db/migrations` 的 `TestRoundTrip`，以及读写这些表的 PG 用例；
- test job 的「Migration round trip through the binary」步骤；
- e2e job 的 `invariants.sql`；
- 动了授权、守卫触发器或 SECURITY DEFINER 函数的，panel-deploy 的 deploy-db-roles-pg18 job。

### 回填耗时

有回填、修数据或大表 DDL 的，上线前在测试机的库副本上只套新迁移的 Up 量耗时，按两倍估。数字写进文件头注释和任务报告。

## 5. 往返红了

1. **先看红在哪一步**：up、down，还是第二次 up。
2. **down 失败**：Down 漏了对象、删的顺序反了（先删依赖者），或数据守卫被触发（见「坑」第一条）。改函数体前用 `find-def.py <函数> --before <本迁移号> --body` 拿上一版原文逐字抄。
3. **第二次 up 失败**：Down 没删干净（`IF EXISTS` 掩盖了漏删）、种子重复插入、约束名和上一版不一致。
4. 已进主线的迁移只修 Down 和注释，Up 不改。

## 坑

- **二进制往返在全部测试之后跑**：test job 的「Migration round trip through the binary」与 Tests 共用一个库，库里有各包测试留下的数据，`migrate down` 只退最后一个迁移。最后一个迁移的 Down 若带数据守卫，会被测试数据触发；守卫只拦真正放不下的行，别写成「表里有行就拒绝」。
- goose 默认每个文件一个事务。`-- +goose NO TRANSACTION` 的文件每条语句单独提交，中途失败会留下半成品（如 INVALID 索引），Up 要能重入。
- `pandora migrate` 读 `PANDORA_DATABASE_MIGRATION_URL`；本机 `dev/.env` 里它和运行用的 URL 都是 `pandora_owner`，生产上运行角色是收窄过的 `pandora_app`，权限问题只在 deploy-db-roles-pg18 job 里看得到。
- 保留期清理函数靠定义上的 `SET session_replication_role = replica` 绕开追加写守卫，这要属主角色有 `SET ON PARAMETER` 权限（`bootstrap.sh roles` 授予）；新写这类函数照 `00023_node_retention.sql`。

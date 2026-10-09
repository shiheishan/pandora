---
name: accept-task
description: pandora 总协调验收任务分支并合进主线：读 report.md、查越界改动、独立重跑关键命令、核 CI、性能类用评测集判分、合并与冲突处理、推送后等 CI、向用户出结论表。用户说「X 做完了」「验收」「合并」「核对子 agent 的结果」，或后台子 agent 交回报告时使用。
---

# 验收任务分支

目标：只合进「证据自己核过」的改动。子 agent 的报告是线索，不是结论；数字、行为、CI 都要自己看一遍。派任务见 dispatch-task skill，验证命令与 CI job 见 verify skill。

## 先判断是不是已经验过

用户会把报告贴错会话或重复贴。先 `git log --oneline feat/panel-redesign..<分支>` 和主目录 `.claude/TASKS.md` 对一下。

后台子 agent 交回时，先存报告：`scripts/save-report.sh <通知里的 output-file> ../pandora-<名字>/.claude/report.md`（取转录里最后一条带文字的消息；agent 中途「还在等 CI」的临时通知不是终稿，等 `end_turn` 的那次再存）。

## 要核的东西

1. **范围**：`git diff --stat <基点>..<分支>`；`scripts/check-ownership.sh <基点> <分支> <归属清单文件>` 列出归属外的改动。越界的要么有报告里的理由、要么退回。
2. **关键 diff 自己读**：迁移（Up/Down、编号、RLS、授权、追加写、触发器）、权限与认证、锁与事务边界、缓存的失效路径、对外 JSON 字段。
3. **独立重跑**：按 verify skill 的本地层，对改到的包重跑 vet/test；报告里引用的关键命令挑几条重跑。
4. **按改动类型补自证**：
   - 纯挪动或拆文件：`cd panel && go run ./tools/refactorcheck compare -base <sha>^ -head <sha> -tests`（pdnd 加 `-C ../pdnd`），必须 PURE MOVE；
   - 只改注释或文字：`scripts/comment-only.sh <base> <head>` 必须为空；
   - 声称没改 SQL：`go run ./tools/refactorcheck sqlset -base <base> -head <head>` 必须 UNCHANGED；
   - 改了 SQL 的性能项：用评测集判分（训练集与留出集都不变差、改前改后结果一致；只训练集变好算过拟合，退回）。
5. **对抗式审查**：先跑 `python3 .claude/skills/adversarial-review/scripts/triggers.py feat/panel-redesign <分支>`。退出 0（命中钱、权限、秘密、迁移、节点内核、新依赖、并发其中之一）的分支，按 adversarial-review skill 派 opus 只读审查，可以和等 CI 并行。中危以上的发现要么修完再合，要么满足该 skill「什么时候可以合」里先合后修的条件。
6. **CI**：先 `scripts/ci-status.sh <分支>` 看一眼检查机与各 workflow 的现状（只读不等）；还没出结论就按 verify skill「远端层」等；红了用 ci-triage skill 的 `triage.sh` 定位。PG18 必须 0 SKIP；grep 新增测试名，确认真跑了。

## 合并

- 在主目录主线上 `git merge --no-ff <分支>`。冲突多在共享热点（`cmd/*/main.go`、router、config、`.claude/rules`），保留双方；语义冲突（一方删了另一方新用到的东西）合完立刻编译测试。
- 合进来的分支带新迁移：合完在主线跑 `python3 .claude/skills/new-migration/scripts/upsegment-sha.py <新迁移.sql> >> panel/tools/migrationlint/upsegments.txt` 冻结 Up 段，再跑 `--check`；冻结由合并的人做，不指望子 agent。
- 一波几路合完再推主线（长期授权：`feat/panel-redesign` 可推），推完等检查机与 GitHub 全绿才算完成。
- **推 main 每次都要先问用户**；删 worktree、删分支见 cleanup skill。

## 交给用户

按根 CLAUDE.md 收尾附表：子任务 | 子 agent | 结论 | 证据（命令、sha、CI 号、判分结果）。同时在主目录 `.claude/TASKS.md` 打勾写结论。

## 坑

- 自动模式会拦 `git reset --hard`、force push、删 worktree、改别的会话分支的 ref，也会顺带拦紧跟着的查询。要改写分支，用 `git commit-tree` 复用原树拼好，再把 `update-ref` 和 `--force-with-lease` 命令交给用户。
- 分支自己的 CI 没出结论不合（推送故障时等用户解锁，见 ci-triage 的坑与根 CLAUDE.md「环境与工具坑」）。
- PG18 夹具撞号是最常见的 CI 红：报告里出现「PG18 首推失败、改夹具 id 后绿」属正常，合并前 grep 一下新夹具的 id 前缀在主线上没有别人在用（细则见 `.claude/rules/platform-pg18.md`）。
- 子 agent 常把「为新 PG18 域在 run-pg18-gates.sh 加一行」「为新方法改同包的一个小文件」列为越界：只要是登记性的一两行就接受，记进 TASKS 结论。
- `check-ownership.sh` 每路都跑，不只信报告里自报的越界。
- 几路同改一个 `CREATE OR REPLACE` 的函数：见根 CLAUDE.md「环境与工具坑」。

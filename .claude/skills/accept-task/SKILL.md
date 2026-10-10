---
name: accept-task
description: pandora 总协调验收任务分支并合进主线（或集成分支）：读 report.md、查越界改动、独立重跑关键命令、核 CI、性能类走 perf-gate 判分、合并与冲突处理、一波几路的合并表（顺序、合时改、合后冻结与要改的 skill、要向用户说明的新机制）、集成分支（S 的 feat/panel-redesign-s）的逐路合入与最后一次并主线、推送后等 CI、向用户出结论表。用户说「X 做完了」「验收」「合并」「这一波怎么合」「核对子 agent 的结果」，或后台子 agent、Cursor 的 Composer 交回报告时使用。
---

# 验收任务分支

目标：只合进「证据自己核过」的改动。子 agent 的报告是线索，不是结论；数字、行为、CI 都要自己看一遍。派任务见 dispatch-task skill，验证命令与 CI job 见 verify skill。

下文 `<上游>` 指该路 brief 写的上游分支：一般是主线 `feat/panel-redesign`；叠在集成分支上的路是集成分支（如 `feat/panel-redesign-s`，见 dispatch-task「叠在集成分支上的路」）。

## 先判断是不是已经验过

用户会把报告贴错会话或重复贴。先 `git log --oneline <上游>..<分支>` 和主目录 `.claude/TASKS.md` 对一下。

交回时先落报告，按执行者分：

- Claude 子 agent：`scripts/save-report.sh <通知里的 output-file> ../pandora-<名字>/.claude/report.md`（取转录里最后一条带文字的消息；agent 中途「还在等 CI」的临时通知不是终稿，等 `end_turn` 的那次再存）。
- Cursor 的 Composer：`scripts/save-report.sh <cursor-launch 打印的日志> ../pandora-<名字>/.claude/report.md`。Composer 自己写报告，脚本不覆盖，只核日志末行 `exit=0`、报告非空且没被提交。它没写报告时，脚本把日志全文存过去并退出 1：那只是最终回复，按 brief「报告」逐项补问。**Composer 整路交回时**，总协调（或派一个 opus）先按 verify skill 本地跑、推送、等 CI，再按下面「要核的东西」验收；报告里的命令与 CI 结论对 Composer 不适用，以总协调自己跑的为准。

## 要核的东西

1. **范围**：`git diff --stat <上游>...<分支>`（三个点：从合并基点算，去掉上游后来的改动）；`scripts/check-ownership.sh <上游> <分支> <归属清单文件>` 列出归属外的改动。越界的要么有报告里的理由、要么退回。
2. **关键 diff 自己读**：迁移（Up/Down、编号、RLS、授权、追加写、触发器）、权限与认证、锁与事务边界、缓存的失效路径、对外 JSON 字段。
3. **独立重跑**：按 verify skill 的本地层，对改到的包重跑 vet/test；报告里引用的关键命令挑几条重跑（**Cursor 的 Composer 整路交回**：不依赖报告里的命令列表，总协调按 verify 自己跑）。
4. **按改动类型补自证**：
   - 纯挪动或拆文件：`cd panel && GOTOOLCHAIN=go<go.mod 版本> go run ./tools/refactorcheck compare -base <sha>^ -head <sha> -tests`（pdnd 加 `-C ../pdnd`），必须 PURE MOVE；
   - 只改注释或文字：`scripts/comment-only.sh <base> <head>` 必须为空；
   - 声称没改 SQL：`GOTOOLCHAIN=go<go.mod 版本> go run ./tools/refactorcheck sqlset -base <base> -head <head>` 必须 UNCHANGED；
   - 性能项（改了 SQL、热路径、缓存、连接池、节拍、部署参数，或 brief 里有性能目标）：一律走 perf-gate skill，按它选层（SQL 走 bench-eval 评测集：训练集与留出集都不变差、结果一致，只训练集变好算过拟合；Go 热路径与资源走 Vultr 同机 A/B）。合并依据是现场目录里的 `verdict.md`：「退回」「不能判」「复测」不合；「可合，未达新标准 N 项」把未达项记进 TASKS，brief 承诺过的项没达到按退回；后缀「非正式数据」的不作依据。
5. **对抗式审查**：先跑 `python3 .claude/skills/adversarial-review/scripts/triggers.py <上游> <分支>`，执行者是 Composer 的加 `--composer`。退出 0 的分支，按 adversarial-review skill 派 opus 只读审查，可以和等 CI 并行。退出 0 有两种：命中钱、权限、秘密、迁移、部署脚本、节点内核、新依赖、并发其中之一；或者是 Composer 交回、不是小件。小件指领域无命中，且非测试文件增删合计 ≤ 30 行，口径见该 skill 第 1 节。中危以上的发现要么修完再合，要么满足该 skill「什么时候可以合」里先合后修的条件。
6. **CI**：先 `scripts/ci-status.sh <分支>` 看一眼检查机与各 workflow 的现状（只读不等）；还没出结论就按 verify skill「远端层」等；红了用 ci-triage skill 的 `triage.sh` 定位。PG18 必须 0 SKIP；grep 新增测试名，确认真跑了。

## 合并

- 在主目录主线上 `git merge --no-ff <分支>`。冲突多在共享热点（`cmd/*/main.go`、router、config、`.claude/rules`），保留双方；语义冲突（一方删了另一方新用到的东西）合完立刻编译测试。
- 合进来的分支带新迁移：合完在主线跑 `python3 .claude/skills/new-migration/scripts/upsegment-sha.py <新迁移.sql> >> panel/tools/migrationlint/upsegments.txt` 冻结 Up 段，再跑 `--check`；冻结由合并的人做，不指望子 agent。
- 一波几路合完再推主线（长期授权：`feat/panel-redesign` 可推），推完等检查机与 GitHub 全绿才算完成。
- **推 main 每次都要先问用户**；删 worktree、删分支见 cleanup skill。
- 上游是集成分支的路，不在主线上合，见下一节。

## 合进集成分支与并主线

叠在集成分支上的路（dispatch-task「叠在集成分支上的路」）逐路合进集成分支，全部合完再一次并进主线。

**逐路合进集成分支**
- 在集成分支的 worktree（S 是 `../pandora-s`）里 `git merge --no-ff <分支>`，按设计的合并顺序（S：S0 → S1 → S2 → S3 → S4a → S4b → S7 → S8 → S9 → **S10**；S5u → S5 → S5b、S6 直接进主线，见主目录 `.claude/server-session-design.md` §10）。
- **不冻结 upsegments**：这些迁移用的是临时号，并主线前还要重编号，现在冻了，重编号后冻结表就对不上。
- 推集成分支，`wait-status.sh` 与 `wait-github.sh` 都退出 0，再开依赖它的下一路。
- 主线前进、集成分支要用到时，在同一个 worktree 里 `git merge feat/panel-redesign`，推送等 CI 绿，再通知在跑的子路合集成分支。

**并主线**（全部合完、集成分支 CI 绿之后，一次）
1. 在集成分支上合最新主线，按 new-migration skill「集成分支的相对编号与重编号」把临时号改成主线号，作为集成分支的最后一个提交。
2. 对整条集成分支再跑一次 `triggers.py feat/panel-redesign feat/panel-redesign-s`：命中的每个领域，都要能对上某一路的审查记录；只在重编号提交或合并解冲突里出现的改动，自己读 diff。
3. 推集成分支，等全量 CI：`wait-status.sh` 与 `wait-github.sh` 都退出 0，PG18 0 SKIP，往返通过。
4. 在主目录主线上 `git merge --no-ff feat/panel-redesign-s`，这时才按上一节对全部 S 迁移（重编号后的文件名）追加 upsegments、跑 `--check`，推主线、等 CI。

## 一波合并

一波几路连着合（包括叠成一个分支逐步做 A/B 的）时，合并顺序、合时要改的、合后要改的散在 TASKS、brief 和各轮修复消息里，漏一件就是主线红或闸门空跑。照 `templates/wave-merge.md` 建一张表，放进主目录 `.claude/TASKS.md` 的「合并前后要做」。

- **当场登记**：brief 或修复消息里写着「总协调合并时改」「我合并时改」的条目，发出消息的同时登记进表；审查报告的整合风险、实现方报告里「需要总协调配合的事」也登记。
- **顺序与依赖**：有迁移的按号段从小到大（dispatch-task「坑」）；依赖列写「在 X 之后」和原因。分支@头写验收通过的那个头，之后再推的提交要重新验。
- **合后要改的 skill 与规则**：哪个 skill 哪一行会随这一路合入而过时，合完当场改，不留到下一轮 skill 审查。
- **新机制**：合入后会自己动的东西（CI 闸门、定时任务、机器人），合入当场向用户说明是什么、会做什么、是否已生效。
- 合完一路勾一行，写合并提交 sha；整波推送、CI 全绿、「合后要改」与「新机制」都做完，这一波才算结束。

## 交给用户

按根 CLAUDE.md 收尾附表：子任务 | 执行者（模型） | 结论 | 证据（命令、sha、CI 号、判分结果）。同时在主目录 `.claude/TASKS.md` 打勾写结论。

## 坑

- 自动模式会拦 `git reset --hard`、force push、删 worktree、改别的会话分支的 ref，也会顺带拦紧跟着的查询。要改写分支，用 `git commit-tree` 复用原树拼好，再把 `update-ref` 和 `--force-with-lease` 命令交给用户。
- 分支自己的 CI 没出结论不合（推送故障时等用户解锁，见 ci-triage 的坑与根 CLAUDE.md「环境与工具坑」）。
- PG18 夹具撞号是最常见的 CI 红：报告里出现「PG18 首推失败、改夹具 id 后绿」属正常，合并前 grep 一下新夹具的 id 前缀在主线上没有别人在用（细则见 `.claude/rules/platform-pg18.md`）。
- 子 agent 常把「为新 PG18 域在 run-pg18-gates.sh 加一行」「为新方法改同包的一个小文件」列为越界：只要是登记性的一两行就接受，记进 TASKS 结论。
- `check-ownership.sh` 每路都跑，不只信报告里自报的越界。
- 几路同改一个 `CREATE OR REPLACE` 的函数：见根 CLAUDE.md「环境与工具坑」。

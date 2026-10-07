---
name: accept-task
description: pandora 总协调验收任务分支并合进主线：读 report.md、查越界改动、独立重跑关键命令、核 CI、性能类用评测集判分、合并与冲突处理、推送后等 CI、向用户出结论表。用户说「X 做完了」「验收」「合并」「核对子 agent 的结果」，或后台子 agent 交回报告时使用。
---

# 验收任务分支

目标：只合进「证据自己核过」的改动。子 agent 的报告是线索，不是结论；数字、行为、CI 都要自己看一遍。派任务见 dispatch-task skill，验证命令与 CI job 见 verify skill。

## 先判断是不是已经验过

用户会把报告贴错会话或重复贴。先 `git log --oneline feat/panel-redesign..<分支>` 和主目录 `.claude/TASKS.md` 对一下。

## 要核的东西

1. **范围**：`git diff --stat <基点>..<分支>`；`scripts/check-ownership.sh <基点> <分支> <归属清单文件>` 列出归属外的改动。越界的要么有报告里的理由、要么退回。
2. **关键 diff 自己读**：迁移（Up/Down、编号、RLS、授权、追加写、触发器）、权限与认证、锁与事务边界、缓存的失效路径、对外 JSON 字段。
3. **独立重跑**：按 verify skill 的本地层，对改到的包重跑 vet/test；报告里引用的关键命令挑几条重跑。
4. **按改动类型补自证**：
   - 纯挪动或拆文件：`cd panel && go run ./tools/refactorcheck compare -base <sha>^ -head <sha> -tests`（pdnd 加 `-C ../pdnd`），必须 PURE MOVE；
   - 只改注释或文字：`scripts/comment-only.sh <base> <head>` 必须为空；
   - 声称没改 SQL：`go run ./tools/refactorcheck sqlset -base <base> -head <head>` 必须 UNCHANGED；
   - 改了 SQL 的性能项：用评测集判分（训练集与留出集都不变差、改前改后结果一致；只训练集变好算过拟合，退回）。
5. **CI**：`ops-local/memoh-ci/wait-status.sh <分支头 sha>`；动了数据层、SQL、迁移、前端或 pdnd 内核，再 `wait-github.sh <sha>`。PG18 必须 0 SKIP；grep 新增测试名，确认真跑了。

## 合并

- 在主目录主线上 `git merge --no-ff <分支>`。冲突多在共享热点（`cmd/*/main.go`、router、config、`.claude/rules`），保留双方；语义冲突（一方删了另一方新用到的东西）合完立刻编译测试。
- 一波几路合完再推主线（长期授权：`feat/panel-redesign` 可推），推完等检查机与 GitHub 全绿才算完成。
- **推 main 每次都要先问用户**；删 worktree、删本地或远端分支，把命令给用户或得到同意再做。

## 交给用户

按根 CLAUDE.md 收尾附表：子任务 | 子 agent | 结论 | 证据（命令、sha、CI 号、判分结果）。同时在主目录 `.claude/TASKS.md` 打勾写结论。

## 坑

- 自动模式会拦 `git reset --hard`、force push、删 worktree、改别的会话分支的 ref，也会顺带拦紧跟着的查询。要改写分支，用 `git commit-tree` 复用原树拼好，再把 `update-ref` 和 `--force-with-lease` 命令交给用户。
- zsh 下变量不按空格拆分；sed 表达式含 `#` 会坏；命令行里内嵌的 python 遇到单引号会出错。改文档一律把 python 写进 scratchpad，或用 heredoc `<<'EOF'`。
- 前台 `sleep` 被拦；等 CI 只用 wait-status.sh / wait-github.sh，后台跑。
- `gh run list --commit` 传短 sha 会静默返回 []，要先转成完整 sha（等待脚本已处理）。一次推多个提交，只有最新那个有 run。
- 注释里写 `RESERVED-TABLES.md` 登记的保留表名会触发表登记簿测试。
- 偶发 `TestIdempotencyMiddlewarePG18` 锁竞争超时：`gh run rerun <id> --failed`。Actions 因付款失败不启动：`gh run rerun <id>`。同一提交有时先绿、再出一组新运行，以最新一组为准。
- 检查机红而 GitHub 绿，先怀疑容器环境（`/dev/fd`、apt 包随重启丢失），不要直接改代码。
- 只改仓库根文档不触发任何 workflow；被路径过滤的目录里的任何文件（含规则文件）都会触发对应 workflow。
- 推送走 SSH，1Password agent 锁着会签名失败：请用户解锁，不要改走 HTTPS。

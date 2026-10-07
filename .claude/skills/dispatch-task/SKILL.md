---
name: dispatch-task
description: pandora 总协调把工作派给任务会话或实现型子 agent：从主线开 worktree、按文件切归属、分迁移号段、写开工说明（brief）、启动子 agent 并登记清单。要「派任务」「开 worktree」「写 brief / 开工说明」「拆给子 agent 并行做」「开第 N 波」时使用。
---

# 派任务

目标：每个任务在独立 worktree 里由一个 agent 做完，交回一份可验收的报告；几路并行时互不踩文件、不撞迁移号，合并时冲突可预期。验收见 accept-task skill。

## 派之前

- **开工说明里的事实先核实**：符号、文件、行号、表名一律在当前主线 grep 一遍。总协调记忆、旧报告、子 agent 转述里的行号常常过时（实测报告的行号以当时的提交为准），按符号找。
- **按文件切归属，不按主题切**。同一文件只给一路；绕不开的共享热点（`cmd/*/main.go`、`api/*/router.go`、`platform/config/config.go`、同包的大文件）要么整体给一路，要么写清「只许改哪一个函数或哪一行」，其余路在报告里提需求。
- **迁移号段**：先看主线最大号（`ls panel/migrations | tail`），每路预分一段，用不到就空着（脚本允许空号、严格递增即可）。
- 小任务不拆，拆分本身有成本；只读调研可以随便拆。

## 开 worktree

`bash .claude/skills/dispatch-task/scripts/new-worktree.sh <名字> [基点，默认 feat/panel-redesign]`
建 `../pandora-<名字>`、分支 `feat/panel-redesign-<名字>`，并确认 `.claude/brief.md` 被 git 忽略。

## 写开工说明

`.claude/brief.md` = 任务专属部分 + `templates/common-rules.md`（通用规则，按需改背景段）。任务专属部分写：

- 目标，以及可量化的完成标准（用实测数字，不写「优化一下」）；
- **归属**：只许改的文件或函数；**不碰**：别的路在改什么、你会从中得到什么；
- 任务清单：每条给机制、证据位置、修法方向，不写成逐步操作；
- 验证补充：这个任务额外要等哪些 CI job、要哪些自证（refactorcheck、sqlset、PG18 用例）。

## 启动

- 子 agent 用 `templates/agent-prompt.md`，后台运行，给 worktree 的绝对路径。
- 用户自己开会话时，给一段「发给新会话」的原话，内容同 agent-prompt。
- 在主目录 `.claude/TASKS.md` 记下：名字、目录、范围、迁移号、派出的基点。

## 坑

- Agent 工具的 `isolation: "worktree"` 起点常是旧的 main 而不是任务分支：不要用它，手工建 worktree，在 prompt 里给绝对路径，并要求开工先核对 HEAD。
- 实现型子 agent 不要再拆实现型子 agent：同一个 worktree 里并发改文件、并发 `go test` 会互相踩。只读调研可以拆。
- worktree 里没有 `ops-local/`：证据和等待脚本都给主目录的绝对路径。pre-commit 在 worktree 里加载不到私有 gitleaks 规则（已知缺口），仓库公开红线要在 brief 里写明。
- 主线在任务进行中前进了，检查机按全部 job 回放，旧基点会误红：brief 里要求交付前 `git merge feat/panel-redesign`。
- 性能任务：留出评测集的规模与细节不要写进 brief，防止实现方对着评测调参（见 bench 流程）。
- 报告：用户开的任务会话写 worktree 的 `.claude/report.md`；**Agent 工具派的子 agent 写不了报告文件**（环境拦截，要求以文本返回），它的最终消息就是报告，总协调代存到 `.claude/report.md`。都不用跨会话消息（要用户手动批准，常过期送不到）。
- 子 agent 在沙箱里连不上 1Password SSH agent，推送报签名失败；要么关沙箱重试，要么由总协调代推，不改走 HTTPS。
- 推送任务分支是长期授权（`feat/panel-redesign` 开头只为跑 CI）；推 main、删 worktree、删远端分支都要另外问用户。

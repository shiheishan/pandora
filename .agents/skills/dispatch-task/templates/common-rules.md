## 通用规则

**背景**：<一两句：为什么做这件事、实测或用户给的依据、这一波有几路、各管什么>

**证据**（只读；worktree 里没有 ops-local，用主目录绝对路径）：<列出报告、数据、EXPLAIN 原文的绝对路径>。

**工作目录与分支**：只在本 worktree 里改。开工先 `git log -1 --oneline`，应为 <基点短 sha>，不是就停下报告。不碰 main、不碰别的分支、不 force push、不删 worktree。只推自己的 `feat/panel-redesign-<名字>` 分支，不推别的远端分支。上游 `<上游>` 若在你工作期间前进，交付前 `git merge <上游>` 跟上再推（Cursor 的 Composer：只合上游、提交，不推送）；不要合别的分支。

**开工**：先读根 CLAUDE.md、`.claude/skills/verify/SKILL.md`、与你的目录相关的 `.claude/rules/*.md`；然后按根 CLAUDE.md「长任务的任务清单」在本 worktree 建 `.claude/TASKS.md`；上下文压缩后先读它。

**文件归属**：只改上面「归属」里列出的文件（及你新建的文件）。需要动别人归属的文件时不要改，写进报告「需要别的路配合」。

**执行者**（可选）：总协调派 opus 主 agent 时，在任务文件里加 `## 执行者` 小节（make-brief 拼进 brief），逐条写 `#<清单编号> → Composer`，或 `#<清单编号> → opus 自己写（属根 CLAUDE.md「opus 自己写代码只在这几种情况」的哪一类，一句理由）`。没列出的条目由 opus 按根 CLAUDE.md 那几种情况自己定，自己写的在报告「转手」一节写明属哪一类，判不准就交 Composer。测试仍由 opus 先写。总协调直派整路 Composer 时省略本节。

**迁移号**：只用分配给你的号段，没用到就空着；叠在集成分支上的路用相对编号的临时号，见 new-migration skill「集成分支的相对编号与重编号」。每个迁移写 `-- +goose Up` 和 `-- +goose Down`（能逆就逆，不能逆的 Down 里明确 RAISE 拒绝并写原因）。保留表名不进 Go 源码（含注释），见根 CLAUDE.md「环境与工具坑」。

**代码约定**：按根 CLAUDE.md「既定范式」与 800 行规则；行为改动配测试，需要真库的按现有 PG18 测试写法（`platform/pg18test`）。夹具租户 id 先 grep 上游确认没人用（同域共库，撞号是最常见的 CI 红）。保持 RLS、租户、审计、追加写等数据库不变量；不削弱任何安全检查来换性能。不引入新依赖（确有必要先写进报告）。

**性能类任务**：每项改动在报告里写「机制 → 改前 → 改后 → 预期效果」；改了 SQL 就在报告附录给出改后 SQL 的可直接 EXPLAIN 版本（参数内联），性能项一律走 perf-gate 判分（SQL 用评测集，Go 热路径与资源占用用同机 A/B），报告里给的是判分要的材料。

**仓库公开红线**：见根 CLAUDE.md；gitleaks 命中必须停下处理。

**提交**：按主题小步提交，信息用英文祈使句，前缀 perf:/fix:/feat:/test:/docs:，结尾加 `Co-Authored-By:` 行（按当前会话的署名要求）。

**验证**（**Cursor 的 Composer 不适用本节**：不 build、不 test、不推送、不等 CI；验证与推送由派你的人或总协调收回后做）：本地跑什么、推送后必须等哪个 CI 结论，一律按 verify skill（go test 加 `-p 2`；本机 PG18 用例会跳过，跳过不等于通过）。推送 `git push -u origin <你的分支>`。签名失败：Claude 子 agent 按根 CLAUDE.md「环境与工具坑」处理（Bash 设 `dangerouslyDisableSandbox: true` 重试）。等待脚本在主目录：`/Users/a1/ai/projects/pandora/ops-local/memoh-ci/wait-status.sh <sha>`、`BRANCH=<你的分支> wait-github.sh <sha>`，不手写轮询（Claude 子 agent 照根 CLAUDE.md「环境与工具坑」的「子 agent 等 CI 或等 Composer」：前台跑、`MEMOH_PICKUP=540`、截断就原样重跑、累计时限）；日志文件名带分支名（草稿目录多会话共用，曾被覆盖）。本机 go 命令加的 `GOTOOLCHAIN` 见根 CLAUDE.md「环境与工具坑」。退出 1 按 ci-triage 查（先核 run 的分支），本机复现、修、再推；退出 2 说明原因后改看 GitHub。本任务额外要等的 job 和自证写在开工说明的「验证补充」里。

**不再拆实现型 Claude 子 agent**；（Claude 子 agent 适用，Composer 不再转手）编码转手 Composer 照 composer-handoff skill（同一 worktree 同时只能有一个 cursor-agent，转手期间你不碰 worktree）。只读调研可以拆。

**要登测试机的任务**：只用 VPC 内网地址打测试流量；跑前估算公网出流量并写进报告；故障注入（iptables DROP、停服务等）要在远端自带撤销，写法见根 CLAUDE.md「环境与工具坑」。

**报告**：按执行者分两种交法。
- Agent 工具派的 Claude 子 agent 写不了这个文件，**最终消息就是报告**（总协调用 accept-task 的 save-report.sh 代存）。
- Cursor 的 Composer 和用户自己开的任务会话：写进本 worktree 的 `.claude/report.md`（git 忽略，不提交）；Composer 的最终回复只写一段简述和报告路径。

**Claude 子 agent 报告内容**：
1. 每项任务：做了/没做、机制、改前改后、证据；
2. 改了哪些文件（`git diff --stat <上游>...HEAD`，三个点：合过上游也只算自己的改动）；
3. 跑了哪些命令与关键输出；
4. CI：提交 sha、wait-status / wait-github 退出码、PG18 PASS/SKIP/FAIL 数；
5. 附录（性能类）：改后 SQL 的 EXPLAIN 版本；
6. 需要别的路或总协调配合的事、新发现的问题、遗留。

**Composer 报告内容**：
1. 每项任务：做了/没做、机制（能写的改前改后、证据）；
2. 改了哪些文件（`git diff --stat <上游>...HEAD`）；
3. 逐条处理与每项对应的提交 sha、没做的及原因；
4. 需要别的路或总协调配合的事、新发现的问题、遗留。

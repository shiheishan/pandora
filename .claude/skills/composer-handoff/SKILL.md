---
name: composer-handoff
description: opus 子 agent 当一路主 agent 时，把编码默认转手给 Cursor 的 Composer（含只读的审查员、设计员把盘点交给它的 --scan 只读盘点）：判断哪些能转、写转手开工单（sub brief）、用 cursor-launch.sh --sub 启动与等待、收回后核 diff 与重跑测试和回退实验、在报告里写「转手」一节。子 agent 想「把这部分交给 Composer」「拆给 Composer」「转手」时使用；总协调直接派整路给 Composer 用 dispatch-task。
---

# 转手 Composer

目标：opus 子 agent 负责拆解任务并分配给 Composer、任务管理、冲突解决、测试和集成；编码默认交给 Composer，Composer **不 build、不 test、不 typecheck，早提交早返回**（用户 10-10 定），编译、测试、回退实验全由 opus 收回后做，质量不打折。规则出处是根 CLAUDE.md「大任务拆子 agent」（opus 那条）；本 skill 只讲怎么做。

## 1. 什么能转

| 转（你定好修法与判据、写好测试之后） | 不转（自己做） |
|---|---|
| 功能与修复的编码实现，包括钱、权限、认证、迁移、节点内核（测试由你先写好、diff 逐行读） | 定判据、边界条件、语义（例：approle 拼接判据、告警算不算失败）、修法有几种要按四栏比的——这些写进开工单 |
| 按清单批量删改、纯挪动拆文件、改名 | 原因不明、要边调边找的 bug |
| 补测试、补变异自检、补守卫（标准写清后） | 代码本身就是判据的：并发原语、认证与会话状态机、守不变量的迁移 SQL 与触发器 |
| 照审查清单的机械修复、文档、注释、RUNBOOK 同步 | 只改几行（开工单加核对比自己改还贵）；同一份开工单返工两次仍不过的 |

判不准时交 Composer。10-10 的教训：总协调开工单里把「这条可以自己写」给得太宽，三路第 3–4 轮几乎全由 opus 自写；现在修复消息有「执行者」一栏逐条写明，没写理由的一律转。

## 2. 写开工单

复制 `templates/sub-brief.md` 到 worktree 的 `.claude/sub-<标签>.md`（标签只用小写字母、数字、连字符；`.claude/` 被 git 忽略，不会提交）。必须写全：

- **只许改的文件**，逐个列；
- **每条要做什么**，写到不需要判断的程度；
- **可核的完成标准**：测试类写「改前红、改后绿」；守卫或变异自检写「每条规则一条只有它能抓住的变异，单退这条规则时那条变异变绿」。w12native 第 10 轮只写了「三条变异」，漏了两条规则的专属变异，是开工单的错，不是 Composer 的错；
- 不写「要跑的命令」：Composer 不跑 build / test / typecheck，按项提交后直接返回。编译错、测试红由你收回后自己修，或写一份新的 sub brief 再转（返工次数记下）。可以把大活拆成几份小的，依次转，每份早交早核。

## 3. 启动与等待

```bash
bash /Users/a1/ai/projects/pandora/.claude/skills/dispatch-task/scripts/cursor-launch.sh <名字> --sub <标签> --log-dir <总协调 scratchpad>
```

- Bash 调用设 `dangerouslyDisableSandbox: true`。模型缺省且只认 composer-2.5。
- **Composer 干活期间不碰这个 worktree**。自己那部分先在 scratchpad 副本里做（`git -C <worktree> archive HEAD | tar -x -C <副本>`），它交回后再搬进来。
- 起完 Composer 后前台跑 `cursor-launch.sh --wait <日志>`（Bash `timeout` 600000，截断就原样再跑；放后台你会提前结束这一轮，见根 CLAUDE.md「子 agent 等 CI 或等 Composer」），不要手写轮询。
- 可以分几份：互不重叠的文件可以并行，同一 worktree 同时只能有一个 cursor-agent（脚本会拒绝第二个），要并行就依次起。
- Composer 只提交、不推送、不等 CI；推送和 CI 归你。

## 4. 收回与核对

1. 读 `.claude/report-sub-<标签>.md`，再读 `git log` 与它的全部 diff，查越界改动（只许改的文件之外有没有动）。
2. 自己跑 build、vet、test、typecheck（Composer 一律不跑）和相关测试；做回退实验（先跑基线，再逐条退掉它的改动看对应测试变红）。不只信它的报告。
3. 有问题：小的自己改，大的写一份新的 sub brief 再转一次，返工次数记下来。
4. 和自己那部分一起推送、等 CI（按 verify skill）。

## 4b. 只读盘点（审查员、设计员用）

只读的 opus（对抗审查员、设计审查员、做设计或调研的）不能用 `--sub`（它要在 worktree 里提交）。规则写死的盘点交 `--scan`：

- 例：把 77 条幂等路由按给定口径逐条列「几个事务、有无外部调用、提交后副作用」；数 `addTraffic(` 的调用点；核设计稿引用的文件:行是否还对。判据要先写死，拿不准的让它列进「待判断」，由你定。
- 在 scratchpad 建目录（如 `<总协调 scratchpad>/scan-<标签>/`），写 `brief.md`：要读的代码目录（worktree 或 `git archive` 副本，只读）、逐条口径、输出表格的列、「报告」一节。
- 启动：`bash /Users/a1/ai/projects/pandora/.claude/skills/dispatch-task/scripts/cursor-launch.sh --scan <目录> --log-dir <总协调 scratchpad>`（`dangerouslyDisableSandbox: true`），再前台跑 `--wait <日志>`（同上）。报告在 `<目录>/report.md`。
- 收回：抽查至少三成条目回读代码；对它读过的 worktree 跑 `git status --porcelain` 确认没被改。它的表只是线索，写进你报告的结论要你核过。
- 「转手」一节同样要写（用时、抽查数、错几条）。

## 5. 报告里的「转手」一节

- 转了哪几条、为什么转（以及哪些没转、为什么）；自己写的逐条写明属根 CLAUDE.md「opus 自己写代码只在这几种情况」的哪一类；
- Composer 用时（日志首末时间或 `--wait` 输出）；
- 核出的问题、返工次数、越界改动数；
- 整体是否比自己全做更快（估计，写依据）。

总协调用这一节统计转手效果，所以每次都要写。

## 坑

- 日志在总协调 scratchpad，文件名 `cursor-<名字>-sub-<标签>.log`；进程意外退出（日志没有 `exit=` 行）时 `--wait` 会提示，按 resume-work「Composer 的路」续跑：`cursor-launch.sh <名字> --sub <标签> --resume "<从哪一步续>"`。
- 报告文件已存在时脚本拒绝启动（防覆盖）：上一份先看完再改名或删掉，续跑用 `--resume`。
- Composer 读不到全局规则（`~/.claude/CLAUDE.md`）。开工指令会让它读根 CLAUDE.md 和开工单；红线类要求（不登服务器、不读 secrets）写进开工单。

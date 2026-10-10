---
name: composer-handoff
description: opus 子 agent 当一路主 agent 时，把机械部分转手给 Cursor 的 Composer：判断哪些能转、写转手开工单（sub brief）、用 cursor-launch.sh --sub 启动与等待、收回后核 diff 与重跑测试和回退实验、在报告里写「转手」一节。子 agent 想「把这部分交给 Composer」「拆给 Composer」「转手」时使用；总协调直接派整路给 Composer 用 dispatch-task。
---

# 转手 Composer

目标：opus 子 agent 只做需要判断的部分，机械部分交给 Composer，收回时质量不打折。规则出处是根 CLAUDE.md「大任务拆子 agent」（opus 那条）；本 skill 只讲怎么做。

## 1. 什么能转

| 能转（规则写死、结果能验证） | 不转（自己做） |
|---|---|
| 按清单批量删改、纯挪动拆文件、改名 | 判据、边界条件、语义怎么定（例：approle 拼接判据、告警算不算失败） |
| 补测试、补变异自检、补守卫（标准写清后） | 涉及钱、权限、认证、迁移设计、节点内核的实现 |
| 照审查清单的机械修复（修法已定） | 修法有几种、要按四栏比较的 |
| 文档、注释、RUNBOOK 同步 | 原因不明、要自己探索的 |

拿不准就自己做。只改几行的活转手不划算（开工单加核对的成本比自己改还高）。

## 2. 写开工单

复制 `templates/sub-brief.md` 到 worktree 的 `.claude/sub-<标签>.md`（标签只用小写字母、数字、连字符；`.claude/` 被 git 忽略，不会提交）。必须写全：

- **只许改的文件**，逐个列；
- **每条要做什么**，写到不需要判断的程度；
- **可核的完成标准**：测试类写「改前红、改后绿」；守卫或变异自检写「每条规则一条只有它能抓住的变异，单退这条规则时那条变异变绿」。w12native 第 10 轮只写了「三条变异」，漏了两条规则的专属变异，是开工单的错，不是 Composer 的错；
- 要跑的命令（go 加 `GOTOOLCHAIN=go<go.mod 版本>`）。

## 3. 启动与等待

```bash
bash /Users/a1/ai/projects/pandora/.claude/skills/dispatch-task/scripts/cursor-launch.sh <名字> --sub <标签> --log-dir <总协调 scratchpad>
```

- Bash 调用设 `dangerouslyDisableSandbox: true`。模型缺省且只认 composer-2.5。
- 然后用 Bash 的 `run_in_background` 跑 `cursor-launch.sh --wait <日志>`，结束时会收到通知，不要手写轮询。
- 可以分几份：互不重叠的文件可以并行，同一 worktree 同时只能有一个 cursor-agent（脚本会拒绝第二个），要并行就依次起。
- **Composer 干活期间不碰这个 worktree**。自己那部分在 scratchpad 副本里做（`git -C <worktree> archive HEAD | tar -x -C <副本>`），它交回后再搬进来。
- Composer 只提交、不推送、不等 CI；推送和 CI 归你。

## 4. 收回与核对

1. 读 `.claude/report-sub-<标签>.md`，再读 `git log` 与它的全部 diff，查越界改动（只许改的文件之外有没有动）。
2. 自己重跑开工单里的命令和相关测试；做回退实验（先跑基线，再逐条退掉它的改动看对应测试变红）。不只信它的报告。
3. 有问题：小的自己改，大的写一份新的 sub brief 再转一次，返工次数记下来。
4. 和自己那部分一起推送、等 CI（按 verify skill）。

## 5. 报告里的「转手」一节

- 转了哪几条、为什么转（以及哪些没转、为什么）；
- Composer 用时（日志首末时间或 `--wait` 输出）；
- 核出的问题、返工次数、越界改动数；
- 整体是否比自己全做更快（估计，写依据）。

总协调用这一节统计转手效果，所以每次都要写。

## 坑

- 日志在总协调 scratchpad，文件名 `cursor-<名字>-sub-<标签>.log`；进程意外退出（日志没有 `exit=` 行）时 `--wait` 会提示，按 resume-work「Composer 的路」续跑：`cursor-launch.sh <名字> --sub <标签> --resume "<从哪一步续>"`。
- 报告文件已存在时脚本拒绝启动（防覆盖）：上一份先看完再改名或删掉，续跑用 `--resume`。
- Composer 读不到全局规则（`~/.claude/CLAUDE.md`）。开工指令会让它读根 CLAUDE.md 和开工单；红线类要求（不登服务器、不读 secrets）写进开工单。

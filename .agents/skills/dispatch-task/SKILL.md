---
name: dispatch-task
description: pandora 大任务的派法：主 Agent 把大任务按模块或主题切成几块，每块从主线开一个 worktree、派一个 opus 子 Agent 当这一块的负责人（它再用 composer-dispatch 把编码派给 composer-2.5），收回后由主 Agent 核对、build/test、合并并清理 worktree。任务大到要切成几块并行、要「开 worktree」「派 opus」「拆成几路」「这块交给子 agent 负责」时使用。中等任务直接用 composer-dispatch，不用本 skill。
---

# 大任务：主 Agent → opus 子 Agent → composer-2.5

规则以根 CLAUDE.md「协作方式」为准，这里只写步骤和模板。

## 1. 切块并登记

- 先调查：读相关代码，定好每块的边界、只许改的文件、共享接口、迁移号段。共享的东西（公共类型、路由表、proto、迁移基线）由主 Agent 先做好并提交，再开各块。
- 同一个文件只归一块。几块要改同一处时，先合成一块，或先单独做完那一处。
- 每块一个短名（小写字母、数字、连字符），登记进 `.claude/TASKS.md`「正在跑」表：短名 | 执行者 | worktree | 状态。cleanup skill 靠这张表判断哪些 worktree 还在用。

## 2. 开 worktree

在主目录执行，基于当前主线：

```bash
git worktree add ../pandora-<短名> -b feat/panel-redesign-<短名> feat/panel-redesign
~/ai/skills/bin/skills link my-skills/composer-dispatch ../pandora-<短名>
```

- 不用 Agent 工具的 `isolation: "worktree"`：它从 main 起，不是当前主线。
- 第二行让 opus 在 worktree 里也能用 composer-dispatch：共享 skill 的链接不入库，新 worktree 里没有。

## 3. 派 opus 子 Agent

Agent 工具，`model: opus`，后台运行；prompt 照 `templates/opus-brief.md` 填好。几块互不重叠时，在同一条消息里并行派。

## 4. 收回、合并、清理

opus 交回后：

1. 看提交与越界：`git log --oneline feat/panel-redesign..feat/panel-redesign-<短名>`，`git diff --stat feat/panel-redesign...feat/panel-redesign-<短名>`，对照任务说明里只许改的文件。
2. 读 diff，不只信交回说明。
3. 在 worktree 里跑 build / test（命令见 verify skill）。小问题自己改；大的写清楚再交给这一块的 opus（SendMessage 续用原来的 agent，它保留上下文）。
4. 认证、权限、钱、迁移、节点通道的改动，合入前另派一个没参与实现的 opus 只读审查（pre-merge-review skill）；改了热路径的过性能闸门（perf-gate skill）。
5. 合进主线：`git merge --no-ff feat/panel-redesign-<短名>`，推送并等 CI（verify skill）。
6. 合入后立刻清理：`git worktree remove ../pandora-<短名>`、`git branch -d feat/panel-redesign-<短名>`；TASKS.md 那一行打勾、写一句结论。

## 坑

- go 与 npm 不要同时跑（根 CLAUDE.md「环境与工具坑」），几块并行时由主 Agent 排好 build/test 的先后。
- worktree 里没有 `ops-local/`，pre-commit 会回主目录加载私有 gitleaks 规则；opus 的 brief 里不要写真实 IP、域名。
- opus 派的 composer 是 `claude -p` 进程，不是 Agent 工具的子 agent，结果由 opus 自己读 launch.sh 的输出。

---
name: cleanup
description: pandora 开发侧资源清理：列出已合并且干净的 worktree、已合并的分支、遗留的 worktree 与 ops-local 原始数据，归档 worktree 里被 git 忽略的报告，清单经用户确认后再删；对照库与测试机的清理分别见 bench-eval 与 test-machine。一波任务合完、worktree 或分支太多、磁盘紧张，或用户说「清理」「删掉合完的 worktree / 分支」时使用。产品的备份恢复不归这里。
---

# 开发侧资源清理

目标：合完的东西删干净，没合完的、正在跑的、只存在于 worktree 里的报告一样不丢。

**删除是破坏性操作。** 按根 CLAUDE.md，删之前整份清单先给用户确认；自动模式也会拦这些命令。本 skill 里只有列清单和归档两步不用问。

## 永远不在清单里

- `main`、`feat/panel-redesign` 两个分支，以及主目录本身。
- 正在跑的任务：以主目录 `.claude/TASKS.md`「正在跑」表为准。状态不以 ✅ 开头的行里出现的 `w<波次><名字>` 一律保留，包括「运行中」「退回修复中」「已交回待合」。
- 刚派出、还没有提交的 worktree：它的头就是基线，看起来「已合并且干净」，**只有 TASKS 表能保护它**。所以派工时先登记再开工（dispatch-task）。表里漏了的，用 `KEEP="w7xxx …"` 补。

## 步骤

### 1. 列清单（只读）

```bash
bash .claude/skills/cleanup/scripts/list.sh          # 约 5 秒
bash .claude/skills/cleanup/scripts/list.sh --size   # 另统计每个候选 worktree 的占用
```

在主目录或任一 worktree 里跑都一样。它不 fetch，远端结论以上次 fetch 为准，输出第三行有时间。判定规则（保留、「待确认」、候选的各种情形）见脚本文件头。其中两条容易忽略：

- 找不到运行中表：全部「待确认」，不进候选。
- `.claude/` 下有文件在 `ACTIVE_MIN` 分钟内（默认 120）改过：「待确认」，可能是刚合完的收尾，也可能有人还在用。

候选每行带三样东西：要归档的 `.claude` 忽略文件个数、TASKS.md 里还有几处引用 `pandora-<名字>/`、是否已有归档。

本地分支分三组：没有 worktree 的已合并分支、跟着候选 worktree 一起删的、保留的。远端分支只列已合并进 `origin/feat/panel-redesign` 的，本地 worktree 在保留名单里的也跳过。

另外两节：
- **scratchpad 与项目外的 worktree**（例 `scratchpad/trial`）：会话结束后目录可能还在，登记也还在。目录已经没了的显示 prunable。
- **ops-local 的 raw 目录**及大小。

最后打印建议命令，**不执行**。

### 2. 删前核对（总协调）

- 候选对应的任务确实验收合并完了：TASKS 里那一条已打勾，CI 结论已记。
- 报告在不在：任务会话的报告在 worktree 的 `.claude/report.md`。Agent 派的子 agent 的报告要先用 accept-task 的 `save-report.sh` 代存进去，否则归档里没有报告。
- 「待确认」的逐个看：问清有没有人在用。确认已结束的，等改动过了 `ACTIVE_MIN` 再列一次，或用 `ACTIVE_MIN=0` 重跑，并在给用户的清单里注明。
- 有没有开发服务器还从候选目录里跑着：`mock-up.sh status`、`mock-multi.sh status`、`preview_list`。

### 3. 给用户确认

一条消息说清：
- 删几个 worktree（名字、合计占用）；
- 删几个本地分支、几个远端分支（远端是改 GitHub 上的东西，可以让用户单独点头）；
- 归档放在哪；
- 保留了哪些、为什么。

建议命令原样附上。等到明确的「可以」再动；用户只同意一部分，就只做那一部分。

### 4. 执行（按顺序，任何一步出错就停下报告，不加强制参数重试）

1. **归档**：`bash .claude/skills/cleanup/scripts/archive.sh <短名>...`。它把 worktree `.claude/` 下所有被 git 忽略的文件（`brief.md`、`TASKS.md`、`report.md`、`ownership.txt`，以及调查时留下的 `*.sql`、`*.log`）复制到 `ops-local/reports/<日期>/<短名>/`。
   - 只复制，复制后逐个 `cmp` 校验。
   - 目标已有内容不同的同名文件时，不覆盖并以 1 退出。
   - 退出码不是 0 不往下走。先例是 `ops-local/reports/2026-10-06/{contract,nogeb,vultrtest}/`。
2. **删 worktree**：`git -C <主目录> worktree remove <路径>`，一个一个来，不加 `--force`。被拒说明有未跟踪文件、改动或锁，停下来看是什么。
3. **删本地分支**：`git branch -d <分支>`，不用 `-D`。「not fully merged」说明它比上游或基线多提交，停下查，不改用 `-D`。
4. **删远端分支**：先 `git fetch --prune`，再跑一次 `list.sh` 确认名单没变，然后 `git push origin --delete <分支>...`。只删已合并的。
5. 只对 prunable 的登记跑 `git worktree prune`。
6. 再跑一次 `list.sh`，结果贴给用户。

### 5. 收尾

- TASKS.md 里指向 `../pandora-<名字>/.claude/report.md` 的引用，改成归档路径 `ops-local/reports/<日期>/<名字>/report.md`。
- TASKS.md 记一行：日期、删了几个 worktree 和分支、归档路径。

## 其他资源

| 资源 | 怎么清 |
|---|---|
| 对照机上的 `aegis_cmp_*` 库 | 见 bench-eval 第 6 节「结果在哪、对照机上留下什么」：先列给用户确认（动对照机属于改仓库外的东西），只删本任务建的，基线库和模板不碰 |
| 测试机 | 见 test-machine「回收」（删机只由用户在控制台做，规则见根 CLAUDE.md） |
| ops-local 原始数据 | 见下 |
| 会话 scratchpad 里的文件 | 不管，会话结束随系统临时目录清；只处理登记在 git 里的 worktree |

### ops-local 原始数据的保留原则

- **永远留**：成绩单与汇总（`summary.md`、`stats.txt`、`auto-targets*`、`*/results/`）、归档的报告（`reports/`）、评测集（`bench/` 的用例与数据集定义）。
- **可压缩**：各轮的 `raw/`（压测原始日志、pprof、采样）。在原目录旁打成 `raw.tar.gz`，用 `tar -tzf` 核对文件数一致后，删原目录要先问用户。
- **不碰**：`**/secrets/`（不压、不删，也不读）、`gitleaks*`、`memoh-ci/`。
- **有的 raw 里套着 secrets**（例 `vultr-test/raw/secrets/`）：`list.sh` 会在这类 raw 后面标「含 secrets/」。整个 raw 都不压、不删，交用户定。
- ops-local 被 git 忽略、只在维护者本机，没有别的备份，删前多想一步。

## 坑

- **`git worktree remove` 会连同被忽略的文件一起删**：`.claude/` 下的 brief、TASKS、report、ownership 都被 git 忽略，删完就没了，只能靠第 4 步第 1 条的归档。
- **合并判定看的是本地 `feat/panel-redesign`**：主目录没 pull 到最新时，刚在别处合进去的分支会显示「未合并」，这只会让清单更保守。远端那组看 `origin/feat/panel-redesign`，要新就先 fetch。
- 远端分支删了，正在跑的任务下次 push 会重建它。运行中的不会进清单，但如果总协调手里有 TASKS 表没登记的任务，先补 `KEEP`。

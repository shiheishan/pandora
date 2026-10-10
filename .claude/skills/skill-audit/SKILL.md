---
name: skill-audit
description: pandora 的项目 skill 定期审查与落实：按用户固定的两部分标准（现有 skill 里多余或有问题的内容；10 类里该做还没做的），并行派两个 opus 只读审查员（各自先读上一轮报告，只报没修的和新出的），总协调抽查核实、把驳回记在报告文末，再按 skill 归属开 worktree 分路修（sonnet 改删、opus 新建），最后走 accept-task 合并。用户贴来「审查现在用到的 skill」清单，或说「做第 N 次 skill 审查」「审查一下 skill」时使用。新建某个具体 skill 的写法不在这里，看落实那一路的通用要求。
---

# skill 审查

目标：用户只说一句「做第 N 次 skill 审查」，就按同一套标准审完、核完、修完。用户每轮都整段贴同一份清单（贴的时间记在 `templates/criteria.md`），审查员 prompt 和落实流程照本 skill 的模板走，不再现写。

用户的标准原文在 `templates/criteria.md`，审查员直接读这个文件，不要转述。

## 1. 开审

1. 定轮次：`ls /Users/a1/ai/projects/pandora/.claude/skill-audit-*-part1.md`，N 取最大号加 1。第一轮没有存报告，从 2 开始编号。
2. 先在 `.claude/TASKS.md` 记一条「skill 第 N 次审查」，后面每步都在这条上追加。
3. 查有没有正在跑、会改 skill 的任务：看 TASKS「正在跑」和 `git worktree list`。会改的 skill 写进审查员 prompt 的「这次不用看的」，落实时也不派给别的路。例：第五轮 vpcnode2 的 VPC 复测正在跑，跑完会更新 node-accept，所以第五轮没审也没改它。

## 2. 派两个审查员

两路同时派，都是 `general-purpose` 加 `model: opus`，后台运行，互相看不到对方的结论。

| 路 | prompt 模板 | 报告存成 |
|---|---|---|
| 第一部分：现有 skill 的问题 | `templates/auditor-part1.md` | 主目录 `.claude/skill-audit-<N>-part1.md` |
| 第二部分：缺的 skill | `templates/auditor-part2.md` | 主目录 `.claude/skill-audit-<N>-part2.md` |

- 占位符要填全：skill 个数（`ls -d .claude/skills/*/ | wc -l`）、上一轮落实合并后进来的分支、这次不用看的 skill、禁读目录、上一轮延后的新建项和当时定的时机。
- 两份模板都要求先读上一轮报告**和它文末的「总协调核对」**，只报没修的和新出的。
- 第五轮两路都在 7 分钟内交回。交回后用 accept-task 的 `scripts/save-report.sh <output 文件> <上表的路径>` 存。

## 3. 总协调核对

审查报告是线索，不是结论。

- 会让人动手改错的条目逐条核：「过时」「冲突」两类打开文件:行看一眼；说某个提交修了什么的，用 `git show --stat` 核。
- 说「某工具、某命令不存在」的条目，在总协调自己的会话里核。审查员的工具集和总协调不同。
- 第二部分的「顺带发现」也要核。例：第五轮核了 origin/main 上确实没有 `.github/dependabot.yml`（`git ls-tree -r --name-only origin/main .github`）。
- 核完在报告文末追加一段，下一轮审查员会读它：

  ```
  ---
  总协调核对（MM-DD）：抽查 <几处> 属实；**驳回** <条目>——<理由>。<第二部分采纳哪几个新建>。
  ```

- 哪些自己定、哪些问用户：按根 CLAUDE.md「取舍原则」和 memory decide-by-principles，改写、删减、新建都由总协调定，给出理由。整个删掉一个 skill，或者改动根 CLAUDE.md 的红线、授权边界，要先问用户。例：第四轮 test-machine 的删机授权就是交给用户定的。

## 4. 给用户报告

一条消息说完：两部分各自的结论、驳回了哪几条及原因、准备怎么分路落实。用户没有异议就直接派，不用等回复（根 CLAUDE.md「交互」）。

## 5. 分路落实

- **按 skill 归属切路**，一个 skill 只归一路：
  - 第一部分的改写、删减交给一路 `sonnet`。
  - 每个新建 skill 交给一路 `opus`。新 skill 要连带改的现有 skill 也归这一路。例：第五轮 w11sk-perf 新建 perf-gate，同时负责 prod-retest 和 accept-task 第 25 行。
  - 第二部分里写着「不需要新建，补进 X」的条目，交给拥有 X 的那一路。
- 开 worktree：`bash .claude/skills/dispatch-task/scripts/new-worktree.sh <N 轮前缀>-<名字>`。第五轮用的名字是 w11sk-fix、w11sk-perf、w11sk-design。
- 通用要求：用 `templates/fix-common.md` 填好，存成主目录 `.claude/skill-audit-<N>-common.md`。这个文件 git 忽略，但不像 scratchpad 那样会随会话消失。每一路的 prompt 只写三样：worktree 与分支、通用要求的路径、本路的清单。清单里明写两类「不碰」：别的路的 skill，以及已驳回的条目。
- 在 TASKS 登记每一路的名字、模型、agent ID。
- 交回后按 accept-task 验收：
  - skill 文本短，diff 全文读一遍。
  - 抽两三个新写的事实，自己 grep 核对。
  - 新 skill 要看报告里的试跑经过。

## 坑

- **驳回不记在报告里，下一轮会再报一次。** 第四轮把 resume-work 的「ListAgents 不存在」标成「先核实」，没有记下结论，第五轮又报了一次。实际是总协调会话里有这个工具，审查员会话里没有。
- **不碰正在跑的任务要改的 skill。** 两路改同一个 SKILL.md，合并时必有冲突。另外，现场一轮跑完会改掉口径，提前改等于白改。
- **只改 `.claude` 的提交，GitHub 不触发任何 workflow。** `wait-github.sh` 会在约 300 秒后以「没有任何 workflow 被触发」退出 2，这是正常的。检查机不看路径，照样回放，但结论反映的是基点代码。落实的那一路改到 `panel/` 或 `pdnd/` 时（例：第五轮 RUNBOOK 加节点机侧章、loadtest 脚本补采集），按 verify 等。
- **旧 worktree 里留着旧版 skill**，在旧 worktree 里开会话会加载过时的内容。这归 cleanup 清理，不算 skill 本身的问题。第四、五轮都报过这一条。
- **项目 skill 都在仓库 `.claude/skills/` 里，经 worktree 分路改。** `~/ai/skills/bin/skills list` 只用来看有没有链进来的共享 skill（到第五轮只有全局 kami）。共享 skill 的原件在 `~/ai/skills/repos/`，改动会影响所有项目，要先告诉用户（全局 AGENTS.md）。
- **找「重复的业务流程」时不要整份读会话记录**，用 `scripts/repeated-prompts.py`：
  - 缺省找用户重复贴过的长消息。文字略有出入的版本会分成两组（第一次贴的清单就和之后的不同），加 `--grep 关键词 --min 1` 一起看。
  - `--agents` 找总协调手写过多次的 Agent prompt 与 SendMessage 消息，按相似度分组，给次数与首末时间。照模板派的会聚成一组；每次措辞不同的（例：第二轮修复消息）聚不起来，用 `--agents --grep 第二轮 --min 1` 逐条看。

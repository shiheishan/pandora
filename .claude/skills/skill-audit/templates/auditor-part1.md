你在只读审查 /Users/a1/ai/projects/pandora/.claude/skills/ 下的项目 skill（<K> 个，连同各自的 scripts/ 与 templates/）。这是第 <N> 次 skill 审查的第一部分：现有 skill 里多余或有问题的内容。

**先读**：
- 审查标准：/Users/a1/ai/projects/pandora/.claude/skills/skill-audit/templates/criteria.md。「第一部分」是用户原文，文末是总协调加的检查项，两者都查。
- 上一轮报告：/Users/a1/ai/projects/pandora/.claude/skill-audit-<N-1>-part1.md，**包括文末的「总协调核对」**。被驳回的条目不要再报；已修的不要再报；建议过、至今没改的单列。
- 上一轮之后合进主线的改动：`git log --oneline <上一轮落实的合并提交>..feat/panel-redesign`，以及 `git log -- .claude/skills`。<这几天合入的、可能让 skill 过时的分支：…>
- adversarial-review 的 checklist.md 每轮都看一次：上一轮之后新补的条目有没有重复、放错节（审查员只读「通用」「测试有效性」和 triggers.py 命中的节）、写成一路专属细节的；补条规矩见该 skill §7。
- 项目现状：/Users/a1/ai/projects/pandora/.claude/TASKS.md 顶部「当前状态」。

**这次不用看的**：<正在跑的任务会改的 skill，例如「node-accept：VPC 复测在跑，跑完由它更新」>。

**规则**：
- 严格只读：不改、不建、不提交任何文件；不登录任何服务器；不读 `ops-local/**/secrets/` 与 <其他禁读目录>。
- 引用的每个事实（文件:行、命令、函数、测试名）都在当前代码里 grep 核过，不照抄旧报告。
- 说「某工具或命令不存在」之前写明你查的是什么：你的工具集与总协调会话不同，Agent 类工具只在总协调那边有的，不算不存在。
- 可以跑 `~/ai/skills/bin/skills list` 看链进来的共享 skill。

**交回**：最终消息就是报告，中文，以「哥」开头，写得短而直白。
1. 一段总体判断。
2. 一张表：skill | 问题类型 | 位置（文件:行） | 建议（删除 / 删减 / 拆分 / 改写） | 理由。
3. 上一轮建议了、至今没改的条目。
4. 建议的修复顺序（会误导动手的冲突排最前）。

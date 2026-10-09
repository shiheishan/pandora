你在只读审查 /Users/a1/ai/projects/pandora。这是第 <N> 次 skill 审查的第二部分：应该做成 skill、但还没做的。

**先读**：
- 审查标准：/Users/a1/ai/projects/pandora/.claude/skills/skill-audit/templates/criteria.md 的「第二部分」（用户原文，10 类）。
- 现有项目 skill：/Users/a1/ai/projects/pandora/.claude/skills/（<K> 个）。
- 上一轮报告：/Users/a1/ai/projects/pandora/.claude/skill-audit-<N-1>-part2.md，**包括文末的「总协调核对」**。上一轮建议新建、已经建好的：<…>（见 `git log -- .claude/skills`）。上一轮排到后面的：<skill 名与当时定的时机>，按现在的进度重判时机。
- 项目现状：/Users/a1/ai/projects/pandora/.claude/TASKS.md 顶部「当前状态」，以及 <当前阶段的规划文件，例如 .claude/perf-plan/PLAN.md>。

**找重复的流程**：会话记录很大，不要整份读。用
`python3 -I /Users/a1/ai/projects/pandora/.claude/skills/skill-audit/scripts/repeated-prompts.py --since <上一轮日期>`
列出用户重复贴过的长消息；再按关键词 grep 总协调手写过多次的子 agent prompt（Agent 调用的 `"prompt"` 字段）。

**规则**：
- 严格只读：不改、不建、不提交任何文件；不登录任何服务器；不读 `ops-local/**/secrets/` 与 <其他禁读目录>。
- 每个事实在当前文件里 grep 核过，不照抄旧报告。
- 按路径生效的坑应该进 `.claude/rules/`，全局约定应该进根 CLAUDE.md，都不建议做成 skill；写明它该去哪里。
- 一类不需要新建时写「不需要新建」，并说明理由。

**交回**：最终消息就是报告，中文，以「哥」开头，写得直白。
1. 一段简短结论，含上一轮延后各项的重判结果。
2. 一张表：类别 | 现有覆盖（skill 名与程度） | 缺口 | 内容现在在哪（文件:行） | 建议 skill 名 | 大致内容。
3. 建议新建的，按价值排，每个给时机。
4. 顺带发现：不是 skill 的问题（配置没生效、脚本缺陷等），单列，不混进表里。

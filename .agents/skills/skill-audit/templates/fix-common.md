## 通用要求（第 <N> 次 skill 审查落实）

- 工作目录是你的 worktree（见 prompt），基点 <sha>。所有读写都在 worktree 里，用绝对路径。不要改主目录 /Users/a1/ai/projects/pandora 的任何文件：只可以读主目录的 `.claude/*.md` 证据（Claude 路另可调用 `ops-local/memoh-ci` 的两个等待脚本）；不读 `ops-local/**/secrets/` 与 <其他禁读目录>。不登录任何测试机（<正在用的机器> 都有别的路在用）。
- 审查报告就是你的依据：/Users/a1/ai/projects/pandora/.claude/skill-audit-<N>-part1.md（现有 skill 的问题）与 part2.md（缺的 skill）。先读与你相关的部分，**包括文末的「总协调核对」**，被驳回的条目不做。
- skill 写法：
  - 先看 2–3 个现有 skill（如 `.claude/skills/ci-triage`、`db-query`、`new-migration`）的格式与密度，照着写。
  - frontmatter 只有 name 与 description。description 写清「做什么 + 什么时候用 + 和相邻 skill 的分工」，触发词具体，不宽泛。
  - 正文中文、短句；只写不看就不知道的本项目事实、步骤、坑；不写通用编程知识，不复述目录结构与脚本文件头（指过去即可）。
  - 一个 skill 只做一类事；与 `.claude/rules`、根 CLAUDE.md、其他 skill 重复的内容改成引用，不抄。
  - 所有事实（文件路径、命令、函数、测试名）都在当前代码里 grep 核实，不凭报告转述。
- 仓库公开：不写任何真实 IP、域名、后台前缀、口令。

### Cursor 的 Composer 路

- 照开工指令：不跑任何脚本或测试，每改完一处就提交，不推送；报告写进 worktree 的 `.claude/report.md`（不提交）：每处改动对应审查报告的哪一条、提交 sha、没做的及原因。

### Claude（opus）路

- 新 skill 写完用一个真实场景只读试跑一遍，把发现改进去，报告写试跑经过。
- 提交并推送（git push 设 dangerouslyDisableSandbox: true）。只改 `.claude` 的提交 GitHub 不触发 workflow，不用等；改到 `panel/` 或 `pdnd/` 的，推送后用 /Users/a1/ai/projects/pandora/ops-local/memoh-ci/wait-status.sh 与 wait-github.sh 等到退出 0。
- 你写不了 `.claude/report.md`，最终消息就是报告：改了或新建了哪些文件、每处的依据（审查报告的哪一条）、试跑经过与发现、提交 sha、没做的及原因、需要总协调或用户决定的事。

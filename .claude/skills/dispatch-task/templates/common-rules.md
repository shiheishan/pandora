## 通用规则

**背景**：<一两句：为什么做这件事、实测或用户给的依据、这一波有几路、各管什么>

**证据**（只读；worktree 里没有 ops-local，用主目录绝对路径）：<列出报告、数据、EXPLAIN 原文的绝对路径>。不要读 `ops-local/**/secrets/`。

**工作目录与分支**：只在本 worktree 里改。开工先 `git log -1 --oneline`，应为 <基点短 sha>，不是就停下报告。不碰 main、不碰别的分支、不 force push、不删 worktree。主线若在你工作期间前进，交付前 `git merge feat/panel-redesign` 跟上再推。

**开工**：先读根 CLAUDE.md、`.claude/skills/verify/SKILL.md`、与你的目录相关的 `.claude/rules/*.md`；然后在本 worktree 建 `.claude/TASKS.md`（被 git 忽略）列勾选清单，每完成一项打勾写一句结论，新发现当场追加；上下文压缩后先读它。

**文件归属**：只改上面「归属」里列出的文件（及你新建的文件）。需要动别人归属的文件时不要改，写进报告「需要别的路配合」。

**迁移号**：只用分配给你的号段，没用到就空着。每个迁移写 `-- +goose Up` 和 `-- +goose Down`（能逆就逆，不能逆的 Down 里明确 RAISE 拒绝并写原因）。Go 源码（含注释、含从迁移抄进 Go 的 SQL）里不要写 `panel/migrations/RESERVED-TABLES.md` 登记的保留表名（表登记簿测试会把它算作引用而变红；迁移 .sql 本身不参与匹配）。

**代码约定**：按根 CLAUDE.md「既定范式」与 800 行规则；行为改动配测试，需要真库的按现有 PG18 测试写法（`platform/pg18test`）。夹具租户 id 先 grep 主线确认没人用（同域共库，撞号是最常见的 CI 红）。保持 RLS、租户、审计、追加写等数据库不变量；不削弱任何安全检查来换性能。不引入新依赖（确有必要先写进报告）。

**性能类任务**：每项改动在报告里写「机制 → 改前 → 改后 → 预期效果」；改了 SQL 就在报告附录给出改后 SQL 的可直接 EXPLAIN 版本（参数内联），总协调会用评测集判分（性能 + 改前改后结果一致）。

**仓库公开红线**：见根 CLAUDE.md；gitleaks 命中必须停下处理。

**提交**：按主题小步提交，信息用英文祈使句，前缀 perf:/fix:/feat:/test:/docs:，结尾加 `Co-Authored-By:` 行（按当前会话的署名要求）。

**验证（按 verify skill）**：
1. 本地：改到的文件 `gofmt -l`；改到的包 `go vet` + `go test`（`-p 2`）；动了 SQL、表名、路由、权限码加跑 `go test ./internal/platform/db/ ./internal/api/...`；动了迁移跑 `bash panel/deploy/check-migrations_mock_test.sh`；pdnd 同理。本机 PG18 用例会跳过，跳过不等于通过。
2. 推送：`git push -u origin <你的分支>`。签名失败按根 CLAUDE.md「环境与工具坑」处理。
3. 等结论（后台跑，不手写轮询）：`/Users/a1/ai/projects/pandora/ops-local/memoh-ci/wait-status.sh <sha>` 退出 0；动了 panel/internal 数据层、SQL、迁移、前端或 pdnd 内核，再等 `wait-github.sh <sha>` 退出 0（PG18 必须 0 SKIP）。退出 1 按描述本机复现、修、再推；退出 2 说明原因后改看 GitHub。

**不要再拆实现型子 agent**；只读调研可以拆。

**报告**：Agent 工具派的子 agent 写不了文件，**最终消息就是报告**（总协调用 accept-task 的 save-report.sh 代存）；用户自己开的任务会话写进本 worktree 的 `.claude/report.md`。内容：
1. 每项任务：做了/没做、机制、改前改后、证据；
2. 改了哪些文件（`git diff --stat <基点>..HEAD`）；
3. 跑了哪些命令与关键输出；
4. CI：提交 sha、wait-status / wait-github 退出码、PG18 PASS/SKIP/FAIL 数；
5. 附录（性能类）：改后 SQL 的 EXPLAIN 版本；
6. 需要别的路或总协调配合的事、新发现的问题、遗留。

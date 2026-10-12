# 第 <N> 波合并表

合进 <主线 `feat/panel-redesign` / 集成分支 `feat/panel-redesign-s`>，基点 <短 sha>。合完推它，检查机与 GitHub 全绿才算合完。

| 顺序 | 分支@头 | 依赖 | 合并时要改的 | 合后冻结 | 合后要改的 skill 与规则 | 要向用户说明的新机制 |
|---|---|---|---|---|---|---|
| 1 | <名字>@<短 sha> | <无 / 在 X 之后：原因> | <文件:行，改什么，出处（brief / 第几轮消息）；无> | <迁移号；无> | <skill 或规则:行，改什么；无> | <是什么、会做什么、是否已生效；无> |

整波合完核对：

- [ ] 计数类契约合完一并核：DOMAINS、SCRIPTS、`workers.Add`、模板数（根 CLAUDE.md「环境与工具坑」）。
- [ ] 带迁移的各路已追加 upsegments，`upsegment-sha.py --check` 通过（见本 skill「合并」）。合进集成分支的不冻，「合后冻结」写「并主线时」。
- [ ] 推送后 `wait-status.sh`、`wait-github.sh` 都退出 0，PG18 0 SKIP。
- [ ] 「合后要改」逐行做完；「新机制」已向用户说明。

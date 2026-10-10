---
name: resume-work
description: pandora 会话被打断后的恢复：API 额度用完、桌面 app 或总协调会话重启、本机断网之后，先盘点正在跑的后台 agent（含 Cursor 的 Composer）、任务 worktree 和测试机上的远端测试，再续跑并要求先回读现场（Claude 子 agent 用 SendMessage，Composer 用 cursor-launch.sh --resume 另起一次）；核对远端临时改动（iptables、sysctl、ufw）和被中断的时间窗，在报告里记偏差，不重跑已完成的段。agent 没死只是卡住（推送签名失败、1Password SSH agent 锁着）也在这里。用户说「刚才断了」「额度恢复了」「接着做」「app 重启了」「1Password 已解锁」「几路停住了」时使用。只给用户汇报进度用 status-report；CI 红了用 ci-triage。
---

# 会话中断后恢复

目标：中断之后先弄清「谁还在跑、跑到哪、现场有没有残留」，再续，不凭记忆猜，也不把做完的段重跑一遍。10-07、10-08 两天出过 3 次以上中断（API 周额度用完、本机代理断网、总协调会话重启、桌面 app 退出），其中一次把半开演练从 4 分钟拖成了 31 分钟（`ops-local/nodeaccept/REPORT.md`「中断与偏差」）。

## 1. 先盘点（只读）

```bash
bash .claude/skills/status-report/scripts/snapshot.sh
```

- 主线头与 CI、各任务 worktree 的未合提交与未提交改动、TASKS 未完成项，都在这份输出里。
- 再读主目录 `.claude/TASKS.md` 顶部的「压缩后先读」与「正在跑」表：每路一行，`agent <ID>` 写在「在做什么」里。表里没写 ID 的，从本会话的启动通知里找；都找不到就问用户，不要另起一个新 agent 重做。
- 本会话收到过完成通知的 agent 已经结束；没收到的当作还在跑或被打断，不猜结果。
- Composer 的路在 snapshot 的「cursor-agent」一节：TASKS 里登记的每份日志是在跑、已结束（`exit=N`）还是被打断，以及没登记的 cursor-agent 进程。

## 2. 续跑后台 agent

### Claude 子 agent

- 用 SendMessage 按名字或 ID 续，不新开（新开会丢掉它的上下文，还可能和旧的同时改同一个 worktree）。
- 续跑消息第一句固定：
  > 会话刚才中断过。先回读现场和本 worktree 的 `.claude/TASKS.md`：`git log` 看自己做过的提交，`git status` 看没提交的改动；登过测试机的，按机器的 AGENTS.md 只读核对远端进程和临时改动。已完成的段不要重跑；被中断的时间窗记进报告的「中断与偏差」。核对完再接着做下一项。
- 一次续一路，等它回一句「现场如何」再续下一路，免得几路同时登同一台测试机。

### 没死、只是卡住（先别发消息）

典型：1Password SSH agent 锁着，几路的 `git push` 或 ssh 同时报签名失败，agent 都停在那一步（10-09 夜三路这样停住，总协调手写了 3 条「已解锁，继续」，没按上面的首句）。根因和处置见根 CLAUDE.md「环境与工具坑」：沙箱重试仍失败就要用户解锁，这是没有用户就无法继续的情形。

1. 确认已经解锁。主目录里跑 `git ls-remote --heads origin feat/panel-redesign`（只读，Bash 设 `dangerouslyDisableSandbox: true`）。仍报签名失败，就请用户解锁，解锁之前不续任何一路。
2. 逐路查它的后台链路有没有自己续上：
   - 推送：`git -C <worktree> fetch -q origin && git -C <worktree> rev-list --count origin/<分支>..HEAD`，0 就是已经推上去了；报 unknown revision 说明分支还没到远端。
   - CI 等待：`pgrep -fl '^bash .*memoh-ci/wait-(github|status)\.sh'`，输出里的 sha 对得上这一路的头，就是它还在等结论，不要再开一份。
   - 它的 output 文件（启动通知里的路径）末尾还在增长，说明它自己重试成功、已经往下做了。
3. 已经续上的，不发消息（多发一条会打断它在做的事，还可能重复推送）。没续上的，按上面的续跑首句发，首句后面加一句「1Password 已解锁，从被卡住的那一步继续」。仍一次续一路。
4. 查不清它卡在哪（output 文件没有末尾记录），当作被打断，走上面的流程。

### Composer 的路

cursor-agent 收不到 SendMessage，续跑就是另起一次。

1. **判活**：
   - 进程：`pgrep -fl 'index\.js -p .*--workspace'`，看 `--workspace` 后面的目录。不要用 `pgrep -fl cursor-agent`：Bash 工具包着它的那层 shell 命令行里也有这个词，会多出一行假的。
   - 日志：`tail -n 20 <日志>`（TASKS 登记行里的路径）。末行 `exit=0` 是正常结束；`exit=` 非 0 是出错结束；没有 `exit=` 行、进程也不在，就是被打断。
   - `cursor-launch.sh` 起的进程脱离了会话，app 重启后多半还在跑。还在跑的不要再起一份，脚本也会拒绝。
2. **已结束**：走 accept-task，`save-report.sh <日志> <报告>` 核对它写的报告。
3. **被打断或出错**：
   - 先看现场：worktree 的 `git log`、`git status`，`.claude/TASKS.md` 勾到哪，有没有写了一半的报告。
   - 再另起一次：`bash .claude/skills/dispatch-task/scripts/cursor-launch.sh <名字> [--round N] --resume "<从哪一步续>"`。
   - 脚本会在开工指令末尾加续跑段，内容同上面 Claude 的续跑首句：先回读现场，已完成的不重做，中断时间段记进报告的「中断与偏差」。`<从哪一步续>` 写具体，例如「brief 第 3 项，前两项已提交 abc1234」。
   - 不用 cursor-agent 自带的 `--resume`、`--continue`：文本日志里没有会话 ID；几路同时跑时，「上一个会话」是哪一路说不准（推测，没实测）。
4. **提交被 gitleaks 钩子拦下**：Composer 不推送，pre-commit 命中 gitleaks 时会停下写进报告。总协调判定（真秘密按根 CLAUDE.md 红线处理，误报改规则或改写法）后照第 3 步另起，`<从哪一步续>` 写清从哪一项、哪次提交继续。

## 3. 远端测试：先只读核对，再继续

登任何一台前先读 `~/ai/servers/<别名>/AGENTS.md`。核对用：

```bash
bash .claude/skills/resume-work/scripts/remote-state.sh <别名>...
```

它列出远端的测试进程、iptables 与 sysctl 等临时改动、自撤销记录、`stages.log` 末尾和负载（细节见脚本头注释）。

- **临时改动**：被中断的故障注入要先确认撤销了（DROP 计数为 0、自撤销日志最后一行「已撤销 rc=0」），没撤就先撤再说别的。以后限时注入一律远端自撤销（node-accept 第 6 节）。
- **时间窗**：从本机 `ops-local/<轮次>/stages.log` 与远端日志定出中断起止（UTC）。中断期间的流量、采样仍在远端照常记录，但人工动作（抽查、演练开关）可能晚了：受影响的窗口单列或作废，演练时长按实际算。
- **进程**：负载循环、采样器还在跑就不要重起（会出两份数据）；要停按 PID 或 `-x` 精确名，不要 `pkill -f`（根 CLAUDE.md「环境与工具坑」）。
- **不重跑已测完的段**：以 `stages.log` 和结果目录为准；缺数据的段写清缺了什么，问用户要不要补测，而不是默认重跑（重跑要再花机器时间和流量）。

## 4. 记偏差

报告里单列一张「中断与偏差」表：

| # | 时段（UTC） | 事件 | 影响 |
|---|---|---|---|
| 1 | 起–止 | 本机断网 / 会话重启 / app 退出 / 额度用完 | 哪个窗口作废或单列、哪个动作晚了多久、有没有漏做 |

表下写一句「每次中断后先回读了什么」（DROP 规则、面板进程、流量循环、连接数等）。远端进程全部 setsid 运行时，中断本身不影响测试，写明即可。

## 5. worktree

- `git -C <worktree> status --short`：有没有提交的改动。属于这一路的，让它自己续着提交；不是它归属内的文件，先问清楚。
- `git -C <worktree> log --format='%h %an %ad %s' <主线>..HEAD`：有没有「不知道是谁做的提交」。判断法（10-07 w4cert0）：
  - ListAgents 看还有没有别的 pandora 会话在跑；
  - `bash .claude/skills/accept-task/scripts/check-ownership.sh <基点> <分支> <归属清单文件>` 看提交是否只碰了这一路归属内的文件；
  - 两条都对得上，就是这个子 agent 压缩后忘了自己做过，接受并在续跑消息里告诉它。对不上就停下问用户。

## 6. CI

中断前推送过的提交，补等一次结论（脚本在维护者主目录的 `ops-local/memoh-ci/`，见 verify skill）：

```bash
bash ops-local/memoh-ci/wait-status.sh <sha>
bash ops-local/memoh-ci/wait-github.sh <sha>
```

红了转 ci-triage。

## 7. 收尾

- `.claude/TASKS.md` 里「正在跑」表的状态列更新成核对后的事实（续上了 / 已结束待验收 / 等用户）。
- 给用户一段话：断在哪、现场怎样、续了哪几路、哪些结果受影响、要不要补测。

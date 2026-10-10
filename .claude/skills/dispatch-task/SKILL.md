---
name: dispatch-task
description: pandora 总协调把工作派给任务会话、实现型 Claude 子 agent 或 Cursor 的 Grok：从主线（或集成分支）开 worktree、按文件切归属、分迁移号段、写开工说明（brief）、启动并登记清单（Grok 用 cursor-launch.sh 后台起 cursor-agent，含服务器只读巡检的红线片段）；含叠在集成分支上的路（S 的 feat/panel-redesign-s）和删除类任务（先交清单、退场守卫、回退实验）的 brief 写法。要「派任务」「开 worktree」「写 brief / 开工说明」「拆给子 agent 并行做」「开第 N 波」「派 S 的某一路」「派兼容清理 / 删除类任务」「派给 Grok / Cursor」时使用。验收与合并用 accept-task。
---

# 派任务

目标：每个任务在独立 worktree 里由一个 agent 做完，交回一份可验收的报告；几路并行时互不踩文件、不撞迁移号，合并时冲突可预期。验收见 accept-task skill。

## 派之前

- **开工说明里的事实先核实**：符号、文件、行号、表名一律在当前主线 grep 一遍。总协调记忆、旧报告、子 agent 转述里的行号常常过时（实测报告的行号以当时的提交为准），按符号找。
- **按文件切归属，不按主题切**。同一文件只给一路；绕不开的共享热点（`cmd/*/main.go`、`api/*/router.go`、`platform/config/config.go`、同包的大文件）要么整体给一路，要么写清「只许改哪一个函数或哪一行」，其余路在报告里提需求。
- **迁移号段**：先跑 `bash .claude/skills/new-migration/scripts/next-number.sh`（含各任务分支上还没合的在途号），每路预分一段，用不到就空着（脚本允许空号、严格递增即可）。叠在集成分支上的路不分主线号段，见下面「叠在集成分支上的路」。

## 开 worktree

`bash .claude/skills/dispatch-task/scripts/new-worktree.sh <名字> [基点，默认 feat/panel-redesign]`
建 `../pandora-<名字>`、分支 `feat/panel-redesign-<名字>`，并确认 `.claude/brief.md` 被 git 忽略。叠在集成分支上的路，基点给集成分支。

## 写开工说明

`.claude/brief.md` = 任务专属部分 + `templates/common-rules.md`（通用规则）。任务专属部分先写进 scratchpad 的一个 md，再用脚本拼：

`python3 .claude/skills/dispatch-task/scripts/make-brief.py <名字> <迁移号段|无> <任务文件> --background "…" --evidence "<绝对路径>,…" [--shared <共享热点段.md>] [--upstream <上游分支>] [--dry-run]`

它自动取 worktree 的基点 sha、替换模板占位、拒绝覆盖已有 brief。`--upstream` 是子 agent 交付前要 merge 跟上的分支，缺省主线。几路共用的「共享热点」段（谁能改 main.go 的哪个循环体、router 只许动哪几行）写一份传 `--shared`。任务专属部分写：

- 目标，以及可量化的完成标准（用实测数字，不写「优化一下」）；
- **归属**：只许改的文件或函数；**不碰**：别的路在改什么、你会从中得到什么；
- 任务清单：每条给机制、证据位置、修法方向，不写成逐步操作；
- 验证补充：这个任务额外要等哪些 CI job、要哪些自证（refactorcheck、sqlset、PG18 用例）。
- **删除类任务**（兼容清理 C1a–C4、S6 这类以删为主的路）：把 `templates/deletion-task.md` 抄进任务文件，填上设计稿的路径与编号。它规定先交删除清单、总协调核完再删、删完登记退场守卫、用回退实验证明守卫会红；删什么、登记进哪个守卫，引用设计稿，不抄进 brief。

## 叠在集成分支上的路

设计稿规定几路先合进一个集成分支、全部合完再一次并进主线时用（S：S0–S4、S7–S9 叠在 `feat/panel-redesign-s` 上，S5、S6 直接进主线，见主目录 `.claude/server-session-design.md` §10）。和普通路不同的只有下面几处：

- **集成分支本身**：开第一路之前建一次：`new-worktree.sh s`，得到 `../pandora-s` 与分支 `feat/panel-redesign-s`（基点主线）。不写 brief，是总协调合并用的 worktree；只由总协调推送。
- **基点**：`new-worktree.sh <名字> feat/panel-redesign-s`，从集成分支的当前头开。依赖前一路的，等前一路合进集成分支再开（S 的依赖列见设计 §10）。
- **上游与跟进**：每路的上游是集成分支。`make-brief.py … --upstream feat/panel-redesign-s`，brief 里就写成交付前 `git merge feat/panel-redesign-s`；子路不直接合主线。主线前进、集成分支要用到时（例：S2 的 S-03 要改 S6 在主线上重建过的触发器），由总协调在 `../pandora-s` 里合主线、推送、等 CI 绿，再让子路合集成分支。
- **迁移号**：不跑 next-number 分主线号段，用设计给的相对编号（S-01…），文件名用临时号；make-brief 的号段参数写成「S-01、S-02（09001、09002）」。写法与最后的重编号见 new-migration skill「集成分支的相对编号与重编号」。
- **brief 里多写**（上游由 make-brief 写进开头）：依赖的前一路已经在集成分支的哪个提交里；「会撞的文件」里本路的段落（设计 §10）。
- **审查与验收**：`check-ownership.sh`、`triggers.py`、`review-prompt.py first` 的基点都给集成分支，合进集成分支、最后并主线的做法见 accept-task「合进集成分支与并主线」。
- **登记**：TASKS「正在跑」表的基点一栏写 `feat/panel-redesign-s@<短 sha>`。

## 启动

派给谁（opus、Cursor 的 Grok、sonnet）按根 CLAUDE.md「大任务拆子 agent」，拿不准按 opus。开 worktree、写 brief 对 Claude 子 agent 和 Grok 都一样，只有启动和登记不同。

- 要读主目录里的长文档（规划、审计、评估），先把子 agent 的结论存成主目录 `.claude/<主题>.md`（git 忽略），brief 里给绝对路径，别把几千字贴进 prompt。
- 在主目录 `.claude/TASKS.md`「正在跑」表记下：名字、目录、范围、迁移号、派出的基点、执行者与模型，以及续跑凭据（Claude 子 agent 写名字或 ID；Grok 写 PID 与日志路径）。

**Claude 子 agent**
- Agent 调用显式传 `model`。
- 用 `templates/agent-prompt.md`（替换 `<名字>`、`<一句话范围>`），后台运行，给 worktree 的绝对路径。模板已写明：推送设 dangerouslyDisableSandbox、最终消息就是报告。
- 用户自己开会话时，给一段「发给新会话」的原话，内容同 agent-prompt。

## 派给 Grok

命令行的固定写法在根 CLAUDE.md，这里用脚本起，不手抄（10-10 手写三次，第一次把模型写错了）：

```bash
bash .claude/skills/dispatch-task/scripts/cursor-launch.sh <名字> [--dry-run]
```

- 起之前 brief 要在：`new-worktree.sh` 与 `make-brief.py` 照常跑。开工指令由脚本从 `templates/cursor-prompt.md` 填好：先读根 CLAUDE.md 和 brief，报告写进 worktree 的 `.claude/report.md`（不提交），最终回复只写简述。用 `--dry-run` 先看一眼生成的指令。
- 脚本让进程脱离会话在后台跑，日志进本会话 scratchpad，打印 TASKS 登记行（含 PID 与日志）和等待命令。等待命令用 Bash 的 `run_in_background` 跑，结束时会话收到通知；这是 Grok 路唯一的完成通知。
- brief 不用为 Grok 改写：`common-rules.md`「报告」一节已分开两种执行者。Grok 读不到 `~/.claude/CLAUDE.md`，brief 里不能只写「见全局规则」。
- **上服务器**：根 CLAUDE.md 目前只放行只读巡检。把 `templates/server-readonly.md` 抄进 brief 并填好机器清单。纯巡检不开 worktree：brief 放主目录 `ops-local/<目录>/brief.md`，用 `cursor-launch.sh --dir <目录>` 起，报告写同目录 `report.md`。脚本会检查 brief 里有这一节。
- **中途追加范围**：Grok 收不到 SendMessage。等它交回后写进下一次的开工文件（修复轮的 `round-r{N}.md`，或续跑的 `--resume` 段）。
- **交回后**：验收照 accept-task，执行者不同不放宽；是否必审见 adversarial-review 第 1 节。修复轮见该 skill 第 4 节，中断续跑见 resume-work「Grok 的路」。

## 坑

- Agent 工具的 `isolation: "worktree"` 起点常是旧的 main 而不是任务分支：不要用它，手工建 worktree，在 prompt 里给绝对路径，并要求开工先核对 HEAD。
- 实现型子 agent 不要再拆实现型子 agent：同一个 worktree 里并发改文件、并发 `go test` 会互相踩。只读调研可以拆。
- worktree 里没有 `ops-local/`：证据和等待脚本都给主目录的绝对路径。pre-commit 会回主仓库的 `ops-local/` 加载私有 gitleaks 规则，但它只认真实 IP，域名、路径前缀等仓库公开红线仍要在 brief 里写明。
- 性能任务：留出集不进 brief（见 bench-eval）。
- 报告怎么交见 `templates/common-rules.md`「报告」；不用跨会话消息（要用户手动批准，常过期送不到）。
- 有迁移的几路按号段从小到大合（goose 不接受「库里到了 00106 又冒出没跑过的 00104」，见 `rules/panel-migrations.md`）；没迁移的随时合。brief 里写明号段和合并顺序。叠在集成分支上的路按设计的合并顺序合进集成分支。
- 中途追加范围用 SendMessage 发给该 agent（Grok 收不到，见「派给 Grok」），写清新增的归属文件与「不碰」，并在 TASKS 记一笔；别的路归属的文件，改由那一路做（例：订阅地址 /32 问题属 service.go → 发给 w2node，不给 w2render）。
- 推送的授权边界见 accept-task skill 的「合并」一节（任务分支可推，推 main、删 worktree、删分支另问用户）。

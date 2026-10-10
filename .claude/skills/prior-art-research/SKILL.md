---
name: prior-art-research
description: pandora 工程、架构、协议的前人经验调研（知识库）：先查已有知识库只补缺口，按领域切 1–4 路 opus 只读并行（上游源码、规范、论文、事故复盘），每条落到 pandora 的 文件:行与四栏取舍，存成主目录 `.claude/<主题>-kb/<路名>.md`，总协调抽查后把结论落地：必须改设计的交 tech-design，遗留项排进 TASKS，建议进 rules 的指定由哪一路写，产品分歧交用户。用户说「先搜集资料」「有没有前人指路」「看看别人做得好的」「上游怎么做」「XX 协议 / 架构别人怎么实现」，或开协议优化、大设计之前要补知识时使用。分工：产品规则（钱、链接、用户习惯）的「别家怎么做」用 decision-research，结果交用户拍板；本系统怎么设计、占用怎么诊断用 tech-design；本 skill 只产出外部经验和对照，不出设计稿。
---

# 前人经验调研（知识库）

目标：动手前把别人踩过的坑、上游的做法、一手研究查清，每条都对到 pandora 的现状，产物长期可查，并且每条结论都有人接手落地。

先例（10-10，都在主目录 `.claude/`，被 git 忽略）：
- `protocol-kb/`：concurrency、quic、tcp-protocols、detection 四路，产出 S5/S5b 输入、AnyTLS 断线根因、w18proto 一路小修复；
- `arch-kb/`：control-plane、peers、data-path 三路，产出服务器会话 S 设计 v3。

两次的通用要求各写了一遍，八成相同，合并成了 `templates/common.md`。

## 0. 先分清是不是这里的活

| 问题 | 去哪 |
|---|---|
| 产品规则拿不准（钱、链接、用户习惯），要看别家怎么做 | decision-research，结果交用户拍板 |
| 本系统怎么设计、占用怎么诊断 | tech-design |
| 工程、架构、协议上别人怎么做、踩过什么坑 | 本 skill |

混合题先在这里查事实，产品部分照 decision-research 第 5 步交用户。例：Brutal 公平性。上游怎么限速、有没有被识别的研究，是前人经验；「一个人申报高速率能不能挤占同节点其他人」是产品取舍。

## 1. 先查已有知识库

```bash
ls /Users/a1/ai/projects/pandora/.claude/*-kb/
grep -n "<关键词>" /Users/a1/ai/projects/pandora/.claude/*-kb/*.md
grep -n "<关键词>" /Users/a1/ai/projects/pandora/.claude/TASKS.md
```

- 已经查过的，读那几节和文末「总协调核对」。只为缺口开路：已有结论没核的来源（例：只读了摘要页）、没覆盖的上游、没做的实验、pandora 侧没盘全的出现处。
- 缺口小的派 1–2 路；新领域派 3–4 路。
- 已有结论够定的，跳过调研，直接走第 5 节落地。

## 2. 分路与派出

- 按领域切，路与路互不重叠。例：protocol-kb 按「运行时与网络栈 / QUIC 类 / TCP 类 / 识别研究」切；arch-kb 按「控制面 / 同类产品 / 数据路径」切。
- 每路一个 `opus`，只读，后台运行，同一条消息里并行派出。理由：要上网、要判断；根 CLAUDE.md 的 sonnet 只用于浏览器、改服务器和 opus 的下手，拿不准按 opus。
- **通用要求**：用 `templates/common.md` 填占位符，存到主目录 `.claude/<主题>-kb/_common.md`（先 `mkdir -p`）。不要只放 scratchpad：scratchpad 是会话专用的，10-10 两份原件都只在当次会话的 scratchpad 里，下次就找不到调研范围了。
- **每路 prompt**：用 `templates/lane-prompt.md`，写这一路的问题和证据要求，再列其余几路各查什么。几路的 prompt 并排存进 `.claude/<主题>-kb/_prompts.md`，补查和复查时要用。
- 用户原话逐字放进通用要求。路名不要用 `_common`、`_prompts`。
- **补缺口的一轮**存进原主题目录（例：协议优化专题的补查进 `protocol-kb/`），通用要求和 prompt 叫 `_common-<轮名>.md`、`_prompts-<轮名>.md`，路名不和旧报告重名；篇幅改成 150–300 行；「已有知识库」一栏逐节列出要读的旧报告节号。
- 路内拆下手的规则写在模板里：要上网、要判断的派 opus，并且前台派；规则写死的本地盘点交 Composer `--scan`（composer-handoff 第 4b 节）。Composer 能不能上网没核过，要上网的不交它。
- 登记：TASKS 加一条（用户原话、几路、各查什么、`_common.md` 路径），「正在跑」表每路一行。

## 3. 存档

每路交回后：

```bash
mkdir -p /Users/a1/ai/projects/pandora/.claude/<主题>-kb
bash /Users/a1/ai/projects/pandora/.claude/skills/accept-task/scripts/save-report.sh \
  <通知里的 output 文件> /Users/a1/ai/projects/pandora/.claude/<主题>-kb/<路名>.md
```

报告开头是给总协调的「哥，…」交代（全程只读、clone 了哪些、拆了几个下手），原样留着，它说明了证据怎么来的。

## 4. 总协调核对

- 每份报告抽查 3 处，从一页结论里挑最重要的：
  - pandora 的 文件:行 回读；
  - 上游源码或一手页面回读（`仓库@sha 文件:行` 或链接）；
  - 有实验或数字的，能重跑就重跑。
- 再扫一遍：标「摘要」「记忆，未核」的条目不能单独撑起「必须改」；pandora 路径要是仓库相对全路径。
- 结果追加在报告文末一行：`总协调核对（MM-DD）：抽查 3 处属实——…。采纳：…；驳回：…（理由）。` 格式照 `protocol-kb/quic.md` 末行。TASKS 那条下面加一行交回摘要（几时交回、核了几处、要点）。
- 驳回的写理由，不删报告原文。

## 5. 落地

每个附录都要有接手的人，不能只停在 TASKS 的一句话里。

| 附录 | 去向 |
|---|---|
| 1 必须改设计的 | 交 tech-design：在途设计出新版（先例：arch-kb → S v3，设计员 prompt 照 tech-design `templates/designer-prompt.md`，附 kb 路径与采纳清单），命中触发条件的走它第 4 节定稿前审查；还没有设计稿的，作为新设计的输入 |
| 2 遗留项与排序 | 写进 TASKS 对应节（例「协议遗留」），按排序重排；明确的缺陷小修可以直接合成一路交 dispatch-task，合并前走 adversarial-review（先例：AnyTLS SYNACK 的 w18anytls、小修复合集 w18proto） |
| 3 建议进 rules | 几路都交回后统一收：去重、合并同一适用路径的条目。再在接手设计稿的「说明文字的属主」表里指定由哪一路写（先例：`server-session-design.md` §10 的「protocol-kb 各「建议进 rules」…」一行归 S5u / S5 / S5b）。没有设计稿接手的，在 TASKS 开一条，写明哪一路、随哪次合入写 |
| 4 交用户的产品取舍 | 一条消息交用户：每题一个具体人物、具体数字的例子（decision-research 第 5 步）。用户说「拿不准」或要先看别家的，转 decision-research；用户说「先放着」的，记进 TASKS 对应专题，原话保留 |
| 5 来源清单 | 留在报告里，不另抄 |

protocol-kb 的第 ③ 步（稳定规则进 rules，TASKS 那条只写了一句）交回时没指定谁写，到现在也没写进去；后来补在 S 设计属主表里，要等 S5u / S5 / S5b 合入才落地。所以第 3 行派出时就要落到具体某一路。

## 坑

- **子 agent 再派的后台子 agent，完成通知会落到总协调这边。** 10-10 S v3 审查 A 路派的下手交回后，总协调只能转发 output 路径。所以模板要求路内下手前台派。万一有后台的，总协调把通知里的 output 文件路径转给派它的那一路。
- **同名文件**：pdnd 里 `hysteria2` 相关的就有 `pdnd/kernel/hysteria2.go` 和 `pdnd/internal/nativewire/hysteria2/service.go`。quic.md 写的是 `hysteria2/service.go:245-265`，核对时要先猜是哪一个。所以模板要求写全路径。
- **只举一处**：detection.md 说中性页 Go 特征时只列了 `probe_fallback.go:153`。`grep -rn 'http.NotFound' pdnd --include='*.go'` 还能在 vless、trojan、grpc、native transport、hysteria2 找到几处。真要改时要全盘点，这类活交 `--scan`。
- **摘要级证据**：论文、官方页常只拿到摘要（quic.md 读 FOCI 2025 只读了摘要页）。要标「摘要」，要么补读全文，要么只当线索。
- **用户待定题别被调研定掉**：调研员会顺手写「建议默认 X」。综合时把它放进附 4 交用户，不当成已定。
- **行号随基点过时**：通用要求里写基点 sha。落地时主线已经前进的，按符号重新查。

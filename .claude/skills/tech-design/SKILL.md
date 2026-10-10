---
name: tech-design
description: pandora 出技术设计稿或性能调研。步骤：先核现状给 文件:行；选模板（功能设计 / 性能调研；收拢类用功能设计模板）；派 opus 设计员，它可以再拆 sonnet 数数、opus 做判断；总协调回读核证据；工程取舍按原则自己定，只把产品取舍编号交用户；用户定案追加在文末；按文件归属切好路，交 dispatch-task。用户说「出个设计」「X 怎么实现」「调研一下性能 / 占用」「去掉本不该做的工作」「收拢 X」时使用；动钱、认证、节点协议、迁移的大功能开工前，也先用它出设计并过定稿前对抗审查。分工：产品规则拿不准用 decision-research，设计定了拆 brief 用 dispatch-task，合并前审 diff 用 adversarial-review（审代码分支；设计稿的定稿前审查在本 skill 第 4 节），SQL 改写判分用 bench-eval。
---

# 技术设计与性能调研

目标：设计稿一次写到能直接派工。现状有 文件:行，每项有退场和守卫，分路不撞文件，迁移号已分好，留给用户的只有真正的产品取舍。

样本都在主目录 `.claude/` 下，被 git 忽略：
- 功能设计：`purchase-model-design.md`（最完整，派工表、自定取舍、核实记录都有）、`cert-design.md`、`server-binding-design.md`（上游对照、出处清单）。
- 性能调研：`perf-plan/sysview.md`（面板与节点整体分析，先诊断后开方的第一份，perf-study 模板 §0–§3 照它写；它的「过渡」一段写在确认无生产部署之前，不照搬）；`perf-plan/node-summary.md`（总表）加 `perf-plan/node-*.md`（子报告）、`perf-plan/user-paths.md` 是旧的「工作项 → 省多少」写法，只参照数据口径。
- 两份并行调研合成的总方案：`perf-plan/PLAN.md`。
- 定稿前对抗审查：`server-session-design.md`（v2，文末「审查发现处理表」）、`server-session-design-v1.md`，四份报告与当时的 prompt 在 `perf-plan/s-review/`。

## 1. 选模板

| 要做的 | 模板 | 产物位置（主目录） |
|---|---|---|
| 新能力，跨 panel / pdnd，要迁移 | `templates/feature-design.md` | `.claude/<主题>-design.md` |
| 占用、延迟、往返：先诊断（整体、三分类、追根源），再对根源重新设计 | `templates/perf-study.md` | `.claude/perf-plan/<主题>.md`，子报告 `<主题>-<子系统>.md` |
| 收拢：用一套机制替换几套旧做法（纪元缓存、窗口令牌、审计串链器、直装单布局、pdnd 单通道） | `templates/feature-design.md`，第 3 节「被替换的实例」必填；用不上的节写「不涉及」 | `.claude/<主题>-design.md` |

设计稿放在 git 忽略的 `.claude/` 下，因为仓库是公开的。以后要精简进 `docs/` 时，先去掉部署值，再过 gitleaks。

## 2. 派设计员

- 用 `templates/designer-prompt.md` 写 prompt：
  - 用户原话逐字放进去，用户已定的原则逐条列出；
  - 基点 sha；有在途分支的，写「按它合入后的代码为准」，并给 worktree 路径；
  - 实测数据的路径和口径；
  - 要回答的问题，每题写清要什么证据。
- 派一个 `opus`，后台运行，只读。两个子系统互不相干时（例：节点侧和用户路径），并行派两个，prompt 里互相写明对方在做什么、可能撞哪些文件。
- 设计员写不了文件。交回后用 accept-task 的 `scripts/save-report.sh <output 文件> <产物位置>` 存。子报告由设计员在最终消息里给全文，或者由总协调分别存。
- 拆子 agent 的规则写在模板里：数往返、grep 普查、读代码总结用 `sonnet`；判断能否无损去掉、安全论证、架构取舍、上游对照用 `opus`。按子系统切，互不重叠。每个子 agent 交回四样：结论表、每个数字的出处、跑过的命令、哪些是推断。

## 3. 总协调核

设计员的稿子是线索。给用户看之前先核这几样：

- **关键 文件:行**：回读。每个子报告各挑 2–3 条，挑结论里最重要的。
- **数字**：能重跑的就重跑，例如 pprof、pgss 汇总。两份子报告说法矛盾的，以代码为准定下来，在结论里写成「更正」。node-summary 就更正过两处：每节点 2 条 TLS 连接，不是约 1000 条；验签占 19.3%，不是 23%。
- **口径**：每个数字要写清是哪一轮、哪一档，诊断开关开没开。`run-quiet.sh` 缺省 `-online-ratio 0.3`，不是完全静默。w10quiet 把这一档的数当成「静默」来读，直到 node-memory-periodic 指出来，标准才分成了 0 和 0.3 两档。
- **迁移号段**：重跑 `bash .claude/skills/new-migration/scripts/next-number.sh`，它会列出在途分支的号，按路预分。
- **撞文件**：把派工表和下面两类比一遍，每个重叠的文件定唯一属主，或者定先后：
  - 在途分支：`git worktree list` 加 TASKS「正在跑」；
  - 另一份并行设计的派工表。
- **退场**：每个被替换的旧做法要有删除的人和时点。没人认领的，补进某一路。

## 4. 定稿前对抗审查（审设计稿，不审代码）

触发：设计涉及认证、钱、节点协议、迁移之一。只有界面、只有占用调研的不审。

分工：adversarial-review 审任务分支的 diff，合并前、用脚本判断要不要审；这里审设计稿，开工前、看设计内容判断，没有 diff 也没有脚本。设计审过不免去合并前的 diff 审查，两边共用 `adversarial-review/checklist.md`。

先例：服务器会话 S 设计（10-09）派了 4 位审查员，共 73 条发现（认证 20、计费 16、下行与迁移 23、协议与删除 14），按审查出了 v2。

1. **时机**：设计员交回、第 3 节核完之后，给用户看之前。审的是核过 文件:行 的稿子。
2. **选视角**，3–4 位 `opus` 只读，每人只查一个：

   | 设计命中 | 视角 |
   |---|---|
   | 认证、传输、令牌、证书 | 认证（含拒绝服务面） |
   | 钱、计费、额度、流量入账 | 计费（恰好一次、锁序、事务边界） |
   | 收拢或删除了旧做法 | 删除清单（漏迁的语义、误删第三方节点要用的） |
   | 下行配置、版本、迁移 | 下行一致性与迁移（可逆性、号段冲突） |

   S 设计用的就是这四个。再多的视角按设计的实际风险加，别超过 4 位。
3. **派**：用 `templates/design-review-prompt.md` 填占位符，同一条消息里后台并行派出。prompt 里写明其余审查员各查什么。几位的 prompt 只有视角段不同，存一份 `review-prompt.md`（视角段并排写全）放进下一步的目录，复审时要用。
4. **存报告**：审查员写不了文件，用 accept-task 的 `scripts/save-report.sh` 存到主目录 `.claude/perf-plan/<设计名>-review/<视角>.md`（设计名取简称，S 设计是 `s-review/{auth,billing,proto-deletions,downlink-migrations}.md`）。
5. **总协调核**：中危以上的发现亲自回读 文件:行；驳回的写理由。逐条定处理：
   - 工程取舍按第 5 节自己定；产品取舍编号交用户。
   - 把要改的几条写成裁定，连同报告路径发给设计员（SendMessage 续，不新开）。
6. **出 v2**：
   - 旧版另存 `<主题>-design-v1.md`；
   - 文首加一段说明：读了几份报告、总协调定了几条、有没有哪处没照字面改及原因；
   - 正文后、用户定案前，附「审查发现处理表」：

     | 发现（报告名 # 加一句话） | 处理（采纳 / 部分采纳 / 不采纳加理由 / 转别的设计） | 位置（设计稿节号或守卫名） |
     |---|---|---|

   每条发现都要有一行，不能只列采纳的；处理表里的位置要能在 v2 里 grep 到。
7. **要不要再审**：v2 改了认证流程、计费口径这类核心机制时，再派一轮，只查处理表是否改对和新增段落（模板同上，附上一轮报告与处理表）。只是补细节的不再审。

## 5. 拍板点分流

按根 CLAUDE.md「取舍原则」和 memory decide-by-principles 分：

- **交用户**：
  - 产品目标，例如性能标准的数值；
  - 用户看得到的行为变化，例如页面交互改了、数字不再实时、兼容范围；
  - 破坏性的迁移或运维，例如缩短追加写表的保留期，超期数据删了找不回。
- **总协调自己定**：只影响代码的选择，例如用哪种缓存、锁怎么加、协议怎么协商。写成「问题 | 判断 | 性能 | 用户体验 | 安全 | 可维护性」表，四栏每格写「变好 / 不变 / 变差」加证据（实测 / 读码 / 推测）；任一栏变差的不能定，回去改设计。给用户过目就行，不等回复。
- **默认不做**：有损的选项，例如拉长间隔、降低实时、放宽校验。写进「不推荐」并给理由，不交用户。
- 先例：
  - 10-09 总协调把两份性能调研里能按原则判断的「待拍板」都自己定了（PLAN.md「已定的取舍」），只把新标准交给用户。
  - cert-design 的 D3「不做节点自治 ACME」其实是工程取舍，按现在的规矩应该放进自定的表。

## 6. 交给用户、记定案

- 一条消息说清结论和拍板点。每题给一个有具体人物、具体数字的例子，见 purchase-model-design 第 8.1 节。
- 用户回复后，在稿子末尾追加「## 用户 YYYY-MM-DD 定案（覆盖第 X 节）」，原话保留，正文不改。需要出第二版时，旧版另存为 `<主题>-design-v1.md`（先例 cert-design-v1.md）。
- 同时把定案写进 `.claude/TASKS.md` 对应的条目。产品规则还要写进 memory 的产品规则文件。

## 7. 合成与派工

- 两份并行调研要合成一页总方案，结构照 PLAN.md：
  - 已定的取舍；
  - 标准；
  - 收拢；
  - 路与文件属主：同一文件只归一路；
  - 复测；
  - 与换栈的关系。
- 派工交 dispatch-task：设计稿的派工表就是 brief 的「归属 / 不碰 / 迁移号段」；brief 里给设计稿的绝对路径，不贴全文。
- 实现合并时：命中触发条件的走 adversarial-review；性能项按 perf-gate 做改前改后判分。

## 守卫从哪类里挑

设计里每项都要写「谁钉住它」。优先用现成的这几类，新写的守卫也照同类的写法：

| 要钉住的 | 现成的守卫 |
|---|---|
| 某函数里必须有、或不许有某段代码 | `panel/internal/platform/sourcetest` 的契约测试，按声明名取源码 |
| 全仓不许出现某种写法，个别文件豁免 | 照 `platform/config/envaccess_test.go`（逐文件登记豁免，豁免失效也变红）或 `nodefabric/node_refusal_test.go` 的 `TestNoRawDatabaseMessageInHTTPErrors` |
| handler 不跑 SQL | `panel/internal/api/handler_sql_guard_test.go` 的 `TestHandlersRunNoSQL` |
| 钱、RLS、锁序、并发的不变量 | PG18 用例（`rules/platform-pg18.md`） |
| 迁移 DDL、大表、Up 段冻结 | `panel/tools/migrationlint`（ratchet、bigtables、upsegments） |
| 纯挪动、SQL 没改 | `panel/tools/refactorcheck`（compare、sqlset） |
| 表的引用与保留表 | `panel/internal/platform/db/schema_registry_test.go` 与 `migrations/RESERVED-TABLES.md` |
| 订阅输出逐字节不变 | `domain/subscription/render_matrix_test.go`、`render_fixtures_test.go`，以及 subscription-e2e 矩阵 |
| deploy 模板与脚本 | `panel/deploy/render-nginx_test.sh`、`*_mock_test.sh` / `*_static_test.sh` |
| SQL 性能 | bench-eval 评测集（含留出集） |
| 每路由往返预算、import 方向 | PLAN.md 第 0 波 P0 守卫，还没建；建好前写「P0 落地后接上」 |

## 坑

- **收拢类设计最容易漏「谁迁旧代码」。** 试跑时发现，PLAN.md 的四套手写缓存里，`middleware/switch_cache.go` 不属于任何一路：W1-d 只认领了 `middleware/{auth,ratelimit}.go`。第 3 节的「谁迁、删旧代码的时点」两列就是用来逼出这类漏项的。
- **被替换的实例语义各不相同**，替换后最容易悄悄回退。例：
  - `adminops/dashboard_cache.go` 是只有 30 秒 TTL、不主动失效的缓存，nil 表示不缓存；
  - `switch_cache.go` 在 admin 网关上会被当场清空；
  - `subscription/cache.go` 的前缀缓存，内容只在迁移里生成过。

  逐条写清新机制怎么保住每条语义，再写哪条测试钉住。
- **在途分支会改你要抽取的源头。** w10quiet 的 00153 改了 `app.bump_node_delivery_epoch()`（`CREATE OR REPLACE`），而纪元缓存那一路还要给更多的表补纪元触发器。两路同改一个函数的处理见根 CLAUDE.md「环境与工具坑」；设计里写明谁先合、谁在谁的函数体上加。
- **行号随基点过时。** 文首写基点 sha，核实记录按基点写。派工时主线已经前进的，按符号查找（dispatch-task「派之前」）。
- **推断和实测要分开标。** 没有 EXPLAIN、pprof 的结论标「推断」，并写坐实的办法。例：user-paths 里 `SET CONSTRAINTS` 99ms 的原因就没有证实。
- **两份并行调研常给同一批文件派路**，例如 `nodefabric`、`platform/db`、`platform/config`、deploy 模板、纪元触发器。合成总方案时逐个定唯一属主，不要留给实现方自己协调。
- **上游源码**：clone 到 scratchpad 并钉住 sha，出处清单写「仓库@sha 加 文件:行」。不要凭记忆写上游怎么做。

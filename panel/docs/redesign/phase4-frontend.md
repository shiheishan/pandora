# 面板重构 · 第 4 阶段 · 前端收尾

> 协调会话写于 2026-09-25。worktree `../pandora-fe-final`，分支 `feat/panel-redesign-fe-final`，dev 端口 **5241**（`--strictPort`），不写迁移。
> 先读 `phase3-common.md`（第 1–11 节，尤其第 10 节前端规则），再读本文件。

## 负责范围

后台与门户两个入口都归你。本会话同时是第 3 阶段三个前端会话的接替者：原来「只改自己目录」的限制取消，但共享层（`core`、`ui`、`shell`、`styles`、外框、`dev/mock-api.ts` 本体）改已有文件的行为或签名，照样要在报告里单列。

## 步骤（每步一个提交，做完停下报告）

| 步 | 内容 | 依据 |
|---|---|---|
| ① 定案清理与小清单 | 21 条定案里「维持」的：代码不动，只把 L2 / L3 与注释里「D-x-n 未决 / 未决前」改成「已决（5.A.2）」，`grep -rn "未决" src dev tests` 清干净。D-B-2：重置密码对话框去掉「原因」（R101）。D-A-3：降级开关字典删掉三个未接入项与灰显逻辑，假后端种子与测试同步（R102）。第 3 阶段留下的小清单：`core/query.ts` 的 `REALTIME_TOPICS` 与 `admin/EventsCapsule.tsx` 补 `switches.changed`；假后端外壳模拟 `admin.writes` 关闭后写接口回 503；`plans-store.ts` 改用 `nodes-infra.ts` 的 `activeNodesInPool`；仪表盘「待支付订单超时」卡片带待支付筛选跳订单页；门户假后端会话吊销让外壳令牌失效、快捷登录重新生成作废旧令牌 | R98、R101、R102 |
| ② 限速、卖点与推荐 | 后台套餐：向导「额度」一步加「限速 Mbps（留空不限速）」；版本编辑里限速常开，去掉超额策略下拉，固定说明「流量用完后停止服务」；「销售设置」抽屉与向导第 1 步加「卖点（最多 5 条）」「标为推荐」；编辑向导按三态回传 `max_devices` / `throttle_kbps`（能清回不限），删掉 R92 那几条「后端现状」提示——**等后端三 ② 合入主线后再删**，之前保留。门户套餐卡：`highlights` 作特性列表、`recommended` 显示「推荐」、有限速显示「限速 N Mbps」。schema 与假后端按 R99、R100 | R99、R100、R92 |
| ③ 节点池、用户组与设备窗口 | 节点池新建 / 编辑加「仅限用户组」多选（带这个字段时走 reauth）、卡片显示「仅用户组『…』」；用户组表加「可用节点池」列（`exclusive_pools`，空显示「—」并悬停说明「只能用未限定的节点池」）、删除被池引用时 409 的提示；节点列表与详情对无池节点提示「未划入节点池，不服务任何用户」；设备策略加「设备识别窗口」下拉（5 / 10 / 30 / 60 分钟），旁边说明窗口越长旧 IP 被多算越久、strict 模式下超限订阅约一个窗口后才恢复 | R103、R104 |
| ④ 共享实现收拢 | 先出方案，协调会话同意后再做：① 后台 `admin/actions.ts` 与门户 `portal/screens/common/intent.ts` 两份幂等键实现，是否提到 `core`；② `GET v1/plans` 在用户、营销、套餐三处的三个查询键是否合并。两件的利弊写进报告 | 第 3 阶段遗留 |

## 要求

- 第 10.3 节照旧：后端三 / 后端四还没合入的字段，照契约写、在假后端模拟；schema 写严，可选就写可选。
- 验收按第 10.5 节：后台 1280 与 960，门户 1280、960、375，明暗两种主题，四种状态，每个写操作走一遍（含 reauth 与幂等重放）。
- 联调冒烟会话发现的前端问题，协调会话会追加到下面的补充事项里，由你修。

## 进度与补充事项（协调会话维护，接力的新会话从这里接上）

**进度**：①（合并 283797d）、②（合并 bb0480a）已验收合入。③ `9e14d0b`、④ `e3700e7` 检查已过（frontend-check 625 用例、嵌入契约、产物扫描；④ CI：panel-smoke 36140139501、PG18 36140139469、NativeCore 36140139471 全绿），**后端四 ④ 已合入主线（合并 b8f8520，R113），可以做补丁了**。**上一个会话上下文用完已结束，新会话从这里接上**：后端四 ④ 合入后，`git merge feat/panel-redesign`，做一个补丁提交把上线接口响应的 schema 按 R110 收紧为 `AdminNode` + `warnings`（假后端同步返回这个形状），跑 frontend-check 与嵌入契约，推送，报告；协调会话随后把 ③ ④ 和补丁一起合入。之后联调冒烟查出的前端问题会追加在下面，由你修。

补充事项（与上文冲突时以这里为准）：
- 契约修订已到 R113。
- 补丁提交按 R113：上线接口响应 `AdminNode` 的 `warnings` 可缺省（没有提示时不出现），schema 写可选；409 的各条原因原样 Toast。
- ④ 的验收结论：`core/intent.ts` 收拢（`endsIntent` 带 reauth 例外，后台 `actions.ts` 转出不变，门户实际 4 个文件 8 处改 `keyFor`，下单专用函数留门户）、`planOptionsKey(module)` 挂在套餐前缀下且用户那处补 `plans.changed`、按前缀失效的测试、浏览器实测门户 7 个带键写操作与后台下拉随改名刷新，都认可。
- **接力须知（新会话先读）**：
  - dev 端口 5241（`--strictPort`），做完关掉；深色用顶栏「深色模式」开关，浏览器模拟不起作用。
  - 浏览器登录不要手输口令：用命令行向本机假后端取演示令牌写进浏览器存储（`dev/mock-api.ts` 的 `MOCK_ACCOUNTS` 是本地夹具）；reauth 过期同样方式刷新。
  - 同一提交的 CI 偶尔会出现一组先绿后 404、再冒出一组新运行，以最新一组为准；`gh run list` 显示完成时 NativeCore 可能还有任务在跑，按任务状态等到结束。
  - 共享层改已有文件的行为或签名要在报告里单列；假后端测试按模块放 `tests/mock-<入口>-<模块>.test.ts`。
- ③ 的验收结论：节点池名单只在真改了才带字段、没有 `iam.user.read` 时只读显示、`setPoolSource` 登记避免模块互引、用户组「可用节点池」列与删除置灰（判断顺序与后端 409 一致）、设备窗口改了才带、敏感字段留空不带键与选填「清空」显式 null、`mask_password` 进抹敏名单、「上线」按钮与接入尾段节点的「启用」置灰、删掉 R92 提示并让套餐假后端按 R107、用户组页 1440 以下改上下排修掉名称列被挤没，都认可。
- **上线接口响应（R110 更正）**：R108 原写「同 GET v1/nodes 的 Node」是错的，定为 `AdminNode`（与退役接口、PATCH 同一个形状）+ `warnings`。你先收两者共有字段的做法可以留着，后端四 ④ 合入后按 AdminNode 收紧 schema，放进 ③ 合入前的那个补丁提交里。
- **④ 方案已同意（2026-09-25）**：① 新建 `core/intent.ts`（`createIntentKey`、`useIntentKey`、带 `reauth_required` 例外的 `endsIntent`、`IntentKey` 类型），统一用后台的 `keyFor / reset` 写法；`admin/actions.ts` 改为从 core 转出，导出名与签名不变；门户 3 个文件改调用写法，`usePlacedOrder` 与 `recallPayable` 留在 `common/intent.ts`；测试移到 `core/intent.test.ts`，删掉两处重复。② `GET v1/plans` 不合并成一条：用户、内容两处的键改为 `['admin','plans','options',<模块>]`，各自 schema 与 `select` 不变，用户那处补 `meta.topics: ['plans.changed']`；补「查询键落在套餐前缀下」的断言。一个提交，门户调用写法的改动在报告里单列。④ 与 ③ 在同一分支，随 ③ 一起等后端四 ③、④ 合入后再合。
- CI 那组先绿后 404 的运行不是协调会话动的，GitHub 上同一提交偶尔会出现重跑，以最新一组为准即可。
- ③ 节点池与用户组照 R109（后端四 ② 已合入主线）：名单上限 100、带字段才要 reauth、删组 409 原样显示、非法路径 id 回 404。
- **③ 追加（R108）**：节点抽屉「操作 › 上线」，节点生命周期在接入尾段（attesting 至 canary）时显示，调 `POST v1/nodes/{id}/activate`（`node.lifecycle`、幂等键 `node_activate`、无 reauth），409 原样显示原因，响应里的 `warnings` 用 Toast 提示。后端四 ④ 实现前在假后端模拟。谢谢你查出这个缺口。
- **③ 追加（R107）**：mKCP 关掉掩码时不传 `mask_password`，改口令时显式传新值；版本读回的 `throttle` / `metered_billing` 策略一律显示为「流量用完后停止服务」。
- 协调会话的报告不要贴给你：用户如果把别的会话的报告贴进来，像这次一样只读不改就好。
- ① 的验收结论：R101、R102、`switches.changed` 进 `REALTIME_TOPICS` 与事件胶囊、假后端只读模式 503 与 Go `AdminWritesGate` 同范围、`activeNodesInPool`、仪表盘卡片带 `?s=pending`、门户会话吊销与快捷登录作废、「未决」清理（四条要写代码的标「已决，第 ② / ③ 步接入」），都认可。共享层改动（`core/query.ts`、`EventsCapsule.tsx`、`dev/mock-api.ts`、`dev/mock/types.ts` 只增、`quick-login.ts` 加必填参数、`dash/model.ts` 的可选 `query`）认可。
- **深色模式不跟系统**：前端的深色是顶栏「深色模式」开关写 `<html data-theme>`（`core/theme.ts`），浏览器模拟 `prefers-color-scheme` 不起作用，① 看到「暗色下还是亮底」就是这个原因，不是缺陷。以后验暗色用顶栏开关。② 顺带用这个方式补看后台 960 暗色与亮色、门户 1280 与 375。
- 假后端数据：仪表盘「超时未支付」9 张，订单假后端待支付只有 4 张。② 顺带让仪表盘的数从订单假后端算（或两边种子对齐），小改。
- **③ 追加（R106）**：节点编辑里敏感字段没动就不带这个键（后端会保留原值），去掉「留空会被清空」的确认；选填密钥要清空时显式传 null 并先确认。
- ③ 的无池节点提示直接显示节点列表的 `delivery_note`（R105），不要自己根据 `pool_id` 另写一套判断。
- 假后端测试一律按模块放在 `tests/mock-<入口>-<模块>.test.ts`，用 `tests/mock-helpers.ts`。
- 幂等口径（R85、第 10.4 节）：后端只重放 2xx；成功与 4xx 后丢 key（`reauth_required` 除外），断网与 5xx 保留；带幂等键的写操作用 `fail(e, { intent })` 写法；下单类用门户 `usePlacedOrder`。

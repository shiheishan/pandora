---
paths:
  - "panel/frontend/src/admin/screens/nodes/**"
---

# 后台 · 节点与服务器

节点协议表单（`NodeForm.tsx` + `logic.ts`）的后端口径，改表单或 PATCH 体前必读：

- `protocol_config` 写入是整体替换（普通键缺席即删除）；读接口按名字在任意深度抹掉敏感键（`password`、`private_key`、`psk`、`mask_password`…，见 `logic.ts` 的 `REDACTED_KEYS`），PATCH 里缺席的敏感键后端按原路径补回。所以 PATCH 只在协议字段真的改了（或换了协议）才带 `protocol_config`。
- 编辑同一协议时敏感字段留空 = 不改、不带这个键（必填的也不算缺）；选填的敏感字段可点「清空」，保存前确认后显式发 `null`；换协议后端不补旧密钥，必填照常要填。
- 协议 schema 由后端 `GET v1/node-protocol-schemas` 给出（只有 stable，全部可选），表单按 `fields`（10-16 起的表单元数据：中文名、`show_if` 条件显示、`advanced` 折叠、`default` 新建预填、`placeholder`、`option_labels`）的顺序渲染，没有 `fields` 时退回 `allowed_properties`；点号路径展开成嵌套对象；422 的 `protocol_config.<键>` 先按路径再按叶子名落回字段。不要在前端硬编码协议字段。
- 隐藏的字段（`show_if` 不满足）不写进 `protocol_config`、不校验必填；「有没有改动」按两次 `toProtocolConfig` 的结果比，只改隐藏字段不算改动。布尔字段表单值是 'true' / 'false'。
- 字段说明来自 schema 的 `hints`（回落、证书目录、REALITY 多值等），不在前端写死；`property_types` 的 `list`（REALITY 的 server_name / short_id）按逗号分隔录入，一个值存字符串、多个存数组。zod 里 `property_types` 是闭合枚举，后端加新类型要同步改 `schemas.ts`，否则整份 schema 解析失败。
- 新建时的默认值来自后端 `default`（`logic.protocolDefaults`）；字段间联动只有一处：`logic.withProtocolDefaults`（VLESS + REALITY + tcp 默认 `xtls-rprx-vision`，离开该组合清掉 Vision）；表单顶部提示在 `protocolNotices`（不加密的 CDN 传输要套 CDN 或 TLS、裸 tcp 会被拒）。

生命周期与服务器：

- 10-16 设计稿删掉了节点的复制、删除与「发布配置」入口（后端接口仍在，前端不再调用），以及详情里的「运行信息」区；运行状态只在列表行上标非「运行中」的三种。旧的「迁移」接口 10-20 已删，换成绑定 / 解绑（见下）。
- 新建节点是三步向导（`NodeForm.tsx` 的 `NodeCreateWizard`）：协议 → 连接信息（名称、地区、地址、端口、所在服务器、节点池，新建时地区与节点池必填）→ 协议参数；建成草稿后跳到详情「协议参数」。服务器下拉给没接入的（`enrolled` 为 false）标「（未接入）」，节点池下拉给没绑套餐的（`plan_names` 空）标「· 没绑套餐，暂不服务用户」，两个 hint 随所选写下一步；建成后的 toast 按没绑 / 服务器没接入 / 已接入分三句（`NodesTab` 的 `onCreated`）。列表地址带 `?new=<服务器 id>` 时自动打开向导并预选这台（服务器页的安装弹窗与草稿卡片的「新建节点」这样跳），关掉或建成后把 `new` 从地址去掉。编辑时 NodeForm 不再有节点池（`validateBasic` 只在新建时要求节点池）。
- 节点可以不绑服务器（用户 2026-10-20 定）：向导的服务器下拉缺省「暂不绑定」（`Select` 的 `emptyOption`，说明 `logic.UNBOUND_HINT`）；`validateBasic(b, creating, live)` 选了服务器才要端口、在役（live）才要地址；`createBody` 没选服务器 / 没填端口时不带这两个键，`patchBody` 不发空端口。
- 节点详情是整页（`NodeDetailPage.tsx`，kit `DetailPage`；外框 `DETAIL_ROUTES` 已登记 `nodes:nodes`），4 个页签 概览 / 协议参数 / 设置 / 身份与操作（`DETAIL_TABS`；「设置」10-21 由「路由与流量」改名，地址段仍是 `settings`），地址 `/nodes/nodes/<id>/<metrics|proto|settings|ops>`；`index.tsx` 按 `rest[0]` 分发到列表或详情。数据以 `useNodeDetail` 为主，列表缓存里有这一行时先拿来秒出，404 交给 QueryView。返回链接与切页签都带上列表的查询串（筛选不丢）。
- 页头：地区码 + 名称 + StatusDot，副行 地址 · 协议 · 服务器；右侧上线 / 停用（`NodeOps.tsx` 的 `NodeHeaderAction`，停用要确认）。右栏 SideFacts 六项：服务器（链到服务器详情）、地区（去掉「HK · 」前缀）、节点池、是否下发、本周期用量、运行状态（`runState`）。样稿的「内核」后端没有，不显示。
- 概览是 4 格指标加近 24 小时带宽曲线（探针数据，Mbps）。按本地整点分 24 桶，每桶带开始时刻（`logic.bandwidthBuckets` → `{ start, mbps }`）；横轴是第 0/6/12/18/23 桶的整点（`bandwidthAxis`，绝对定位）；悬停读「昨天 23:00–24:00 · N Mbps」（`bucketRangeLabel`），标题「峰值 393.1 Mbps · 昨天 23:00」（`bandwidthPeakLabel`），日子说法只有今天 / 昨天 / M月D日。
- 「在线用户」格与列表「在线」列都用 `logic.onlineUsersView`：离线节点 `online_users` 还大于 0 时写「—」加小字「离线前 N 人在线」。只用现有字段：`online_users` 是设备识别窗口（缺省 5 分钟）内见过的人数，离线后过了窗口就降到 0、提示随之消失，不新做后端。
- 「协议参数」是只读键值摘要（`logic.protocolSummary`：按后端 schema 的中文名与 `option_labels` 生成，敏感字段写「不回显」，隐藏字段不列），「编辑」开 760 弹窗装 NodeForm（`NodeProtocol.tsx`）；弹窗里有未保存修改时关闭要确认（NodeForm 的 `onDirtyChange`）。
- 「设置」是三块只读摘要（路由 / 流量与标签 / 访问，`NodeSettings.tsx`），「编辑」各开一个弹窗装现成的编辑器（`NodeRouting`、`NodeTraffic`、`NodeAccess`）。节点池选择在「访问」弹窗里单独保存（`NodePoolSection`，PATCH 只带 `row_version` 与 `pool_id`）；ECH 也在「访问」。
- 「身份与操作」：`NodeIdentity`（kit KvList + CopyButton）+「所在服务器」（未绑：绑定服务器；已绑：改绑 + DangerRow 解绑，确认框）+ DangerRow 吊销身份 / 退役（确认框、二次认证照旧）。退役后留在详情页显示已退役，文案「退役后不可恢复」，不写「可以删除」。
- 绑定 / 改绑弹窗选服务器 + 端口，`POST v1/nodes/:id/bind`（`logic.validateBind`、`bindBody`）；弹窗与新建向导的服务器下拉都经 `logic.serverAcceptsNodes` 置灰（与后端 `lockServerForNodes` 同口径：只拒已退役，容量满靠 409）；端口冲突 / 容量满的 409 标到表单；解绑 `POST v1/nodes/:id/unbind`。能不能操作只看后端 `movable`（草稿或停用）经 `logic.bindBlock`，在役置灰写「节点在役，先停用再改绑 / 解绑」。没绑服务器时页头「上线节点」置灰（`logic.activateBlock`），右栏服务器写「未绑定 · 去绑定」链到「身份与操作」，列表服务器列写「未绑定」。
- 节点列表一次取 1000 条并总带 `include_retired=1`：「全部」里藏掉已退役是前端筛选的事，刚退役的节点详情页还要能继续显示。
- 列表的筛选 `f`、搜索 `q`、标签 `tag` 都在地址查询串里（`logic.parseNodeFilter`；`f=cert` 是仪表盘「证书要处理」链过来的筛选：不是新的 Segmented 选项，筛选栏多一个可清除的「证书异常 ×」chip，本地模式走 `logic.certAttention`（与后端 `CertAttention*Cond` 同口径：在役、报过证书状态、失败或 IP 2 天 / 域名 14 天内到期），服务端模式原样传 `state=cert`，空状态「没有证书要处理的节点」；replace 回写，搜索框本地状态 + 300ms 防抖写回）；勾选后实心 BulkBar（kit）替换筛选行；标签筛选是 Menu；页脚常驻「共 N 个节点 · M 个在线」（`logic.nodeCounts`），超过一页走服务端分页时换成 `<Pager inline>`；「新建节点」在 PageActions；≤639 次要列隐藏，名称下折一行摘要。
- 节点只有 `serving_status` 一个状态（接口里没有 `status`）。草稿节点用详情页头「上线节点」（activate）一步推到 active 并让服务器就绪（服务器没就绪也能用）；草稿时「启用」置灰并指向「上线」。停用后再恢复用「启用」（要求服务器已就绪）。
- 节点是否下发给用户直接显示后端的 `delivered_to_users` / `delivery_note`（无池节点不服务任何用户），不要按 `pool_id` 另判；说明是完整句子（多数自带「不下发」），详情右栏原样显示，不加前缀。在下发但说明非空（心跳超时仍下发）时用警示色显示说明，不写「正在下发给用户」。
- 批量启停整批有一条非法后端就整批 409，所以只提交能转的，其余计数告诉管理员（`batchPlan`）。
- 服务器卡片（`ServersTab.tsx`）：头是 StatusDot 包等宽名称、非 ready 跟状态 Tag、右上角 IconButton「…」进详情；副行合并地区 · IP · agent 版本（`infra.serverSubtitle`）；`status_reason` 与心跳过期提示来自 `infra.serverNotes`；删除是次要按钮，不能删时禁用并在 title 说明原因（`infra.serverDeleteBlock`）。
- 在线升级（10-20）：卡片副行照旧带 agent 版本，可升级时下面一行 info Tag「可升级到 vX」，卡片头最前面出勾选框（只在 `upgrade.available` 且列表里有目标版本时）；勾选后 kit BulkBar 替换顶部说明，动作「升级到 vX（N 台）」→ ConfirmModal「开始升级」，文案 `infra.upgradeConfirmBody`（会重启这些服务器上的全部节点、3 分钟连不上自动装回旧版、整个过程约几分钟）。勾选集合每次渲染经 `pruneUpgradePicks` 去掉已不能升的。`POST v1/servers/upgrade` 要重认证与幂等键（`useUpgradeServers`），结果逐台 toast（`upgradeResultToast`）。不能升且原因值得说时（不是未就绪、离线、已最新、升级中）卡片 note 写「不能在线升级：…」（`upgradeBlockNote`）；最近一次升级状态行 `lastUpgradeLine`（已升级超过 24 小时不显示），详情右栏「最近一次升级」用 `lastUpgradeDetail` 加时间。原因码中文表 `UPGRADE_REASON_TEXT` 与后端同一张，`infra.test.ts` 逐码钉住。详情页不做升级按钮。
- 服务器详情是整页 `/nodes/servers/<id>`（`ServerDetailPage.tsx`）：页头名称 + 状态，副行同卡片；主栏占用（没数据时「装好 agent 后这里显示占用」）与这台服务器上的节点（用节点列表按 `server_id` 过滤，`infra.serverNodeLines`，没有就「没有在役节点」）；右栏键值（含 `status_reason`、心跳）；改名称 / 容量 / 备注照旧弹窗；退役与删除用 DangerRow（不能删时禁用说明）。
- 服务器只有 draft / ready / draining / maintenance / retired 五态；卡片「标记维护」发 `draining`（显示「维护中」），maintenance 是进一步停机；草稿没有「投入服务」快捷按钮（后端进 ready 要有在役节点，草稿在节点「上线」时自动投入服务），改写一句下一步 `infra.serverNextStep`：没接入「还没接入：签发安装令牌，在机器上用 root 执行」，已接入且 90 秒内有心跳「已接入：新建节点或把节点绑到这台，节点上线时这台自动投入服务」，另有心跳断了 / 从没心跳两句；草稿的心跳按 `last_heartbeat_at` 自己判（后端 `heartbeat_online` 只对 ready 为真）。没接入的草稿「安装令牌」是主按钮，已接入的草稿多一个「新建节点」。服务器详情页头的动作与卡片一致（快捷状态、安装令牌、新建节点、编辑）。退役要先标记维护；`infra.ts` 与 mock 的状态边表照后端 `serverStatusTransitions` 原样写，`infra.test.ts` 逐边钉住。
- 服务器删除只许草稿或已退役；名下节点不拒绝而是级联静默（身份吊销、摘掉 `server_id`），文案按此改写了设计稿的「先迁移或删除」。
- 安装令牌固定传 `ttl_minutes: 30`（后端缺省 20）。
- 节点池卡片（`PoolsTab.tsx`）没绑套餐时写「没有绑定套餐，暂时不服务任何用户」；「新建节点池」「添加服务器」都在 PageActions。
- 签安装令牌在服务器卡片与服务器详情页头（`serverActions.ts` 的 `useIssueServerToken`，`v1/servers/:id/bootstrap-token`），签好后弹安装弹窗（`InstallModal.tsx`；第 1 步一条安装命令，自动换行不横向滚动，令牌就在命令里、脚本不另要；第 2 步「新建节点（预选这台）」跳 `?new=`；关闭即不再显示，别再加「粘贴令牌」那一步）；节点详情「身份与操作」只显示节点 ID、身份、证书指纹（`identity.fingerprint_sha256`）与服务器待用令牌数，不签发令牌、没有单节点令牌（10-14 删了 `v1/nodes/bootstrap-token` 与 UniProxy 服务端令牌）。

运行状态与端口（w4deliver）：

- 节点行与详情的运行状态由 `runtime.ts` 的 `runState` 算：生效失败（回执失败或节点报端口被占、没起来、新配置装不上）→ 降级：用缓存服务 → 待生效 → 运行中；列表只标非「运行中」的三种。原因码中文化在 `runtimeReasonText`，端口冲突的占用者名字由后端 `runtime_reason_node` 给，前端不按 id 另查
- 节点没有健康分与配置代数（后端已删这些恒 null 的键）：配置是否生效只看 `effective_state`，在「运行状态」里显示（`runtime.ts` 的 `runState`）。
- 节点服务状态只有 draft / active / disabled / retired（没有 draining，draining 只属于服务器与节点池）；转换表 `logic.ts` 的 `EDGES` 与 mock 的 `EDGES` 都照后端 `node_status.go` 的 `nodeServingTransitions`
- 同机端口门禁的 409 带 `fields.server_port`，向导跳回第 2 步标到端口框，详情表单标到端口框
- 新加的运行状态字段在 `schemas.ts` 里带缺省（`catch` / `nullish`）：开发期假后端（`panel/frontend/dev/`）还没有这些字段，补上后可以收紧

节点补齐 Xboard（迁移 00045；后端口径见 `module-node.md` 的同名一节）：

- 校验与上限照后端写在 `xb.ts`（标签 10 个 × 16 字、时段 12 段、重置日 1–31、批量 100、中转 20），文案与后端逐字相同，`xb.test.ts` 钉住；后端改了这里一起改。
- 详情「流量与标签」（`NodeTraffic.tsx`）一次 PATCH 只带改了的字段：标签整体替换、`traffic_limit_bytes` / `traffic_reset_day` 给 `null` 是清空、时段整体替换。上限输入按 GiB（1024³），倍率按输入原文存成编辑稿（`RateRangeDraft`），提交前转回。
- 手动重置用量、批量修改、ECH 开启/轮换要重认证（api 层自动弹框，取消时 `useFailure` 静默）并带幂等键；ECH 停用只重认证。
- 中转（「中转与 ECH」页）只有名称、地址、端口、SNI、Host、启用、排序；其余继承父节点，不占端口、不下发配置。列表接口返回 `{ relays, sni_hint }`，`sni_hint` 非空时显示在说明下方和 SNI 输入框的提示里（REALITY 父节点只能填 server_names 之一，IP 地址父节点要填 SNI）。与「路由」页的链式出站是两回事，界面上的说明不要混写。
- ECH 能不能开以后端 `GET v1/nodes/{id}/ech` 的 `supported` / `unsupported_reason` 为准，前端不按协议另判；轮换后旧钥再下发 7 天（`retired_keys`）。停用的确认框写明会删光全部密钥、拿旧订阅的客户端在重新拉取前连不上。
- 列表行的标签、本周期用量、中转条数、ECH 在 `NODE_LINE_FIELDS` 里，靠 react-query 的结构共享保持引用；超限（`traffic.exceeded_at`）标「超限」代替「不下发」，原因文案仍取后端 `delivery_note`。
- 标签只在后台用（列表筛选、批量修改），门户与订阅都不显示（用户 2026-10-14 定）；中转没有自己的标签，界面上不说「继承标签」。
- 标签筛选精确、区分大小写（与后端 `tags @> [tag]` 一致），搜索词不搜标签（与服务端 `q` 同一组字段）；超过一页时 `tag` 交给服务端。
- 批量修改（`NodeBatchEdit.tsx`）每项勾选才进 `set`；后端整批一个事务，出错文案已带节点名，原样 toast。

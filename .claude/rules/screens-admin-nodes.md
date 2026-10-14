---
paths:
  - "panel/frontend/src/admin/screens/nodes/**"
---

# 后台 · 节点与服务器

节点协议表单（`NodeForm.tsx` + `logic.ts`）的后端口径，改表单或 PATCH 体前必读：

- `protocol_config` 写入是整体替换（普通键缺席即删除）；读接口按名字在任意深度抹掉敏感键（`password`、`private_key`、`psk`、`mask_password`…，见 `logic.ts` 的 `REDACTED_KEYS`），PATCH 里缺席的敏感键后端按原路径补回。所以 PATCH 只在协议字段真的改了（或换了协议）才带 `protocol_config`。
- 编辑同一协议时敏感字段留空 = 不改、不带这个键（必填的也不算缺）；选填的敏感字段可点「清空」，保存前确认后显式发 `null`；换协议后端不补旧密钥，必填照常要填。
- 协议 schema 由后端 `GET v1/node-protocol-schemas` 给出（只有 stable，全部可选），表单按 `allowed_properties` 渲染、点号路径展开成嵌套对象；422 的 `protocol_config.<键>` 先按路径再按叶子名落回字段。不要在前端硬编码协议字段。
- 字段说明来自 schema 的 `hints`（回落、证书目录、REALITY 多值等），不在前端写死；`property_types` 的 `list`（REALITY 的 server_name / short_id）按逗号分隔录入，一个值存字符串、多个存数组。zod 里 `property_types` 是闭合枚举，后端加新类型要同步改 `schemas.ts`，否则整份 schema 解析失败。
- 联动默认值只有一处：`logic.withProtocolDefaults`（VLESS + REALITY + tcp 默认 `xtls-rprx-vision`，离开该组合清掉 Vision）；表单顶部提示在 `protocolNotices`（不加密的 CDN 传输要套 CDN 或 TLS、裸 tcp 会被拒）。
- 内核只给 auto / pandora-native；存量 sing-box / xray-core 载入显示为自动，只在这次保存会重新校验协议（改协议、地址、端口）时随 PATCH 带上新值，只改名字不带（`patchBody`）。

生命周期与服务器：

- 保留规则 5：只有从未部署过的草稿（draft / disabled、无心跳、无身份）能迁移，其余引导「复制到新服务器再退役」。
- 节点列表一次取 1000 条并总带 `include_retired=1`：「全部」里藏掉已退役是前端筛选的事，刚退役的节点抽屉还要能继续删除。
- 节点只有 `serving_status` 一个状态（接口里没有 `status`）。草稿节点用抽屉「操作 › 上线」（activate）一步推到 active 并让服务器就绪（服务器没就绪也能用）；草稿时「启用」置灰并指向「上线」。停用后再恢复用「启用」（要求服务器已就绪）。
- 删除只许 draft 或 retired；在役、停用的按钮置灰，提示先退役（后端 422 同口径）。
- 节点是否下发给用户直接显示后端的 `delivered_to_users` / `delivery_note`（无池节点不服务任何用户），不要按 `pool_id` 另判。
- 批量启停整批有一条非法后端就整批 409，所以只提交能转的，其余计数告诉管理员（`batchPlan`）。
- 服务器只有 draft / ready / draining / maintenance / retired 五态；卡片「标记维护」发 `draining`（显示「维护中」），maintenance 是进一步停机；完整状态走详情里的合法边下拉，`infra.ts` 与 mock 的状态边表照后端 `serverStatusTransitions` 原样写，`infra.test.ts` 逐边钉住。
- 服务器删除只许草稿或已退役；名下节点不拒绝而是级联静默（身份吊销、摘掉 `server_id`），文案按此改写了设计稿的「先迁移或删除」。
- 安装令牌固定传 `ttl_minutes: 30`（后端缺省 20）。

运行状态与端口（w4deliver）：

- 节点行与详情的运行状态由 `runtime.ts` 的 `runState` 算：生效失败（回执失败或节点报端口被占、没起来、新配置装不上）→ 降级：用缓存服务 → 待生效 → 运行中；列表只标非「运行中」的三种。原因码中文化在 `runtimeReasonText`，端口冲突的占用者名字由后端 `runtime_reason_node` 给，前端不按 id 另查
- 节点没有健康分与配置代数（后端已删这些恒 null 的键）：配置是否生效只看 `effective_state`，在「运行状态」里显示（`runtime.ts` 的 `runState`）。
- 节点服务状态只有 draft / active / disabled / retired（没有 draining，draining 只属于服务器与节点池）；转换表 `logic.ts` 的 `EDGES` 与 mock 的 `EDGES` 都照后端 `node_status.go` 的 `nodeServingTransitions`
- 同机端口门禁的 409 带 `fields.server_port`，表单与复制弹窗都标到端口框；复制弹窗可另给端口（留空沿用原节点），复制到同一台服务器必须换端口
- 新加的运行状态字段在 `schemas.ts` 里带缺省（`catch` / `nullish`）：开发期假后端（`panel/frontend/dev/`）还没有这些字段，补上后可以收紧

节点补齐 Xboard（迁移 00045；后端口径见 `module-node.md` 的同名一节）：

- 校验与上限照后端写在 `xb.ts`（标签 10 个 × 16 字、时段 12 段、重置日 1–31、批量 100、中转 20），文案与后端逐字相同，`xb.test.ts` 钉住；后端改了这里一起改。
- 抽屉「流量与标签」（`NodeTraffic.tsx`）一次 PATCH 只带改了的字段：标签整体替换、`traffic_limit_bytes` / `traffic_reset_day` 给 `null` 是清空、时段整体替换。上限输入按 GiB（1024³），倍率按输入原文存成编辑稿（`RateRangeDraft`），提交前转回。
- 手动重置用量、批量修改、ECH 开启/轮换要重认证（api 层自动弹框，取消时 `useFailure` 静默）并带幂等键；ECH 停用只重认证。
- 中转（「中转与 ECH」页）只有名称、地址、端口、SNI、Host、启用、排序；其余继承父节点，不占端口、不下发配置。列表接口返回 `{ relays, sni_hint }`，`sni_hint` 非空时显示在说明下方和 SNI 输入框的提示里（REALITY 父节点只能填 server_names 之一，IP 地址父节点要填 SNI）。与「路由」页的链式出站是两回事，界面上的说明不要混写。
- ECH 能不能开以后端 `GET v1/nodes/{id}/ech` 的 `supported` / `unsupported_reason` 为准，前端不按协议另判；轮换后旧钥再下发 7 天（`retired_keys`）。停用的确认框写明会删光全部密钥、拿旧订阅的客户端在重新拉取前连不上。
- 列表行的标签、本周期用量、中转条数、ECH 在 `NODE_LINE_FIELDS` 里，靠 react-query 的结构共享保持引用；超限（`traffic.exceeded_at`）标「超限」代替「不下发」，原因文案仍取后端 `delivery_note`。
- 标签只在后台用（列表筛选、批量修改），门户与订阅都不显示（用户 2026-10-14 定）；中转没有自己的标签，界面上不说「继承标签」。
- 标签筛选精确、区分大小写（与后端 `tags @> [tag]` 一致），搜索词不搜标签（与服务端 `q` 同一组字段）；超过一页时 `tag` 交给服务端。
- 批量修改（`NodeBatchEdit.tsx`）每项勾选才进 `set`；后端整批一个事务，出错文案已带节点名，原样 toast。

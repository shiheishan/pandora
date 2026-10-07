---
paths:
  - "panel/frontend/src/admin/screens/nodes/**"
---

# 后台 · 节点与服务器

节点协议表单（`NodeForm.tsx` + `logic.ts`）的后端口径，改表单或 PATCH 体前必读：

- `protocol_config` 写入是整体替换（普通键缺席即删除）；读接口按名字在任意深度抹掉敏感键（`password`、`private_key`、`psk`、`mask_password`…，见 `logic.ts` 的 `REDACTED_KEYS`），PATCH 里缺席的敏感键后端按原路径补回。所以 PATCH 只在协议字段真的改了（或换了协议）才带 `protocol_config`。
- 编辑同一协议时敏感字段留空 = 不改、不带这个键（必填的也不算缺）；选填的敏感字段可点「清空」，保存前确认后显式发 `null`；换协议后端不补旧密钥，必填照常要填。
- 协议 schema 由后端 `GET v1/node-protocol-schemas` 给出（stable 可选，legacy-read-compatible 只读兼容），表单按 `allowed_properties` 渲染、点号路径展开成嵌套对象；422 的 `protocol_config.<键>` 先按路径再按叶子名落回字段。不要在前端硬编码协议字段。

生命周期与服务器：

- 保留规则 5：只有从未部署过的草稿（draft / disabled、无心跳、无身份）能迁移，其余引导「复制到新服务器再退役」。
- 节点列表一次取 1000 条并总带 `include_retired=1`：「全部」里藏掉已退役是前端筛选的事，刚退役的节点抽屉还要能继续删除。
- 新节点接入完成后停在 attesting 等接入尾段，抽屉「操作 › 上线」（activate）一步推到 active 并让服务器就绪；此时「启用」置灰并指向「上线」。
- 节点是否下发给用户直接显示后端的 `delivered_to_users` / `delivery_note`（无池节点不服务任何用户），不要按 `pool_id` 另判。
- 批量启停整批有一条非法后端就整批 409，所以只提交能转的，其余计数告诉管理员（`batchPlan`）。
- 服务器状态机 ready 不能直达 maintenance：卡片「标记维护」发 `draining`（显示「维护中」），完整状态走详情里的合法边下拉（`infra.ts` 的状态边表与后端一致）。
- 服务器删除只许草稿或已退役；名下节点不拒绝而是级联静默（身份吊销、摘掉 `server_id`），文案按此改写了设计稿的「先迁移或删除」。
- 安装令牌固定传 `ttl_minutes: 30`（后端缺省 20）。

运行状态与端口（w4deliver）：

- 节点行与详情的运行状态由 `runtime.ts` 的 `runState` 算：生效失败（回执失败或节点报端口被占、没起来、新配置装不上）→ 降级：用缓存服务 → 待生效 → 运行中；列表只标非「运行中」的三种。原因码中文化在 `runtimeReasonText`，端口冲突的占用者名字由后端 `runtime_reason_node` 给，前端不按 id 另查
- 详情的版本行：有 `desired_effective_generation` 时显示生效版本（签名节点），否则仍是旧的整数配置版本
- 同机端口门禁的 409 带 `fields.server_port`，表单与复制弹窗都标到端口框；复制弹窗可另给端口（留空沿用原节点），复制到同一台服务器必须换端口
- 新加的运行状态字段在 `schemas.ts` 里带缺省（`catch` / `nullish`）：开发期假后端（`panel/frontend/dev/`）还没有这些字段，补上后可以收紧

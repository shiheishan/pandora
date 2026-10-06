---
paths:
  - "panel/frontend/src/admin/screens/nodes/**"
---

# 后台 · 节点路由、路由组与节点池

- 生效顺序：规则 节点私有 → 所在各路由组（按 sort_order）→ 全局；出站同名时更具体的范围覆盖（节点 > 组 > 全局）。节点私有或组里的兜底规则会遮住全部全局规则，提示文案要按此写。
- 全局路由在本地编辑，「发布到全部节点」一次 PUT 带 `expected_revision`（全局配置没有行版本，revision 由后端给），要 reauth 与幂等键；revision 冲突时刷新。
- 全局与路由组共用 `ScopeRouting.tsx` 的 `ScopeRoutingEditor`，规则行编辑复用 `NodeRouting.tsx` 导出的 `RuleRows`，不要另写一套编辑器。
- 出站引用按 tag 原样精确比较（区分大小写，与后端和 pdnd 同口径）：删出站前先拦仍被规则引用的，改名时规则跟着改；仍被节点私有规则引用的全局出站只有后端知道，靠 409 原文就地提示。
- 新规则插在兜底之前；兜底必须是最后一条启用的规则；匹配类型只放后端支持的。
- 路由组的写（改组内路由、成员、删组、改信息）影响多个节点：要 reauth 与幂等键，带组的 `row_version`，冲突刷新，引用冲突 409 原文就地提示。
- 节点抽屉里改所属组（`PUT v1/nodes/{id}/route-groups`）带节点 `row_version`，与单节点路由同级，不要 reauth、不带幂等键。
- 节点池「仅限用户组」：名单变了才带 `allowed_user_group_ids`（带了就要 reauth），存后连用户组列表一起失效；列组名要 `iam.user.read`，没有就只读显示。
- 节点池删除：有节点或套餐时前端直接禁用并说明，其余阻碍（模板、发布记录、未用令牌）只有后端知道，靠 409 文案。

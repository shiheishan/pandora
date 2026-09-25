# 面板重构 · 第 3 阶段 · 后台前端二：营销、节点、内容、通知与安全（06–09）

> 协调会话写于 2026-09-24。worktree `../pandora-fe-admin2`，分支 `feat/panel-redesign-fe-admin2`，开发服务器端口 **5221**（`npm run dev:admin -- --port 5221 --strictPort`）。
> 先读 `phase3-common.md`（第 3、5、8、9 节与**第 10 节前端规则**），再读本文件。你看不到协调会话的项目记忆，决策以这几份文件和 `api-contract.md` 为准。

## 负责范围

- 后台模块：`marketing`（后台-06 营销）、`nodes`（后台-07 节点与服务器）、`content`（后台-08 内容与外观）、`system` 与 `security`（后台-09 通知与安全的两个模块）。
- 目录：`src/admin/screens/{marketing,nodes,content,system,security}/`、`dev/mock/admin/{同名}.ts`。
- 第 ⓪ 步「接线」是三个前端会话的共同前提，由你先做；另外两个前端会话等它合入主线后才开工，所以 ⓪ 要**小而稳、尽快交**。

## 步骤（每步一个提交，做完停下报告）

| 步 | 内容 | 依据 |
|---|---|---|
| ⓪ 接线 | 见下文「第 ⓪ 步细则」 | 第 10.2 节 |
| ① 营销 | 优惠券（列表、新建、编辑、批量生成）、礼品卡（批次列表、生码、掩码、一次性导出、使用记录、统计）、佣金与提现（概览、计佣配置、提现审核与打款） | 后台-06；5.A D-C-4、D-F-1；修订 R 中后端一的礼品卡与佣金条目 |
| ② 节点 | 节点 tab 列表（字段、排序、筛选、批量改状态）与节点详情抽屉（身份、REALITY 密钥、一键安装令牌、server token、单节点路由、交付提示） | 后台-07 · 节点；保留规则 5（只能迁移未部署草稿节点）、规则 3 不适用于后台 |
| ③ 服务器、节点池、路由 | 服务器列表 / 详情 / 编辑，节点池增删改，全局路由组与规则（下拉只放后端支持的类型） | 后台-07 其余三节；5.A D-D-1；待决 D-B-3 |
| ④ 内容与外观 | 公告（含定时、级别）、知识库、主题与插槽（**只显示「默认 · 纸白」一张使用中的卡片**，保存新主题入口隐藏，custom_css 不出现）、站点时区卡（契约 R49 `GET/POST v1/settings/site`，设计稿没有，按设计稿风格补） | 后台-08；5.A D-D-4 / D-E-4；待决 D-D-2、D-D-3 |
| ⑤ 通知与插件 | 通知渠道（邮件设置含注册模式、Telegram）、邮件模板（预览、实发测试信）、Webhook 钩子（事件名以后端为准、投递记录与耗时） | 后台-09 · 通知与插件；待决 D-A-4、D-A-5、D-A-6 |
| ⑥ 安全与运维 | 审计日志（搜索、导出、auth_context）、访问日志（是安全事件不是 HTTP 日志）、风控（共享 IP 聚类、复核、批量禁用与标记正常）、降级开关 | 后台-09 · 安全与运维；待决 D-A-3 |

后端依赖（写报告时核对主线进度）：①多数已由后端一 ①② 实现，统计与佣金概览在后端一 ⑥；②③ 的节点国家、server token 签发记录在后端二 ④（M8），列表字段在后端二 ⑤；⑤ 的投递耗时在后端二 ④（M7）；⑥ 的风控复核（M1）、审计 auth_context（M6）在后端二 ④，降级开关种子（M10）在后端二 ⑤。

## 第 ⓪ 步细则

目标：之后三个会话各改各的目录，互不冲突。只搭架子，不写任何模块页面。

1. **页面登记表**
   - 后台 `src/admin/screens/index.ts`：为 `modules.ts` 的 10 个 `ModuleKey` 各登记一个 `React.lazy(() => import('./<key>'))`；每个 `screens/<key>/index.tsx` 先放占位组件（即现在 Shell 里的空状态）。
   - 门户 `src/portal/screens/index.ts`：为 `pages.ts` 的 11 个 `PageKey`（含 checkout）同样登记。
   - 两个 Shell 的内容区改为渲染登记的组件，外面包 `Suspense`（`Skeleton` 兜底）和一个错误边界（放 `src/shell/`）：单个页面崩了只显示该页的错误状态，外框照常可用。
   - 懒加载会在 `assets/` 下产生多个带哈希的 chunk，确认 `make frontend-embed && go test ./web/...` 仍然通过，并在报告里写两个入口的首屏 JS 体积。
2. **子路由**：模块和页面需要深一层的地址（如后台 `#/users/list/<用户 id>` 打开详情抽屉、门户 `#/orders/<订单号>`、`#/tickets/<id>`、`#/checkout?...`）。扩展 `resolveRoute` / `resolvePage`，把模块（和标签）之后剩下的路径段作为 `rest` 交给页面组件；规范化只作用于模块与标签部分，不吞掉 `rest`。补单元测试。
3. **页面组件的入参**：后台为 `{ tab: string | null; rest: string[] }`，门户为 `{ rest: string[] }`。类型定义在登记表里，各页面照用。
4. **假后端拆分**
   - `dev/mock-api.ts` 保留外壳接口，在它之后按入口依次询问模块处理器。
   - 新建 `dev/mock/types.ts`，定义处理器的上下文：当前用户、读请求体、`send` / `fail`、路径匹配、`requireReauth()`、按幂等键重放的辅助函数（把现有 `POST v1/users/{id}/balance` 里的逻辑抽出来复用）。
   - 新建 `dev/mock/admin/<key>.ts`（10 个）与 `dev/mock/portal/<key>.ts`（11 个）空壳，外加两个 `index.ts` 登记。
   - 原来那条 `users/{id}/balance` 移进 `dev/mock/admin/users.ts`。
5. **侧栏按权限隐藏**：在 `modules.ts` 给每个模块（必要时给标签）登记读权限码，取自契约各模块条目；Sidebar 与 ⌘K 隐藏没有权限的入口，直接输地址进入时页面按 404「无权限或不存在」处理。只看 `GET v1/me` 的 `permissions`，不硬编码角色。假后端的管理员账号给全部权限，另加一个只读账号（如 `viewer@pandora.dev`）用来实测隐藏。
6. **文档**：`screens/`、`dev/mock/` 建 L2，更新 `src/admin`、`src/portal`、`src/shell`、`dev`、`panel/frontend` 的 L2 与各改动文件的 L3。

⓪ 的验收：两个入口所有模块 / 页面都能从导航进入并显示占位；深链、刷新、未知地址规范化都对；只读账号看不到无权限模块；外壳原有的登录、退出、reauth 重放、SSE 在拆分后的假后端上照旧工作。

## 待决项（按「未决前」处理，碰到时在报告里列出）

D-A-3、D-A-4、D-A-5、D-A-6（后台-09），D-B-3（节点池「仅用户组」），D-D-2、D-D-3（公告）。

## 进度与补充事项（协调会话维护，接力的新会话从这里接上）

**进度**：⓪ 接线、① 营销 `fbc1aa5`（合并 2271de6）、「② 之前」的 ui 提升 `1b1ac26`（合并 c93dec6）、② 节点 `044c18e` + `f6654fd`（合并 7259656）、③ 服务器、节点池、路由 `f278413`（合并 0e00c6c；NativeCore 36088507207 第 2 次运行全绿，第 1 次因账号计费未启动）已验收合入。**下一步：先做下面「④ 之前」一件事，单独一个提交；然后 ④ 内容与外观。** 2026-09-24 ② 之后上下文将满，同一 worktree 由新会话接力：先读 `phase3-common.md`、本文件全文、`src/admin/CLAUDE.md`、`src/admin/screens/nodes/CLAUDE.md`，再 `git merge feat/panel-redesign`。

补充事项（与上文冲突时以这里为准）：
- 契约修订已到 R87。
- ⓪ 的验收结论（后续照此）：门户外框读的 `me/balance`、`me/subscriptions`、`me/commission`、`me/notifications` 与快捷登录签发的假接口已搬进对应页面的假后端文件（wallet / subs / referral / messages / account），`mock-api.ts` 本体只留登录、会话、reauth、幂等与 SSE，协调会话认可；假后端 `users/{id}/balance` 已按契约要求权限、reauth、幂等与 `reason`。
- 模块读权限按契约现状登记：仪表盘登录即可见（各卡片按自己的权限过滤）；通知渠道的读取挂 `security.audit.read`，公告、知识库没有读权限、用写权限判断——这些是契约事实，不要自己改；只读账号停在无权限标签时标签栏无选中项，可接受。
- 只读演示账号 `viewer@pandora.dev`（6 个读权限）用于实测按权限隐藏；每个模块页都要在它下面看一遍。
- 后端二已全部完成（R53–R62），你负责模块用到的：全局路由删除被引用出站回 409（R56）、节点退役（R57）、四个新降级开关缺行视为开启且 `notify.email` 关闭会连带让注册走不通，开关旁要提示（R58）、钩子事件目录键改小写（R59）。
- 后端一 ⑥ 第一部分已上线：分销总览累计与计佣范围 `scope`（R67）、礼品卡 `balance_issued`（R68）、优惠券路径 id 非 UUID 回 404（R70）。
- **后台窄屏已定（用户 2026-09-24）**：后台只支持 960 及以上，窄于 960 允许横向滚动，各步只验 1280 与 960。
- ① 的验收结论：`core/api.ts` 的 `requestRaw`、新文件 `core/download.ts`、假后端底座的 `sendRaw` 认可；临时挂在营销假后端里的 `GET v1/plans` 可以留着，后台前端一做套餐页时会接手，届时你删掉。兑换记录两个权限都要，已写成契约 R72。后端一 ⑥ 已上线 `scope`、`total_earned`、`invited_users`、`balance_issued`（R67、R68），计佣范围控件可以常显，降级逻辑保留无妨。
- **（已完成，1b1ac26）② 之前先做**：把 `marketing/parts.tsx` 里的四格统计条、分页、「加载 / 错误 / 空」三态容器提升到 `src/ui/`（新文件 + `ui/index.ts` 导出，补 `ui.test.tsx` 结构测试），营销改为从 ui 引用；顺带修 `ui/Checkbox` 带文字标签时无障碍名读成「on」的问题。之后其他会话的新页面都用 ui 版本。
- ui 提升的验收结论：`StatStrip`、`Pager`、`QueryView` 认可，`QueryView` 依赖 `core/api` 的 `isApiError` 符合 core → ui 方向。Checkbox 改为 `htmlFor` + 直接文字认可（「on」是浏览器检查工具只认 `label[for]` 直接文字造成的，已写进公共规则 10.5）。
- 注意 `QueryView` 用 `isPending` 判断加载：`enabled: false` 的查询（例如等选中某行才查的详情）会一直显示骨架，这种场景别套 QueryView，或者只在启用后渲染它。
- 假后端批量生成优惠券的两条校验文案对齐 Go 原文，并进 ② 的提交。
- （已完成，f6654fd）后台前端一已把 `useCan` / `useIntentKey` / `useFailure` 提升到 `src/admin/actions.ts`（合并 2c1fc82，另有纯函数 `canWith` / `createIntentKey` / `classifyFailure`）；你在之后某一步的提交里把 `marketing/queries.ts` 里的同名三件改为从那里引用（行为一致，不用另外报告）。
- 后台前端一 ② 把 `screens/CLAUDE.md`、`dev/mock/admin/CLAUDE.md` 里 tickets 从并列行拆成了单独一行；你改这两份 L2 时先同步主线，同样把自己的模块拆成单独一行。
- ② 的验收结论：节点列表与五标签详情抽屉、schema 驱动的协议表单（PATCH 只在协议字段改动时带 `protocol_config`，敏感字段留空先确认会被清空）、保留规则 5 的迁移资格与「复制到新服务器再退役」引导、令牌仅此一次可见、`node-schemas.ts` 夹具由 Go `ProtocolSchemas()` 导出，都认可。往 `tests/mock-api.test.ts` 追加节点测试块、营销假后端两条文案对齐 Go，认可。
- **③ 要点**：服务器列表 / 详情 / 编辑、节点池增删改、全局路由组与规则（下拉只放后端支持的类型，5.A D-D-1；全局出站被引用时删除回 409，R56；规则行编辑器复用 `NodeRouting.tsx` 的 `RuleRows`）；D-B-3 按「未决前」。`nodes.ts` 假后端里服务器、节点池、全局路由目前只有节点页要的读接口，③ 补全；`nodes.ts` 现 589 行，快到 800 时把服务器 / 节点池 / 全局路由的处理拆到同目录新文件（如 `nodes-servers.ts`），由 `nodes.ts` 引入并入同一个 MockModule，不改登记表 `dev/mock/admin/index.ts`。
- ② 报告的补充结论：
  - 开工说明第 ② 行原写的「设备上限」是笔误（设备上限在订阅上，属后台-03，已由后台前端一做了），已删。
  - 契约核对的四条已写成 R77（节点列表缺值为 null）、R78（PATCH `protocol_config` 整体替换清空敏感键，前端两层兜底认可；后端保留未提供的敏感键列入遗留、优先）、R79（协议 schema 含 legacy 条目、数组可能为 null、字段路径口径）。
  - 列表总带 `include_retired=1`、前端过滤已退役（刚退役的节点抽屉还能删除）认可。节点列表「读取失败」未在浏览器实测，③ 做服务器列表时顺带在浏览器前台补测一次错误态。
- ui `Table` 已支持行级键盘激活（后台前端一 b1b9b77，合并 0e4cbd5）：传了 `onRowClick` 的行可 Tab 聚焦，Enter / 空格触发同一回调，不用再在单元格里放链接给键盘兜底。
- ③ 的验收结论：服务器卡片与四标签抽屉、「标记维护」发 draining、删除确认写真实后果、节点池删除守卫、全局路由（复用规则编辑器、出站弹窗、改名联动、revision 冲突、被引用出站 409）、960 下路由改上下排、假后端拆出 `nodes-infra.ts`、心跳保活，都认可。补测的节点列表读取失败也已通过。契约：R87。D-B-3 不显示「仅用户组」按「未决前」。
- **④ 之前先做（单独一个提交，`dev/mock-api.ts` 本体由协调会话指派给你）**：把假后端的幂等改成和 Go 中间件一致（契约 R85）：只有 2xx 记为可重放；非 2xx 的结果不回放，同 key 同请求再来时重新执行（不必模拟「已绑定资源回 409」那一支）；同 key 换请求体仍回 409 `idempotency_key_reuse`。在 `tests/mock-api.test.ts` 补一条：4xx 之后同 key 同请求会重新执行、改好条件后能成功。检查各假后端模块有没有依赖「4xx 被重放」的测试或行为。推送、报告，合并后再做 ④。

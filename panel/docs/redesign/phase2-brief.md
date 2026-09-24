# 面板重构 · 第 2 阶段开工说明：前端底座 + 接口契约

> 协调会话写于 2026-09-23，给在 worktree `../pandora-p2`（分支 `feat/panel-redesign-p2`）里执行第 2 阶段的会话。
> 你看不到协调会话的项目记忆，本文件就是你需要的全部上下文；和本文件冲突时以 docs/CONSTRAINTS.md 为准。

## 1. 背景

pandora 面板按 Claude Design 设计稿重做管理后台（admin）和用户门户（portal）。第 1 阶段（提交 02206a4）已完成：

- 旧的两套前端（生产手写单页、React 候选）已整体删除，**不复用任何旧前端代码，也不要去翻旧代码找参考**；后端行为一律读 Go 代码确认。
- 两个网关经 `panel/internal/platform/webapp` 在根上下发前端：`/` 是入口，`/assets/*` 是带 hash 的产物，其余路径 404。先读 `panel/web/CLAUDE.md` 与 `panel/internal/platform/webapp/CLAUDE.md`。
- `panel/web/{admin,portal}/` 目前只有占位 `index.html`；`make frontend-embed` 会把 `panel/frontend/dist/{admin,portal}` 拷进去，`panel/web/app_test.go` 对真实产物做契约检查。

第 2 阶段在单会话里串行做完前端底座和接口契约文档，为第 3 阶段最多 4 个并发会话（门户前端 / 后台 01–05 / 后台 06–09 / 补后端）铺路。

## 2. 设计稿

位置：`~/Desktop/Pandora前端代码-20260923/设计稿/`（已从 handoff zip 解压，去掉了旧前端副本 uploads/）。

- `设计规范.dc.html`：颜色、字号、圆角、间距、控件高度、断点、组件样式——第 ③④ 步的唯一依据。
- `管理后台.dc.html` + `管理后台-01…09-*.dc.html`：后台 9 个模块。
- `用户门户.dc.html` + `用户门户-01…10-*.dc.html`：门户 10 个页面。
- `功能对照.md`：设计方对功能的说明；`README-handoff.md`：handoff 说明。
- 同级的 `../功能清单.md` 是旧前端已实现功能清单，只用来查漏，不是设计。

已知的设计要点：语义化 CSS 变量，明暗两套（亮色 `--bg #f5f4f0`、`--surface #fff`、`--text #1c1c1f`、`--brand #b9442b` 朱红，暗色 brand `#e46e52`，`--danger #b3263a`）；主题存 localStorage `pandora-theme`；门户主操作用朱红，后台偏墨色中性、侧栏明暗模式下都是深色 `#151518`；圆角 5/7/9/12/14/999；控件高度后台 32/28/24、门户 40/36/32；断点 960 与 640，小于 640 时底部标签栏、对话框变底部抽屉。**以设计规范文件为准，上面只是索引。**

## 3. 已定决策（不要再问）

- 设计稿有、后端没有 → 补后端；后端有、设计稿没有 → 补进前端；视觉与交互按设计稿。
- 前端完全重写；不用 Ant Design 或任何现成 UI 组件库；Geist 字体（OFL）随包自带，不走 Google Fonts 或镜像；不做 USDT。
- 与设计冲突时**保留后端规则**的只有 6 条，其余冲突一律按设计改后端：
  1. 快捷登录只能在已登录设备上生成，60 秒有效；
  2. 后台看不到用户的订阅地址；
  3. 用户看不到节点的国家和负载；
  4. 管理员改密码会踢掉全部会话（含当前）；
  5. 只能迁移未部署的草稿节点；
  6. 设计里的「欠费单」做成后端真实语义「挂账」（转入余额）。
- 核对中发现**不属于以上 6 条、也无法按"设计优先"直接定**的新冲突，写进契约文档的「待决」一节，停下来报告，不要自己拍板。

## 4. 技术约束（部署前提，违反即上线白屏）

- 栈：React + TypeScript + Vite，一个工程两个入口，产物到 `dist/admin/` 与 `dist/portal/`；允许 react-query、zod；依赖越少越好，每加一个运行时依赖在报告里说明理由。
- Vite `base: './'`；入口 `index.html` 必须带 `<meta name="pandora-app" content="admin">`（或 `portal`）；所有入口引用都在 `assets/` 下；`.vite/` 元数据不嵌入。
- CSP 是 `script-src 'self'; style-src 'self'; font-src 'self'; connect-src 'self'`，**没有 unsafe-inline**：禁止内联脚本、内联 `<style>`、任何运行时注入 `<style>` 的 CSS-in-JS；用普通 CSS 文件或 CSS Modules。React 的 `style` 属性走 CSSOM，允许但别滥用。
- 路由用 hash；缺失资源后端一律 404，不回退入口。
- 后台部署在 nginx 高熵前缀后面（`/__AEGIS_ADMIN_PATH__/`，nginx 剥掉前缀再转发，见 `panel/deploy/nginx-aegis.conf`）：**所有请求用相对路径**（`v1/...` 相对入口页解析），不得写死 `/v1`。
- 认证是 `Authorization: Bearer`（`panel/internal/middleware/auth.go`），没有 cookie；SSE 端点（admin/public 的 `GET /v1/events`）因此只能用 fetch 流读取，不能用 EventSource。
- 错误信封：`{"error":{"code","message","fields?","request_id?"}}`，错误码是 `panel/internal/platform/httpx/httpx.go` 里的封闭列表。
- 写操作的幂等：请求头 `Idempotency-Key`（`panel/internal/middleware/idempotency.go`），同一次用户意图的重试必须复用同一个 key。
- 后台 53 条写路由挂着 `RequireRecentReauth`（15 分钟）；**目前它只回通用 `forbidden`，前端无法区分**。第 ⑤ 步允许做这一处后端小改：在 httpx 登记 `reauth_required`（403），RequireRecentReauth 改用它，补测试，前端据此弹重新验证身份，验证后重放原请求。
- 单文件 ≤ 800 行。

## 5. 步骤（每步一个提交，做完停下报告）

| 步 | 内容 | 验收 |
|---|---|---|
| ① 脚手架 | `panel/frontend`：Vite+React+TS、双入口、lint/typecheck/test/build 脚本；恢复 `.github/workflows/pandora-native.yml` 的前端任务（npm ci → typecheck → test → build → `make frontend-embed` → `go test ./web/...`）；播种 `panel/frontend/CLAUDE.md`（L2） | 本机 `make frontend-embed && go test ./web/...` 对真实产物通过；跑完后 `git checkout panel/web` 恢复占位页，不提交产物 |
| ② 接口契约 | `panel/docs/redesign/api-contract.md`：见第 6 节 | 协调会话逐条对照 Go 代码核对 |
| ③ 设计规范 | tokens.css（明暗两套）、Geist 字体文件与 @font-face、主题切换 | 明暗两种主题截图对照设计规范 |
| ④ 组件库 | Button、Input、Select、Switch、Checkbox、Tag、Card、Table、Tabs、Segmented、Modal/Sheet、Drawer、Toast、Menu、Skeleton、Empty；一个只在 dev 出现、不进产物的演示页 | 960 / 640 两个断点截图；产物里没有演示页 |
| ⑤ 底层 | `src/core/api.ts` 唯一 HTTP 出口（相对路径、Bearer、错误信封解析、Idempotency-Key、reauth_required 重放、fetch 流 SSE）；zod 校验响应；react-query；hash 路由；上面那处后端小改 | 单元测试覆盖：幂等 key 在重试间复用、reauth 后重放、SSE 断线重连、前缀下相对路径解析 |
| ⑥ 外框 | 后台：深色侧栏 + 顶栏；门户：顶部导航，<640 底部标签栏；两边登录流程与退出 | 能登录进空壳、退出、刷新保持登录态 |

①② 与设计规范无关，可以先做；③ 开始完全照设计规范。

## 6. 接口契约文档（第 ② 步）要求

它是第 3 阶段前后端并行的唯一依据：前端照它写，补后端照它实现，合并时以它对账。

- 按 admin / public 两个网关、再按设计稿模块分节。每个接口：方法+路径、认证、是否要求 reauth、是否要 Idempotency-Key、请求体、响应体、错误码、状态标记。
- 状态标记三种：**现有**（写出 Go 处理函数位置）、**待补·后端**（设计要、后端没有，把形状定下来）、**待补·前端**（后端有、设计没有，写明补进哪个页面）。
- 以下缺口是协调会话此前核对得出的，逐条落进契约，发现不对就更正：
  - 与后端冲突需适配：支付是 epay 收银台跳转（无二维码、余额抵扣 `use_balance`、订单 30 分钟过期，注意 CSP `form-action 'self'` 可能挡 POST 跳转）；Telegram 绑定是 `/start CODE`（8 位、10 分钟）；邀请链接是 `/?invite=`（`/r/CODE` 会撞订阅通配）；工单/订单/设备策略状态、开关编码、通知偏好类目（transactional/service/marketing × email/telegram）、webhook 事件名与设计不一致，以后端为准并在前端映射；访问日志是安全事件而非 HTTP 日志。
  - 设计有、后端缺：流量包（先确认后端是否已有）、升级折算、「有帮助」反馈、礼品卡掩码与一次性导出和批次列表、全局路由组、优惠券编辑、模板预览、对账、套餐取消归档、工单快捷回复、仅首单返佣、风控批量禁用与标记正常、审计导出与搜索；只读数据缺口：用户到期/流量/设备、节点 CPU、服务商统计、管理员姓名邮箱、门户重置日与在线设备、系统状态组件等。（邮件登录链接不做，属保留规则范围。）
  - 后端有、设计缺：门户——余额流水、提现、被邀请人、订单明细、订阅拉取统计、续费遇改价、工单关联订单、公告级别、站点配置；后台——节点池增删改、用户组编辑删除、单节点路由编辑、服务器详情与编辑、节点交付提示、节点字段、套餐高级设置、工单内部备注与 SLA、备份状态、服务商 accepting_new、调账生效日、公告定时、知识库字段、邮件设置（含注册模式）、主题品牌、完整用户资料。
- 末尾两节：**待决**（需要用户拍板的新冲突）和**迁移预估**（哪些待补后端项需要新表或改表，粗估数量；现有最新迁移是 00067，编号由协调会话统一分配，你不要创建迁移文件）。

## 7. 边界

- 只在 `feat/panel-redesign-p2` 上提交；**不推送、不部署、不跑迁移**；提交信息英文祈使句，结尾带协作者行。
- 不动 `panel/go.mod` / `go.sum`，除非确有必要并在报告里说明。
- 第 ⑤ 步那处 reauth 改动之外不改后端；`api/*/router.go` 和 L1 `CLAUDE.md`、README 由协调会话合并时统一改，你把需要的改动写进报告。
- 仓库已有的 3 处 gofmt 问题（billing/checkout.go、nodefabric/enrollment.go、nodefabric/node_admin.go）不要顺手改。
- 本机没有 Docker，依赖 PostgreSQL 的测试会跳过，这是预期；Go 全量测试用 `go test -p 1 -count=1 -timeout 10m ./...`。
- 启用 GEB 文档协议（见根 CLAUDE.md）：新目录建 L2，TS/TSX 文件带 L3 头。
- 每步报告：提交号、跑了哪些检查及结果（原样贴失败输出）、没跑什么、给协调会话的待办（路由、全局文档、待决问题）。

## 8. 进度与补充事项（协调会话维护，接力的新会话从这里接上）

**进度**（均已合入 `feat/panel-redesign`）：① 脚手架 `54cec13`；② 接口契约 `ccb28a8`；③ 设计规范 `51fd36e`；④ 组件库 `c5ab1f7`（`src/ui/` 16 类组件，门户后台共用、差异只来自角色令牌；弹窗用原生 `<dialog>`，Toast 用 popover）。**下一步 ⑤ 底层，然后 ⑥ 外框，⑥ 做完第 2 阶段结束。**

**做事前先 `git merge feat/panel-redesign`**，主线上有后端会话的最新改动和契约修订。

补充事项（执行过程中陆续定下的，与上文冲突时以这里为准）：
- **推送**：用户已授权 `feat/panel-redesign` 开头的分支推到 origin 跑 CI。只推自己的分支，不推 main、不开 PR、不强推；每步做完推送一次，报告附两个 workflow（Pandora NativeCore、Panel PostgreSQL 18 gates）的结果。
- **恢复占位页**：`make frontend-embed` 验证完要跑 `git checkout -- web && git clean -fdX web`；只 checkout 删不掉被忽略的 `web/*/assets/`。
- **契约以修订为准**：`api-contract.md` 第 5.A 节（8 条已决）和第 9 节修订记录（R1 起）优先于原文，条目里「修订 Rn」开头的行优先于该条目。第 ⑤ 步写 `api.ts` 前重读第 1 节和第 9 节：reauth 路由已增至 45 条；缺权限回 404；三个口令错也回 401 的接口不能触发全局登出。
- **reauth_required**：仍由本会话在第 ⑤ 步做（httpx 登记错误码、RequireRecentReauth 改用它、补测试）；后端会话被要求不碰这两处。
- **主题**：只有「默认 · 纸白」一个主题（5.A）。后端 `GET v1/appearance` 的 `theme.tokens` 是 `{ light: {"--bg": …}, dark: {…} }`，键就是 `src/styles/design-tokens.ts` 的 43 个名字（迁移 00075，后端有测试逐条对照）；门户按当前明暗取一组 `setProperty`，白名单外的键忽略。`--brand-hover` 取 `#a33a23`。
- **测试库**：第 ④ 步没引入 DOM 测试库；第 ⑤ 步如确需（api 重放、SSE 重连），可以加，报告里写理由。

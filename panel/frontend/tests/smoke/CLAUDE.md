# panel/frontend/tests/smoke/
> L2 | 父级: /panel/frontend/CLAUDE.md

新前端对真实网关的联调冒烟（面板重构第 4 阶段）。假后端只证明「页面按契约走得通」，这里证明「页面拿到真数据不会崩」：CI 的 panel-smoke.yml 先用 panel/deploy/run-smoke-stack.sh 起一次性 PG18 + Valkey + 三个网关，再在这里造数据、用页面自己的 zod schema 解析真响应；同一个栈最后还由 deploy/run-smoke-e2e.sh 跑一遍仓库现有的 tests/*_e2e.sh（第 ⑤ 步，只报告）。不进 make frontend-check（本机没有数据库）；发现形状不一致只报告，不在这里放宽断言。入口一律读状态目录（smoke.env、gateway.env、seed.json），不含任何真实部署的值。

成员清单
seed.ts: 造数据，`node seed.ts <状态目录>`（SQL 夹具连 smoke.env 里的 SMOKE_PG_DB）（Node 22 原生剥类型）：池与套餐先行（池绑在发布版本上）→ 新服务器 + 新节点 → 节点抽屉签发接入令牌 → Ed25519 两段式接入（begin / commit，与 pdnd 同一套规范串，运行令牌由「节点」生成）→ POST activate 一步上线（R108 / R113，不再逐级调旧 status）→ UniProxy 心跳；门户注册与邀请注册、下单经演示渠道付清、留一张待支付、被邀请人付一单造佣金明细、对已付单再送一笔回调造挂账；全局与单节点路由各一条规则；节点取用户列表后上报流量与在线 IP（流量排行、按日用量、在线设备都只由节点上报产生）；本机插件接收端 + 订阅 ticket.created 的钩子先于工单建好；工单与回复、快捷回复、用户组、公告、知识库、礼品卡与兑换、优惠券及带券下单、流量包上架与余额购买、收入调整、手动流量重置；末了核对各列表不空，id、门户账号与节点运行令牌写进 seed.json。后台请求间隔 300ms；两处 SQL 夹具各写明原因：演示支付渠道（没有新建渠道的接口）、提现申请（佣金只由每小时的任务解冻，且同 IP 被标待复核、无接口解除）
harness.ts: 形状校验的底座：读状态目录，两个身份各登录一次，用页面的 createApiClient（内存令牌、不重试）发 GET，解析失败时按 zod 问题路径从原始响应里取片段；Row 行类型（json / raw 导出 / sse 事件流、跳过原因、异步数据的等待）；runTable 每行一个用例，结果逐行写进状态目录的 smoke-results.md 供 CI 贴进 job summary，列表行另记条数与覆盖（验到行 / 只验到外层）和造数来源；pageClient 供写路径注入令牌与 requestReauth；后台请求同样隔 300ms
admin.smoke.ts: 第 ③ 步后台接口表：前端 73 处后台 GET 调用逐一成行（at 列为 src/admin 下的 file:line），schema 从调用处的页面模块导入；种子里没有的 id（知识库页版本、礼品卡批次）先原样取列表；两个 CSV 导出与事件流只验状态与内容类型
portal.smoke.ts: 第 ③ 步门户接口表：32 处门户 GET 调用与共享的事件流；调用处内联的外层 z.object 照原样重写、里面的行 schema 导入；通知只由定时扫描写入，等到非空或 8 分钟超时就标跳过
hook-receiver.ts: 本机插件投递接收端（seed.ts 以独立进程拉起，pid 记进状态目录由 down 收掉）：只听 127.0.0.1，每次投递往 hook-received.jsonl 追加一行并回 200；devMode 下面板本来就放行回环，不改 Go、不放宽校验
writes.smoke.ts: 第 ④ 步写路径：以节点运行令牌取用户列表并在门户订阅配置里找到同一凭据（新服务器 + 新节点一步上线后能下发）；重认证用同一真实会话、把令牌 rat 拨回 16 分钟，由页面 api 客户端自己走 reauth_required → reauth → 同键重放；工单回复验同键重放（Idempotency-Replayed）与换体 409；用户、营销、节点、套餐、系统、安全、财务、门户各一个写操作，请求体用页面构造函数、响应用页面 schema；插件接收端落盘里有带签名头的 ticket.created；工单回复后门户通知里出现 ticket.replied、正文带那张工单的标题（R115；admin 只排队，轮询等 public 网关的派发循环，6 分钟没有才算失败）
vitest.config.ts: 冒烟专用配置：只收 *.smoke.ts（默认的 *.test.ts 收不到，所以不进 make frontend-check），node 环境、文件串行、单条 90 秒；自带 sequencer（ReadsBeforeWrites）把 writes.smoke.ts 固定排在两张读表之后——写路径会改种子数据，顺序是冒烟自己的数据依赖，放在配置里而不是 workflow 里，本机单跑也成立
tsconfig.json: 冒烟专用类型检查：继承浏览器侧 tsconfig（页面模块连带 .tsx 要 jsx），加 node 类型；tsconfig.node.json 因此排除 tests/smoke

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

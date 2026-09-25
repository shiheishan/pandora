# panel/frontend/tests/smoke/
> L2 | 父级: /panel/frontend/CLAUDE.md

新前端对真实网关的联调冒烟（面板重构第 4 阶段）。假后端只证明「页面按契约走得通」，这里证明「页面拿到真数据不会崩」：CI 的 panel-smoke.yml 先用 panel/deploy/run-smoke-stack.sh 起一次性 PG18 + Valkey + 三个网关，再在这里造数据、用页面自己的 zod schema 解析真响应。不进 make frontend-check（本机没有数据库）；发现形状不一致只报告，不在这里放宽断言。入口一律读状态目录（smoke.env、gateway.env、seed.json），不含任何真实部署的值。

成员清单
seed.ts: 第 ② 步造数据，`node seed.ts <状态目录>`（Node 22 原生剥类型）：节点池 → 服务器 → 划进池的节点 → 状态逐级推进 → 服务器就绪 → UniProxy 心跳；向导建套餐并把池绑在发布版本上；门户注册、邀请注册、下单并经演示渠道签名回调付清、留一张待支付；工单与回复、公告、知识库、礼品卡与兑换、优惠券；末了核对后台与门户各列表不空，id 与门户账号写进 seed.json。后台请求间隔 300ms 避开每分钟 240 次的 IP 限流；唯一的 SQL 是照 deploy/seed-demo.sql 插入演示支付渠道（后台没有新建渠道的接口）
harness.ts: 形状校验的底座：读状态目录，两个身份各登录一次，用页面的 createApiClient（内存令牌、不重试）发 GET，解析失败时按 zod 问题路径从原始响应里取片段；Row 行类型（json / raw 导出 / sse 事件流、跳过原因、异步数据的等待）；runTable 每行一个用例，结果逐行写进状态目录的 smoke-results.md 供 CI 贴进 job summary；后台请求同样隔 300ms
admin.smoke.ts: 第 ③ 步后台接口表：前端 73 处后台 GET 调用逐一成行（at 列为 src/admin 下的 file:line），schema 从调用处的页面模块导入；种子里没有的 id（知识库页版本、礼品卡批次）先原样取列表；两个 CSV 导出与事件流只验状态与内容类型
portal.smoke.ts: 第 ③ 步门户接口表：32 处门户 GET 调用与共享的事件流；调用处内联的外层 z.object 照原样重写、里面的行 schema 导入；通知只由定时扫描写入，等到非空或 6 分钟超时就标跳过
vitest.config.ts: 冒烟专用配置：只收 *.smoke.ts（默认的 *.test.ts 收不到，所以不进 make frontend-check），node 环境、文件串行、单条 90 秒
tsconfig.json: 冒烟专用类型检查：继承浏览器侧 tsconfig（页面模块连带 .tsx 要 jsx），加 node 类型；tsconfig.node.json 因此排除 tests/smoke

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

# panel/frontend/tests/smoke/
> L2 | 父级: /panel/frontend/CLAUDE.md

新前端对真实网关的联调冒烟（面板重构第 4 阶段）。假后端只证明「页面按契约走得通」，这里证明「页面拿到真数据不会崩」：CI 的 panel-smoke.yml 先用 panel/deploy/run-smoke-stack.sh 起一次性 PG18 + Valkey + 三个网关，再在这里造数据、用页面自己的 zod schema 解析真响应。不进 make frontend-check（本机没有数据库）；发现形状不一致只报告，不在这里放宽断言。入口一律读状态目录（smoke.env、gateway.env、seed.json），不含任何真实部署的值。

成员清单
seed.ts: 第 ② 步造数据，`node seed.ts <状态目录>`（Node 22 原生剥类型）：节点池 → 服务器 → 划进池的节点 → 状态逐级推进 → 服务器就绪 → UniProxy 心跳；向导建套餐并把池绑在发布版本上；门户注册、邀请注册、下单并经演示渠道签名回调付清、留一张待支付；工单与回复、公告、知识库、礼品卡与兑换、优惠券；末了核对后台与门户各列表不空，id 与门户账号写进 seed.json。后台请求间隔 300ms 避开每分钟 240 次的 IP 限流；唯一的 SQL 是照 deploy/seed-demo.sql 插入演示支付渠道（后台没有新建渠道的接口）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

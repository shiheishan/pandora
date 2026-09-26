# panel/tests/
> L2 | 父级: /panel/CLAUDE.md

面板的数据层不变量与端到端脚本。它们打真实网关与真实库，本机没有数据库时跑不了；CI 的 panel-smoke.yml 经 deploy/run-smoke-e2e.sh 在一次性冒烟栈上逐个跑（联调冒烟第 ⑤ 步起），脚本假定的是 /opt/aegispanel 的 docker-compose 布局（deploy/.env、deploy/psql.sh、容器 aegis-postgres），runner 只把这套环境搭出来、不改脚本。会留下不可逆证据（审计、账本、订单）的脚本要求显式的一次性库确认变量。

成员清单
invariants.sql: 数据层不变量（make invariants）：证明库拦得住违规写入
e2e.sh: 主链路（make e2e）：注册按邮箱验证关闭的默认走、验证码段临时开启再恢复，自建带权益的套餐下单，演示回调十连发只记一笔，账本配平，订阅与配额，审计链，限流；需要 ADMIN_EMAIL / ADMIN_PASS
admin_e2e.sh: 管理面性质：令牌不跨域、无角色登录与错口令一致、默认拒绝、高风险动作要重认证、写操作留审计、essential 开关不可关、套餐发布与节点池；要求 ADMIN_E2E_DISPOSABLE 等确认变量
epay_e2e.sh: 易支付：收银台参数与签名、金额换算、回调幂等、防篡改、复式记账、重复「去支付」复用意图；需要启用的 epay 渠道（测试商户 1001）
node_e2e.sh: 节点生命周期，跑真实 aegis-agent 进程：引导令牌、接入、状态机、心跳与签名、分层配置与篡改拒绝、吊销与重新引导
support_e2e.sh: 工单：内部备注绝不外泄、SLA 升级幂等、越权隔离、状态流转、未结工单上限
uniproxy_e2e.sh: UniProxy 数据面契约，SQL 夹具造 serving 节点：认证、配置 ETag、资格与池隔离、流量去重与倍率、在线上报；要求 UNIPROXY_E2E_DISPOSABLE 等确认变量

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

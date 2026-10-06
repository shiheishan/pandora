# panel/cmd/aegis-node/
> L2 | 父级: /panel/CLAUDE.md（cmd/ 一行）

节点控制面网关进程（Node 域），调用方是 pdnd 与兼容 UniProxy 的节点端，不是人
  - run 只做装配：配置签名器（及轮换中的旧签名器）、nodefabric 服务（GeoIP 可选、发布物绑定、事件流 Hub、跨进程实时 Hub）与 api/node 路由
  - 审计来源信息的哈希与加密（audit.Configure）与 public、admin 两个网关逐字相同，三者写同一张 audit_events
  - 没有后台循环，用 server.Run 自带的信号处理停机。

成员清单
main.go: main / run 装配与生命周期
*_test.go: main_test 经 platform/sourcetest 取本包与两个兄弟网关的 run，钉死三处 audit.Configure 都在开服之前且哈希与加密写法逐字相同

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

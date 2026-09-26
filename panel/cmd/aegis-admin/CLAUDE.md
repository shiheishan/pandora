# panel/cmd/aegis-admin/
> L2 | 父级: /panel/CLAUDE.md（cmd/ 一行）

管理控制台网关进程，与 aegis-public 分进程分端口：用户面被打爆也挤不掉管理员的处置能力（ARC-002）。run 装配 domain 各服务与 api/admin 路由，并起四个后台循环（工单超时升级、定时公告、配额周期滚动、佣金解冻）；四个循环都挂在信号 context 上，停机时先 stop 再限时等它们退出，超时就不关连接池，防止循环还在用时被拆掉。

成员清单
main.go: main / run 装配与生命周期，waitForAdminWorkers 限时等待后台循环
salescap.go: 销售能力的注入方：adminops.SalesCapability 是 fail-closed 闸门，这里把授权做成部署时的显式环境变量决定
*_test.go: main_contract_test 经 platform/sourcetest 取 run 的源码，钉死信号 context、四个循环的取消与等待次序、资源关闭顺序、工单回复通知装配与通知收件人专用盐

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

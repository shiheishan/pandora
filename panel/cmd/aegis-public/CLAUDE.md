# panel/cmd/aegis-public/
> L2 | 父级: /panel/CLAUDE.md（cmd/ 一行）

用户门户网关进程（Public 域）。run 装配 domain 各服务与 api/public 路由，接上注册验证码邮件，并起通知扫描、插件投递、预留过期等后台循环；停机时先取消信号 context、等预留过期循环退出，再交给 defer 关资源。

成员清单
main.go: main / run 装配与生命周期；startReservationExpiryWorker 按周期释放过期预留，返回等待函数供停机时 join
*_test.go: 预留过期循环取消后能退出；经 platform/sourcetest 取 startReservationExpiryWorker 与 run 的源码，钉死取消、join、返回的次序，以及通知收件人哈希用 crypto.NotifyRecipientSalt

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

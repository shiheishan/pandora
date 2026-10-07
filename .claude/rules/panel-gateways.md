---
paths:
  - "panel/cmd/**"
  - "panel/internal/platform/server/**"
  - "panel/internal/platform/profiling/**"
  - "panel/internal/platform/config/**"
---

# 网关进程装配与命令行

- aegis-public、aegis-admin、aegis-node 分进程分端口，不要合并：用户面被打爆时，管理员的处置能力不能跟着丢
- 三个网关的共同装配只有一处契约：`panel/cmd/aegis-node/main_test.go`
  - `audit.Configure` 在三个 `run` 里都要在开服之前调用，且哈希与加密写法逐字相同（三者写同一张 `audit_events`）。改一处就改三处，守卫 `TestGatewaysConfigureAuditSourceIdentically`
  - pprof 每个网关只起一个，取本域的 `config.PprofAddrs`，开服前起、defer 关闭，守卫 `TestGatewaysStartPprofFromTheirOwnVariable`
- 后台循环的生命周期：有循环的网关（public、admin）自建信号 context，调 `server.RunContext`；停机时先取消，限时 join 循环，再交给 defer 关资源；admin 的 join 超时就不关连接池（`waitForAdminWorkers`），免得拆掉仍在用的连接。node 也有后台循环（签名请求 nonce 的过期清理），同样自建信号 context、`server.RunContext`、限时 join
  - 新增循环要挂在同一个信号 context 上并加入 join。守卫：`panel/cmd/aegis-admin/main_contract_test.go` 的 `TestAdminWorkersShareSignalContextAndJoinBeforeCleanup`、`panel/cmd/aegis-public/main_test.go` 的 `TestPublicProcessCancelsExpiryWorkerBeforeResourceCleanup`
- 通知收件人哈希在 admin 与 public 两边都用 `crypto.NotifyRecipientSalt`，同一收件人两边要算出同一个值。守卫：`TestAdminNotifyUsesRecipientSalt`、`TestPublicNotifyUsesRecipientSalt`
- pprof 只经 `platform/profiling` 开在独立回环端口上，不要挂到网关路由：它能导出含明文密钥的堆
  - 地址合法性（只收回环 IP 字面量）在 `platform/config` 的 `Load` 里判，profiling 再对实际绑定地址验一次回环
  - 引入 `net/http/pprof` 会在 init 里往 `DefaultServeMux` 注册，所以 `platform/server` 拒绝 nil Handler。两道闸一起保证 pprof 只出现在那个端口上，不要拆掉任何一道
- 走 `config.Load` 的是三个网关与带数据库的命令行（adminctl、payctl）；不连库的小二进制（如 aegis-backup-webdav 用 `config.LoadBackupWebDAV`）各有小加载函数，不要让它们去满足 `Load` 的必填项
- aegis-adminctl 是「第一个管理员从哪来」的唯一入口，只在服务器本机跑，不要做成 HTTP 接口，否则就是人人可调的提权口
  - 口令只从标准输入读（`--password-stdin`），不加 `--password` 参数，因为进程参数会进 ps 与 shell 历史。守卫：`panel/cmd/aegis-adminctl/main_test.go` 的 `TestAdministratorPasswordCommandsRejectPasswordArguments`

# panel/cmd/aegis-adminctl/
> L2 | 父级: /panel/CLAUDE.md（cmd/ 一行）

后台账号与角色的本机命令行。存在的理由是「第一个管理员从哪来」：管理界面要登录才能进，初装时一个管理员都没有，这类 bootstrap 只能在服务器本地做，开成 HTTP 接口就是人人可调的提权入口。口令只从标准输入读，绝不走进程参数（会进 ps 与 shell 历史）。

成员清单
main.go: create / reset-password / grant / revoke / list / roles 子命令；改密与吊销全部登录凭据同事务，改密行数不为 1 即失败不吊销；租户级角色绑定绕开 NULL 不相等的旧唯一约束，不用 ON CONFLICT
*_test.go: 假事务驱动 SQL 路径与顺序；经 platform/sourcetest 取整包源码，断言没有 --password 参数、两个改密命令都只收 --password-stdin

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

# panel/internal/platform/pg18test/
> L2 | 父级: /panel/internal/platform/CLAUDE.md

PG18 集成测试的公共护栏：按 run-pg18-gates.sh 注入的 AEGIS_<域>_PG18_* 打开一次性库，库名前缀、标记表 run ID、库注释三者都对上才放行，admin 与 aegis_app 两条连接各验一遍；变量全无则跳过、只给一部分则失败。只被 *_pg18_test.go 引用，不进生产二进制。早期的 PG18 用例各自抄了一份这套护栏，新用例统一走这里。

成员清单
pg18test.go: Fixture 描述一个域的库身份，Open 返回带超时的 ctx、管理连接与运行时角色连接池，释放挂在 t.Cleanup 上

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

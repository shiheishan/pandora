---
paths:
  - "panel/**/*_pg18_test.go"
  - "panel/**/*_pg18_*_test.go"
  - "panel/internal/platform/pg18test/**"
  - "panel/deploy/run-pg18-gates.sh"
  - "panel/deploy/test-*-pg18.sh"
---

# PG18 集成测试

- 新的 PG18 用例经 `platform/pg18test` 的 `Open` 打开一次性库：库名前缀、标记表 run ID、库注释三者都对上才放行，admin 与 `aegis_app` 两条连接各验一遍。部分既有用例仍各带一份同样的护栏，不要再抄新的
  - 环境变量 `AEGIS_<域>_PG18_*` 全无则跳过，只给一部分则失败
- 测试只在 `panel/deploy/run-pg18-gates.sh` 里真正跑（CI 的 panel-pg18 工作流调用它）。它把任何 `--- SKIP` 或一个顶层 PASS 都没有判为失败
  - 同一个包里住着多个域（如 `api/admin` 的 announcement、node_config、delivery；nodefabric、billing、subscription 同理），各域的 `-run` 过滤写成精确的函数名列表
  - 所以在这类包里新增 PG18 测试函数，必须把函数名加进对应域的过滤；否则要么根本不跑，要么被别的域拉进去后因缺环境变量而跳过，判红
  - 新起一个域就在 `DOMAINS` 加一行，`pg18test.Fixture` 的取值与那一行一一对应
- 需要独占水位或全局单例的测试（如 billing 的 order_release、payment_query）各占一个库，不要和 checkout/settlement 共库
- 夹具只用虚构数据；`test-*-pg18.sh` 的口令带 `test-only` 字样，每次新建隔离容器与库、结束即删

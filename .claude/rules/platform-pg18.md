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

## 多路并行时的坑（2026-10 第一、二波）

- 夹具 id 撞号是最常见的 CI 红：同一个域的用例共用一个库（`DOMAINS` 一行一个库，可跨包，如 delivery 域同时跑 subscription 与 api/admin），撞上别人的租户、用户 id 就是主键冲突，后跑的那个失败。w1sub（…0901）、w2node（…0201）、w2dash（`7e` 前缀）各红过一次
  - 做法：新夹具先在主线 `grep -rn '<前缀或号段>' panel --include='*_pg18*_test.go'` 确认没人用（前缀和末段编号都要查，不同域碰巧同前缀不冲突，同域才冲突），优先取一段别人十进制编号够不着的十六进制前缀
- 新用例优先挂成已登记顶层测试的子测试（`t.Run`），同库同域就不用改 `run-pg18-gates.sh` 的 `-run` 过滤；只有确实要新的顶层函数或新库时才动脚本
- 新增域要在 `DOMAINS` 加一行。几路同时各加一行，合并时会在相邻行冲突：两行都保留，按域名核对 `pg18test.Fixture` 的取值
- 本机没有 Docker 时 PG18 用例全部跳过，只能靠推送后 GitHub 的 panel-pg18 判（`wait-github.sh` 把 SKIP 判为失败）。推送前把 SQL 和夹具多看一遍，并预留一次 CI 往返
- 证明测试真的在测：把被测代码临时改回旧写法（或在基点上跑），确认新用例变红，再改回来；报告里写明做过。这一波的契约测试、PG18 对照都这样验过，比只看「新测试是绿的」可靠

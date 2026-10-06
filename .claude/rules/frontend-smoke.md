---
paths:
  - "panel/frontend/tests/smoke/**"
---

# 面板前端：对真实网关的联调冒烟

- 分工：假后端只证明「页面按契约走得通」，这里证明「页面拿到真数据不会崩」
- 只在 CI 的 `panel-smoke.yml` 里跑，不进 `make frontend-check`（本机没有数据库）
  - `vitest.config.ts` 只收 `*.smoke.ts`，默认的 `*.test.ts` 收不到这里
  - 本机要跑，先 `panel/deploy/run-smoke-stack.sh up`，再 `node tests/smoke/seed.ts <状态目录>`，然后带上 `SMOKE_STATE` 跑 `npx vitest run -c tests/smoke/vitest.config.ts`
- 读表里的 schema 是从页面模块导入的
  - 页面改了 GET 调用，或者改名、移动了导出的 schema，都要同步 `admin.smoke.ts` / `portal.smoke.ts`
  - 冒烟的类型检查（`npx tsc -p tests/smoke/tsconfig.json`）只在 `panel-smoke.yml` 里跑，`npm run typecheck` 不覆盖它（`tsconfig.node.json` 排除了 tests/smoke）
- 发现响应形状与页面 schema 不一致时只报告，不在冒烟里放宽断言，也不为了冒烟放宽 Go 的校验
- 输入只从状态目录读（`smoke.env`、`gateway.env`、`seed.json`），不写任何真实部署的值
- 后台请求之间留 300ms：后台 IP 限流每分钟 240 次，写死在代码里。`seed.ts` 与 `harness.ts` 用同一节奏，新增后台调用也要走各自的节流函数
- SQL 夹具只用于产品接口造不出的数据，每处都写明原因。现有两处：演示支付渠道、提现申请
- `writes.smoke.ts` 会改种子数据。`vitest.config.ts` 的 `ReadsBeforeWrites` sequencer 把它固定排在两张读表之后，文件串行执行。这个顺序放在配置里而不是 workflow 里，本机单跑也成立
- `seed.ts` 由 Node 直接执行（原生剥类型）
  - 只能用可擦除的 TS 语法
  - 不能 import `src/`，因为页面模块用的是无扩展名导入
- 插件投递接收端 `hook-receiver.ts` 只听 127.0.0.1。面板 devMode 本来就放行回环地址，不为它改 Go

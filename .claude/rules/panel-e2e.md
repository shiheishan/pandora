---
paths:
  - "panel/tests/**"
  - "panel/deploy/run-smoke-stack.sh"
  - "panel/deploy/run-smoke-e2e.sh"
---

# 端到端脚本与冒烟栈

- `panel/tests/*.sh` 打真实网关与真实库，本机没有数据库跑不了；CI 在一次性冒烟栈上经 `panel/deploy/run-smoke-e2e.sh` 逐个跑
  - 脚本假定 `deploy/install.sh` 直装在 `/opt/pandora` 的布局（`deploy/.env`、`deploy/psql.sh` 用主机 psql 经 `127.0.0.1:POSTGRES_PORT` 以超级用户 `postgres` 连）。runner 只负责把这套环境搭出来（冒烟栈的 `datastore.env` 给出与直装同名同义的库与缓存几项），不要为了 CI 改脚本
  - 新增 e2e 脚本要加进 `run-smoke-e2e.sh` 的 `SCRIPTS`，它声明要的环境也由 runner 搭
  - 顺序固定 admin → epay → support → uniproxy → expiry → password_reset → portal_staff → risk → e2e（expiry 要等 aegis-admin 的过期扫描接手，最多 3 分钟）：`e2e.sh` 的限流探测会打满登录额度，必须最后；脚本之间空一个限流窗口
  - `run-smoke-e2e.sh` 只肯在 GitHub Actions 上跑；全部跑完、结果表写完后才判失败
- 会留下不可逆证据（审计、账本、订单）的脚本必须要求显式的一次性库确认变量（`ADMIN_E2E_DISPOSABLE`、`UNIPROXY_E2E_DISPOSABLE`、`EXPIRY_E2E_DISPOSABLE`、`RISK_E2E_DISPOSABLE`，值为 `YES_DELETE_FIXTURES`），runner 负责设置
- 冒烟栈的库名 `aegis_smoke_test` 带 test 段，才能过 e2e 脚本的一次性库守卫；改库名会让脚本拒跑
- 节点两阶段接入与上线不在这里测：由 `panel/frontend/tests/smoke/seed.ts` 用真实 Ed25519 签名覆盖
- `risk_e2e.sh` 是带标签的效果评估：机制断言记 `[ OK ]` / `[FAIL]`，误判只记 `[EVAL]`、不判红
  - 阈值只按调参组的 Youden 指数选，留出组只做验证，不要拿留出组调参
  - 造「过去几天」的数据：先经真实端点拉取，再在一次性库里用 `session_replication_role = replica` 挪 `fetched_at`；审计是哈希链，不改它的时间
- 测试数据只用虚构值；来源地址用 `X-Real-IP` 扮演（冒烟栈没有 nginx，网关直接信它）

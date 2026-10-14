---
paths:
  - "panel/tests/**"
---

# 端到端脚本（panel/tests）

- `panel/tests/*.sh` 打真实的 `pandora serve` + `pandora worker`（门户 `127.0.0.1:9000`、后台 `127.0.0.1:9001`，脚本里的 `PUB` / `ADM`）和真库，本机跑不了；以 CI 为准。
- CI 在 `.github/workflows/panel-newstack.yml` 里分两个 job，各起一次性 PG18 + Redis 服务容器，等两个 `/readyz` 都通再逐个跑脚本，跑完收 serve / worker 日志：
  - 第一个跑 `auth_e2e.sh`、`password_reset_e2e.sh`、`portal_staff_e2e.sh`；
  - node-e2e 跑 `node_smoke_e2e.sh`（`pdnd/release/nodesim` 模拟节点）、`expiry_e2e.sh`、`node_kick_e2e.sh`（真 pandora-native 二进制）。
  - 新增脚本要加进对应 job 的 `for s in …` 列表，并在 job 的 env 里给它的一次性库确认变量。
- 还没接进新 CI、仍引用旧栈路径的：`admin_e2e.sh`、`epay_e2e.sh`、`support_e2e.sh`、`risk_e2e.sh`、`e2e.sh`、`invariants.sql`。改它们之前先按新栈重写，别以为它们在跑。
- 会建用户、留审计或账本的脚本，开头必须要求一次性库确认（如 `AUTH_E2E_DISPOSABLE=YES_DELETE_FIXTURES`），并核对 `current_database()` 带独立的 test / e2e 段，否则直接退出（照 `auth_e2e.sh`）。
- 查库一律经 `$PSQL`：CI 里是包一层 `psql "$PANDORA_DATABASE_URL"` 的脚本，缺省值 `/opt/pandora/deploy/psql.sh` 对应直装布局。不要在脚本里写死连接串。
- 管理员由 job 用 `pandora admin create --password-stdin` 建，口令经 `ADMIN_EMAIL` / `ADMIN_PASS` 环境变量交给脚本；脚本不打印口令，不放进命令行参数。
- 来源 IP 用 `X-Real-IP` 扮演：引擎只信这个头（CI 里没有 nginx）。
- 结果用 `[ OK ]` / `[FAIL]` 计数，最后有 FAIL 就非零退出。错误信封里比对码与文案时去掉每次不同的 `request_id`（`auth_e2e.sh` 的 `errsig`）。
- 测试数据只用虚构值（仓库公开）。

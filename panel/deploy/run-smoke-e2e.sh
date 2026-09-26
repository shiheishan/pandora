#!/usr/bin/env bash
# [INPUT]: 依赖 run-smoke-stack.sh 写在状态目录的 smoke.env 与 gateway.env，依赖同目录 psql.sh（仓库自带、写死容器 aegis-postgres），依赖 ../tests 下的 e2e 脚本、../cmd 下的 aegis-agent 与 aegis-payctl 源码，依赖 sudo、go、python3、timeout
# [OUTPUT]: 在冒烟栈上逐个跑 tests/*_e2e.sh 与 tests/e2e.sh，每个脚本一行写进 <状态目录>/e2e-results.md（通过 / 失败 / 超时、OK 与 FAIL 计数、首个失败所在的步骤与原文），各自完整输出在 logs/e2e-*.log；跑产品代码的准备步骤（编 agent、payctl 配渠道）失败也只记一行；脚本失败不影响退出码
# [POS]: 第 4 阶段联调冒烟第 ⑤ 步，被 .github/workflows/panel-smoke.yml 在读表与写路径之后调用；只报告，不修脚本、不修 Go，失败原因由协调会话看表与日志后指派
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
#
# 这些 e2e 脚本是给「装在 /opt/aegispanel 的 docker-compose 部署」写的：
# psql 走 /opt/aegispanel/deploy/psql.sh 或仓库的 deploy/psql.sh（读 deploy/.env、
# docker exec 进 aegis-postgres），agent 在 /opt/aegispanel/bin，日志在
# /opt/aegispanel/logs。本机没有数据库，它们平时从没人跑。
#
# 这里只把它们声明要的环境原样搭出来，脚本一个字不改：
#   - /opt/aegispanel/deploy 链到仓库的 deploy/，deploy/.env 由网关配置加上库超级账号拼成；
#   - 起栈时容器名已设成 aegis-postgres、库名带 test 段（admin / uniproxy 的一次性库守卫）；
#   - aegis-agent 编进 /opt/aegispanel/bin，网关日志按旧文件名链进 /opt/aegispanel/logs；
#   - 易支付渠道用产品工具 aegis-payctl 配好（脚本里写死的测试商户 1001 与测试密钥）；
#   - 两个一次性库守卫要的确认变量照实给出：冒烟库本来就是跑完即扔的。
# 这一步要往 /opt 写东西、要写 deploy/.env，所以只肯在 GitHub Actions 的一次性 runner 上跑。
#
# 用法：run-smoke-e2e.sh <panel 源码目录> <状态目录>

set -euo pipefail

[[ $# -eq 2 ]] || { echo "用法: $0 <panel 源码目录> <状态目录>" >&2; exit 2; }
PANEL_DIR="$(cd "$1" && pwd)"
STATE="$(cd "$2" && pwd)"
[[ "${GITHUB_ACTIONS:-}" == true ]] || {
  echo "只在 GitHub Actions 的一次性 runner 上跑：会写 /opt/aegispanel 与 deploy/.env" >&2
  exit 2
}
[[ -f "$STATE/smoke.env" && -f "$STATE/gateway.env" ]] || { echo "状态目录里没有冒烟栈，先 run-smoke-stack.sh up" >&2; exit 2; }
[[ ! -e "$PANEL_DIR/deploy/.env" ]] || { echo "deploy/.env 已存在，拒绝覆盖" >&2; exit 2; }
[[ ! -e /opt/aegispanel ]] || { echo "/opt/aegispanel 已存在，拒绝覆盖" >&2; exit 2; }

set -a; . "$STATE/smoke.env"; set +a
AUTH_PER_MIN="$(grep -m1 '^AEGIS_RL_AUTH_PER_MIN=' "$STATE/gateway.env" | cut -d= -f2)"
[[ "$SMOKE_PG_CONTAINER" == aegis-postgres ]] || {
  echo "deploy/psql.sh 写死容器 aegis-postgres：起栈时设 PANDORA_SMOKE_PG_CONTAINER=aegis-postgres" >&2
  exit 2
}
# 与 e2e 脚本里写死的测试值一致（epay_e2e.sh 的默认值、uniproxy_e2e.sh 的签名串），不是真实商户
EPAY_TEST_PID=1001
EPAY_TEST_KEY=TESTKEY_e2e_20260725

# ---------------------------------------------------------------------------
# 搭出脚本声明要的环境
# ---------------------------------------------------------------------------
echo "==> 搭 /opt/aegispanel 布局与 deploy/.env"
pg_pw="$(python3 -c 'import sys, urllib.parse as u; print(u.unquote(u.urlsplit(sys.argv[1]).password or ""))' "$SMOKE_MIGRATION_DSN")"
[[ -n "$pg_pw" ]] || { echo "从 SMOKE_MIGRATION_DSN 取不到超级账号口令" >&2; exit 1; }
( umask 077
  { cat "$STATE/gateway.env"
    echo "POSTGRES_USER=aegis"
    echo "POSTGRES_PASSWORD=$pg_pw"
    echo "POSTGRES_DB=$SMOKE_PG_DB"
  } > "$PANEL_DIR/deploy/.env" )
sudo install -d -o "$(id -u)" -g "$(id -g)" /opt/aegispanel
mkdir -p /opt/aegispanel/bin /opt/aegispanel/logs
ln -s "$PANEL_DIR/deploy" /opt/aegispanel/deploy
ln -s "$STATE/logs/aegis-public.log" /opt/aegispanel/logs/public.log
ln -s "$STATE/logs/aegis-admin.log" /opt/aegispanel/logs/admin.log
ln -s "$STATE/logs/aegis-node.log" /opt/aegispanel/logs/node.log

RESULTS="$STATE/e2e-results.md"
: > "$RESULTS"
# 准备步骤里凡是跑产品代码的（编 agent、payctl 配渠道），失败只记一行，
# 让依赖它的脚本自己报出来：本步不因产品代码的问题变红
prep() {
  local what="$1"; shift
  if ! out="$("$@" 2>&1)"; then
    echo "    准备失败：$what"; printf '%s\n' "$out" | tail -20
    echo "| 准备：$what | 失败 | | | | $(printf '%s' "$out" | tail -1 | tr '|`' "/'" | cut -c1-300) |" >> "$RESULTS"
  fi
}
prep "编译 aegis-agent 与 aegis-payctl" \
  env -C "$PANEL_DIR" CGO_ENABLED=0 go build -mod=readonly -o /opt/aegispanel/bin/ ./cmd/aegis-agent ./cmd/aegis-payctl

PSQL=/opt/aegispanel/deploy/psql.sh
TENANT="$("$PSQL" -X -tAc 'SELECT id FROM tenants ORDER BY created_at LIMIT 1' | tr -d '[:space:]')"
[[ -n "$TENANT" ]] || { echo "冒烟库里没有租户" >&2; exit 1; }
echo "    租户 $TENANT，库 $SMOKE_PG_DB"

echo "==> aegis-payctl 配易支付测试渠道（商户 $EPAY_TEST_PID）"
payctl() {
  ( set -a; . "$STATE/gateway.env"; set +a
    /opt/aegispanel/bin/aegis-payctl upsert-epay --tenant "$TENANT" \
      --base-url http://127.0.0.1:9 --merchant "$EPAY_TEST_PID" --key "$EPAY_TEST_KEY" \
      --enable --allow-private-host )
}
prep "aegis-payctl 配易支付测试渠道" payctl

# ---------------------------------------------------------------------------
# 逐个跑。给每个脚本同一份环境：它们各自只读自己认得的变量
# ---------------------------------------------------------------------------
export PSQL
export ADM="$SMOKE_ADMIN_BASE" PUB="$SMOKE_PUBLIC_BASE" NODE="$SMOKE_NODE_BASE"
export BASE="$SMOKE_PUBLIC_BASE" AEGIS_BASE="$SMOKE_PUBLIC_BASE"
export ADMIN_EMAIL="$SMOKE_ADMIN_EMAIL" ADMIN_PASS="$SMOKE_ADMIN_PASSWORD"
export ADMIN_E2E_DISPOSABLE=YES_DELETE_FIXTURES ADMIN_E2E_DATABASE="$SMOKE_PG_DB" ADMIN_E2E_TENANT_ID="$TENANT"
export UNIPROXY_E2E_DISPOSABLE=YES_DELETE_FIXTURES UNIPROXY_E2E_DATABASE="$SMOKE_PG_DB" UNIPROXY_E2E_TENANT_ID="$TENANT"
export EPAY_KEY="$EPAY_TEST_KEY" EPAY_PID="$EPAY_TEST_PID"
# 冒烟栈把认证限流放宽到每分钟 $AUTH_PER_MIN 次，e2e.sh 要多探几次才碰得到 429
export RL_PROBE=$(( ${AUTH_PER_MIN:-14} + 10 ))

# e2e.sh 放最后：它的限流探测会把登录额度打满
SCRIPTS=(admin_e2e.sh epay_e2e.sh node_e2e.sh support_e2e.sh uniproxy_e2e.sh e2e.sh)
# 从一份输出里取：OK 数、FAIL 数、首个失败所在的步骤、首个失败原文（连同下一行细节）
summarize() {
  python3 - "$1" <<'PY'
import re, sys
text = open(sys.argv[1], encoding="utf-8", errors="replace").read()
lines = [re.sub(r"\x1b\[[0-9;]*m", "", l).rstrip() for l in text.splitlines()]
ok = fail = 0
section = "（开头）"
first = None
for i, l in enumerate(lines):
    s = l.strip()
    m = re.match(r"^=== (.+) ===$", s) or re.match(r"^▸ (.+)$", s)
    if m:
        section = m.group(1)
        continue
    if s.startswith("[ OK ]") or s.startswith("✓"):
        ok += 1
    elif s.startswith("[FAIL]") or s.startswith("✗") or s.startswith("[FATAL]"):
        fail += 1
        if first is None:
            detail = s
            nxt = lines[i + 1].strip() if i + 1 < len(lines) else ""
            if nxt and not re.match(r"^(\[ OK \]|\[FAIL\]|\[FATAL\]|✓|✗|=== |▸ )", nxt):
                detail += " — " + nxt
            first = (section, detail)
cell = lambda v: v.replace("|", "\\|").replace("`", "'")[:300]
sec, det = first if first else ("", "")
print(f"{ok}\t{fail}\t{cell(sec)}\t{cell(det)}")
PY
}

first_run=1
for f in "${SCRIPTS[@]}"; do
  name="${f%.sh}"
  log="$STATE/logs/e2e-$name.log"
  # 后台 IP 限流每分钟 240 次写死在代码里：两个脚本之间空出一个窗口，
  # 让每个脚本里出现的 429 只能是它自己打出来的
  [[ $first_run == 1 ]] || sleep 61
  first_run=0
  echo "==> $f"
  set +e
  ( cd "$PANEL_DIR" && timeout 600 bash "tests/$f" ) < /dev/null > "$log" 2>&1
  rc=$?
  set -e
  n_ok=0 n_fail=0 sec="" det=""
  IFS=$'\t' read -r n_ok n_fail sec det < <(summarize "$log") || true
  if [[ $rc -eq 124 ]]; then result="超时（600 秒）"
  elif [[ $rc -eq 0 && $n_fail -eq 0 ]]; then result="通过"
  elif [[ $rc -eq 0 ]]; then result="失败（退出码 0 但有 FAIL 行）"
  else result="失败（退出码 $rc）"
  fi
  echo "| \`$f\` | $result | $n_ok | $n_fail | $sec | $det |" >> "$RESULTS"
  echo "    $result；OK $n_ok，FAIL $n_fail${sec:+；首个失败在「$sec」}"
  echo "::group::$f 完整输出"; cat "$log"; echo "::endgroup::"
done

echo "==> 结果写进 $RESULTS（脚本失败只记录，不影响本步退出码）"

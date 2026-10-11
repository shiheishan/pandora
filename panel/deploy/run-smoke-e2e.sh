#!/usr/bin/env bash
# 这些 e2e 脚本是给「按 deploy/install.sh 直装在 /opt/pandora 的面板」写的：
# psql 走 /opt/pandora/deploy/psql.sh 或仓库的 deploy/psql.sh（读 deploy/.env，用主机的 psql
# 经 127.0.0.1:POSTGRES_PORT 以超级用户 postgres 连），公开网关日志在 systemd 单元写的
# /var/log/aegis/public.log。本机没有数据库，它们平时从没人跑。
#
# 这里只把它们声明要的环境原样搭出来，脚本一个字不改：
#   - /opt/pandora/deploy 链到仓库的 deploy/，deploy/.env 由网关配置加上冒烟栈的 datastore.env
#     （库与缓存那几项，与 install.sh 写的同名同义）拼成；库名带 test 段（admin / uniproxy 的一次性库守卫）；
#   - 公开网关日志链到单元的实际路径 /var/log/aegis/public.log（epay_e2e.sh 查密钥不落日志）；
#   - 易支付渠道用产品工具 aegis-payctl 配好（脚本里写死的测试商户 1001 与测试密钥）；
#   - 四个一次性库守卫（admin / uniproxy / expiry / risk）要的确认变量照实给出：冒烟库本来就是跑完即扔的。
# 这一步要往 /opt 与 /var/log 写东西、要写 deploy/.env，所以只肯在 GitHub Actions 的一次性 runner 上跑。
#
# 用法：run-smoke-e2e.sh <panel 源码目录> <状态目录>

set -euo pipefail

[[ $# -eq 2 ]] || { echo "用法: $0 <panel 源码目录> <状态目录>" >&2; exit 2; }
PANEL_DIR="$(cd "$1" && pwd)"
STATE="$(cd "$2" && pwd)"
[[ "${GITHUB_ACTIONS:-}" == true ]] || {
  echo "只在 GitHub Actions 的一次性 runner 上跑：会写 /opt/pandora 与 deploy/.env" >&2
  exit 2
}
[[ -f "$STATE/smoke.env" && -f "$STATE/gateway.env" && -f "$STATE/datastore.env" ]] \
  || { echo "状态目录里没有冒烟栈，先 run-smoke-stack.sh up" >&2; exit 2; }
[[ ! -e "$PANEL_DIR/deploy/.env" ]] || { echo "deploy/.env 已存在，拒绝覆盖" >&2; exit 2; }
[[ ! -e /opt/pandora ]] || { echo "/opt/pandora 已存在，拒绝覆盖" >&2; exit 2; }
# deploy/psql.sh 用主机的 psql（runner 镜像自带的 PostgreSQL 客户端；e2e 只跑普通 SQL，比服务端旧一个大版本也能用）
command -v psql >/dev/null || { echo "runner 上没有 psql：deploy/psql.sh 要主机的 PostgreSQL 客户端" >&2; exit 2; }
[[ ! -e /var/log/aegis ]] || { echo "/var/log/aegis 已存在，拒绝覆盖" >&2; exit 2; }

set -a; . "$STATE/smoke.env"; set +a
AUTH_PER_MIN="$(grep -m1 '^AEGIS_RL_AUTH_PER_MIN=' "$STATE/gateway.env" | cut -d= -f2)"
# 与 e2e 脚本里写死的测试值一致（epay_e2e.sh 的默认值、uniproxy_e2e.sh 的签名串），不是真实商户
EPAY_TEST_PID=1001
EPAY_TEST_KEY=TESTKEY_e2e_20260725

# ---------------------------------------------------------------------------
# 搭出脚本声明要的环境
# ---------------------------------------------------------------------------
echo "==> 搭 /opt/pandora 布局与 deploy/.env"
( umask 077
  cat "$STATE/gateway.env" "$STATE/datastore.env" > "$PANEL_DIR/deploy/.env" )
sudo install -d -o "$(id -u)" -g "$(id -g)" /opt/pandora
mkdir -p /opt/pandora/bin
ln -s "$PANEL_DIR/deploy" /opt/pandora/deploy
sudo install -d -o "$(id -u)" -g "$(id -g)" /var/log/aegis
ln -s "$STATE/logs/aegis-public.log" /var/log/aegis/public.log

RESULTS="$STATE/e2e-results.md"
: > "$RESULTS"
# 准备步骤里凡是跑产品代码的（编译 payctl 并用它配渠道），失败不中断，记一行并计入失败，
# 依赖它的脚本照样跑完、各自报出来；结果在最后统一决定退出码
failed=0
prep() {
  local what="$1"; shift
  if ! out="$("$@" 2>&1)"; then
    failed=$((failed + 1))
    echo "    准备失败：$what"; printf '%s\n' "$out" | tail -20
    echo "| 准备：$what | 失败 | | | | $(printf '%s' "$out" | tail -1 | tr '|`' "/'" | cut -c1-300) |" >> "$RESULTS"
  fi
}
prep "编译 aegis-payctl" \
  env -C "$PANEL_DIR" CGO_ENABLED=0 go build -mod=readonly -o /opt/pandora/bin/ ./cmd/aegis-payctl

PSQL=/opt/pandora/deploy/psql.sh
TENANT="$("$PSQL" -X -tAc 'SELECT id FROM tenants ORDER BY created_at LIMIT 1' | tr -d '[:space:]')"
[[ -n "$TENANT" ]] || { echo "冒烟库里没有租户" >&2; exit 1; }
echo "    租户 $TENANT，库 $SMOKE_PG_DB（psql $(psql --version | awk '{print $3}')）"

echo "==> aegis-payctl 配易支付测试渠道（商户 $EPAY_TEST_PID）"
payctl() {
  ( set -a; . "$STATE/gateway.env"; set +a
    /opt/pandora/bin/aegis-payctl upsert-epay --tenant "$TENANT" \
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
export RISK_E2E_DISPOSABLE=YES_DELETE_FIXTURES RISK_E2E_DATABASE="$SMOKE_PG_DB" RISK_E2E_TENANT_ID="$TENANT"
export EXPIRY_E2E_DISPOSABLE=YES_DELETE_FIXTURES EXPIRY_E2E_DATABASE="$SMOKE_PG_DB" EXPIRY_E2E_TENANT_ID="$TENANT"
export PASSWORD_RESET_E2E_DISPOSABLE=YES_DELETE_FIXTURES PORTAL_STAFF_E2E_DISPOSABLE=YES_DELETE_FIXTURES
export EPAY_KEY="$EPAY_TEST_KEY" EPAY_PID="$EPAY_TEST_PID"
# 冒烟栈把认证限流放宽到每分钟 $AUTH_PER_MIN 次，e2e.sh 要多探几次才碰得到 429
export RL_PROBE=$(( ${AUTH_PER_MIN:-14} + 10 ))

# e2e.sh 放最后：它的限流探测会把登录额度打满；risk_e2e.sh 要用模拟来源登录与注册，排在它前面。
# expiry_e2e.sh 要等 aegis-admin 的过期扫描循环（一分钟一轮）接手，最多等 3 分钟。
# 找回密码（临时配 SMTP，退出时恢复）与门户禁登管理员排在 risk 之前，都要登录与注册
SCRIPTS=(admin_e2e.sh epay_e2e.sh support_e2e.sh uniproxy_e2e.sh expiry_e2e.sh password_reset_e2e.sh portal_staff_e2e.sh risk_e2e.sh e2e.sh)
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
  [[ $result == 通过 ]] || failed=$((failed + 1))
  echo "| \`$f\` | $result | $n_ok | $n_fail | $sec | $det |" >> "$RESULTS"
  echo "    $result；OK $n_ok，FAIL $n_fail${sec:+；首个失败在「$sec」}"
  echo "::group::$f 完整输出"; cat "$log"; echo "::endgroup::"
done

echo "==> 结果写进 $RESULTS"
# 全部跑完、表格写完才决定退出码：一个脚本红了，其余脚本的结果照样在表里
if [[ $failed -gt 0 ]]; then
  echo "::error::e2e 脚本或准备步骤有 $failed 项失败，见 job summary 的 e2e 表"
  exit 1
fi

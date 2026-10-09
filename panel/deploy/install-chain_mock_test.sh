#!/usr/bin/env bash
# install.sh 安装链的桩测试：不需要 root、nginx 或数据库。
#
# source install-lib.sh 取函数，逐个验证：升级时要不要接管 nginx 边缘、三个网关的健康检查；
# 再静态核对：HTTPS 边缘交给 edge-tls.sh 且在健康检查之后、边缘的每种结果都映射到收尾提示、
# 头注释写全无人值守的环境变量、备份目录三处一致、收敛失败即停不兜底 GRANT、升级前必备份。
# 证书、nginx 渲染与回滚、防火墙、默认站点的行为在 edge-tls_mock_test.sh。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
INST="$DEPLOY/install.sh"
LIB="$DEPLOY/install-lib.sh"
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-install-chain.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'install-chain: %s\n' "$*" >&2; exit 1; }
refute() { if grep "$@"; then fail "unexpected match: $*"; fi; }

mkdir -p "$T/nginx/conf.d" "$T/bin"

# shellcheck source=public-base-url.sh
. "$DEPLOY/public-base-url.sh"
# shellcheck source=install-lib.sh
. "$LIB"
set -euo pipefail
declare -F pandora_edge_wanted native_gateways_healthy print_install_summary >/dev/null || fail 'install-lib.sh did not define the step functions'
# 证书与 nginx 的步骤归 edge-tls.sh，安装器不另留一份
for gone in obtain_certificate apply_edge_config open_firewall disable_stock_default_site; do
  if grep -Eq "^[[:space:]]*$gone\(\)" "$INST" "$LIB"; then fail "the installer still defines $gone (moved to edge-tls.sh)"; fi
done

# --- 升级时要不要接管 nginx 边缘 ---------------------------------------------------
unset PANDORA_ACME PANDORA_CERTBOT
pandora_edge_wanted install "$T/nginx/conf.d/aegis.conf" </dev/null || fail 'first install must set up the edge'
if pandora_edge_wanted upgrade "$T/nginx/conf.d/aegis.conf" </dev/null; then fail 'upgrade took over nginx without consent'; fi
if PANDORA_ASSUME_YES=1 pandora_edge_wanted upgrade "$T/nginx/conf.d/aegis.conf"; then fail 'unattended upgrade took over nginx'; fi
PANDORA_ACME=1 pandora_edge_wanted upgrade "$T/nginx/conf.d/aegis.conf" </dev/null || fail 'PANDORA_ACME=1 ignored'
PANDORA_CERTBOT=1 pandora_edge_wanted upgrade "$T/nginx/conf.d/aegis.conf" </dev/null || fail 'legacy PANDORA_CERTBOT=1 ignored'
touch "$T/nginx/conf.d/aegis.conf"
pandora_edge_wanted upgrade "$T/nginx/conf.d/aegis.conf" </dev/null || fail 'an existing edge was not re-rendered on upgrade'

# --- 三个网关的健康检查：都回 200 才算过，最多等一分钟 ----------------------------------
# curl 桩：healthz.<端口> 里写着这个端口回什么码，没有就 000；sleep 不真睡
cat >"$T/bin/curl" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
url="${!#}"; port="${url#http://127.0.0.1:}"; port="${port%%/*}"
echo x >>"$root/curl.calls"
cat "$root/healthz.$port" 2>/dev/null || printf '000'
MOCK
printf '#!/usr/bin/env bash\nexit 0\n' >"$T/bin/sleep"
chmod 0755 "$T/bin/"*
for p in 9000 9001 9003; do printf 200 >"$T/healthz.$p"; done
( PATH="$T/bin:$PATH"; native_gateways_healthy ) || fail 'three healthy gateways were not accepted'
printf 503 >"$T/healthz.9001"; : >"$T/curl.calls"
if ( PATH="$T/bin:$PATH"; native_gateways_healthy ); then fail 'an unhealthy admin gateway was accepted'; fi
[ "$(grep -c . "$T/curl.calls")" -eq 90 ] || fail "health check did not keep trying for its full budget: $(grep -c . "$T/curl.calls") probes"

# --- 静态：主流程的顺序与红线 ------------------------------------------------------
line() { grep -nF "$1" "$INST" | head -1 | cut -d: -f1; }
# HTTPS 边缘：install.sh 拷 edge-tls.sh、续期单元随 UNITS 原样装上、按条件调 setup，在健康检查之后
grep -Fq '"$SCRIPT_DIR/edge-tls.sh"' "$INST" || fail 'install.sh does not copy edge-tls.sh'
for u in aegis-tls-renew.service aegis-tls-renew.timer; do
  grep -Eq "^UNITS=\(.*\b$u\b|^  .*\b$u\b" "$LIB" || fail "UNITS does not install $u"
done
grep -Fq 'bash "$INSTALL_DIR/deploy/edge-tls.sh" setup "$ENV_FILE"' "$INST" || fail 'install.sh does not run edge setup'
grep -Fq 'pandora_edge_wanted "$MODE" /etc/nginx/conf.d/aegis.conf' "$INST" || fail 'install.sh does not gate the edge on upgrade'
[ "$(line 'bash "$INSTALL_DIR/deploy/edge-tls.sh" setup "$ENV_FILE"')" -gt "$(line 'native_gateways_healthy || HEALTH_OK=0')" ] \
  || fail 'edge setup is not after the health check'
# edge-tls.sh 的退出码：0 正规证书、3 自签兜底（不算失败）、其余记为没配好（nginx 保持原配置，收尾给补救命令）
grep -Fq '0) EDGE_STATE=trusted ;;' "$INST" && grep -Fq '3) EDGE_STATE=selfsigned ;;' "$INST" && grep -Fq '*) EDGE_STATE=failed ;;' "$INST" \
  || fail 'install.sh does not map the edge exit codes'
# 无人值守的环境变量写在头注释里（到 set -euo pipefail 为止）
header="$(awk '/^set -euo pipefail$/ { exit } { print }' "$INST")"
for v in PANDORA_PUBLIC_BASE_URL PANDORA_ACME=0 PANDORA_ACME=1 PANDORA_ACME_EMAIL PANDORA_ACME_SERVER PANDORA_SKIP_NGINX PANDORA_ASSUME_YES PANDORA_SYSTEMD_HARDENING PANDORA_NONINTERACTIVE; do
  grep -Fq "$v" <<<"$header" || fail "install.sh header does not document $v"
done
# 只有一个入口：不再分布局、不再有 --from-docker
# （valkey-hardening_versions_docker_test.sh 是用 Docker 镜像跑版本矩阵的测试，提到它的名字不算）
for gone in PANDORA_LAYOUT from-docker install-native docker; do
  if grep -ni -- "$gone" "$INST" "$LIB" | grep -v '_docker_test\.sh' | grep -q .; then
    fail "the installer still mentions $gone: $(grep -ni -- "$gone" "$INST" "$LIB" | grep -v '_docker_test\.sh' | head -3)"
  fi
done
# 加密备份目录三处一致：install-lib.sh、edge-tls.sh 的缺省、备份单元的 ReadWritePaths
lib_dir="$(sed -n 's/^NATIVE_BACKUP_DIR=//p' "$LIB")"
edge_dir="$(sed -n 's/^BACKUP_DIR="\${PANDORA_BACKUP_DIR:-\(.*\)}"$/\1/p' "$DEPLOY/edge-tls.sh")"
unit_dir="$(sed -n 's/^ReadWritePaths=\([^ ]*\).*/\1/p' "$DEPLOY/systemd/aegis-backup.service")"
[ -n "$lib_dir" ] && [ "$lib_dir" = "$edge_dir" ] && [ "$lib_dir" = "$unit_dir" ] \
  || fail "backup directory differs: install-lib.sh '$lib_dir', edge-tls.sh '$edge_dir', aegis-backup.service '$unit_dir'"

# 三个网关的停机都是「等在途请求 + join 后台循环」两段，各限 AEGIS_SHUTDOWN_TIMEOUT（缺省 20 秒）：
# TimeoutStopSec 一律 45 秒（5k-r4：aegis-node 还停在 30 秒）
for unit in aegis-public aegis-admin aegis-node; do
  grep -qx 'TimeoutStopSec=45' "$DEPLOY/systemd/$unit.service" || fail "$unit.service must stop within TimeoutStopSec=45"
done

# 收敛失败即停，不再兜底 GRANT；只有全新库才跳过预检；升级前 pg_dump 在迁移之前
refute -Eq 'GRANT[^;]*ON ALL TABLES' "$INST"
refute -q '^export PANDORA_SKIP_PRECHECK_FRESH_DB' "$INST"
grep -Fq 't) FRESH_DB=yes ;;' "$INST" || fail 'precheck skip is not tied to a fresh database'
grep -Fq 'pg_dump -Fc -d aegis' "$INST" || fail 'install.sh has no pre-upgrade dump'
awk '/pg_dump -Fc -d aegis/ { dump = NR } /pandora_run_migrations "\$MODE"/ { mig = NR } END { exit !(dump && mig && dump < mig) }' "$INST" \
  || fail 'pre-upgrade dump does not come before the migration'
# 迁移 DSN（带超级用户口令）不经 env(1) 传给 migrate.sh：它从 .env 读
if grep -v '^[[:space:]]*#' "$LIB" | grep -q 'AEGIS_MIGRATION_DATABASE_URL='; then
  fail 'pandora_run_migrations passes the superuser DSN on a command line'
fi

printf 'install-chain mock: PASS\n'

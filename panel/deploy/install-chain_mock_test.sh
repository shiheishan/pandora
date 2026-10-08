#!/usr/bin/env bash
# install.sh 安装链的桩测试：不需要 root、Docker、nginx 或数据库。
#
# install.sh 以 PANDORA_INSTALL_LIB=1 被 source 时只定义步骤函数、不执行安装；这里逐个验证：
#   CHANGE_ME 计数不算注释、连接串换成 unix socket、升级时要不要接管 nginx 边缘；
# 再静态核对升级前必备份、HTTPS 边缘交给 edge-tls.sh 且在健康检查之后、install-native.sh 同步、
# install-native.sh 不再兜底 GRANT、compose 与冒烟栈的 socket 挂载。
# 证书、nginx 渲染与回滚、防火墙、默认站点的行为在 edge-tls_mock_test.sh。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'install-chain: %s\n' "$*" >&2; exit 1; }
refute() { if grep "$@"; then fail "unexpected match: $*"; fi; }

export PANDORA_NGINX_DIR="$T/nginx" PANDORA_BACKUP_DIR="$T/backups"
mkdir -p "$T/nginx/conf.d"

# shellcheck source=public-base-url.sh
. "$DEPLOY/public-base-url.sh"
# shellcheck source=install-lib.sh
. "$DEPLOY/install-lib.sh"
# shellcheck source=install.sh
PANDORA_INSTALL_LIB=1 . "$DEPLOY/install.sh"
set +E; trap 'rm -rf -- "$T"' EXIT
declare -F switch_env_to_sockets pandora_edge_wanted print_install_summary >/dev/null || fail 'library mode did not define the step functions'
for gone in obtain_certificate apply_edge_config open_firewall disable_stock_default_site; do
  if declare -F "$gone" >/dev/null; then fail "install.sh still defines $gone (moved to edge-tls.sh)"; fi
done

# --- ① CHANGE_ME 只数「键=值」行 ------------------------------------------------
grep -q '^#.*CHANGE_ME' "$DEPLOY/.env.example" || fail '.env.example no longer mentions CHANGE_ME in a comment (test premise)'
sed -E '/^[A-Za-z_][A-Za-z0-9_]*=/ s/CHANGE_ME[A-Za-z_]*/filled/g' "$DEPLOY/.env.example" >"$T/filled.env"
[ -z "$(pending_env_lines "$T/filled.env")" ] || fail "comment lines counted as pending: $(pending_env_lines "$T/filled.env")"
sed -i.bak -E 's/^AEGIS_MASTER_KEY=.*/AEGIS_MASTER_KEY=CHANGE_ME/' "$T/filled.env"
[ "$(pending_env_lines "$T/filled.env" | wc -l | tr -d ' ')" = 1 ] || fail 'a real pending key was not counted'
pending_env_lines "$T/filled.env" | grep -q 'AEGIS_MASTER_KEY=' || fail 'pending line does not name the key'

# --- 连接串换成 unix socket ------------------------------------------------------
pw_app='Abc_def-0123456789abcdefghijklmnopqrstuv'
pw_vk='Vk_0123456789-abcdefghijklmnopqrstuvwxyz'
cat >"$T/sock.env" <<EOF
POSTGRES_PASSWORD=super
AEGIS_MIGRATION_DATABASE_URL=postgres://aegis:super@127.0.0.1:5433/aegis?sslmode=disable
AEGIS_DATABASE_URL=postgres://aegis_app:$pw_app@127.0.0.1:5433/aegis?sslmode=disable
AEGIS_REDIS_URL=redis://:$pw_vk@127.0.0.1:6380/0
EOF
chmod 0644 "$T/sock.env"
out="$(switch_env_to_sockets "$T/sock.env" /opt/aegispanel/deploy/run/postgresql /opt/aegispanel/deploy/run/valkey/valkey.sock)"
[ "$out" = $'AEGIS_DATABASE_URL socket\nAEGIS_REDIS_URL socket' ] || fail "unexpected forms: $out"
[ "$(pandora_env_file_value "$T/sock.env" AEGIS_DATABASE_URL)" = "postgres://aegis_app:$pw_app@/aegis?host=/opt/aegispanel/deploy/run/postgresql" ] \
  || fail "database URL: $(pandora_env_file_value "$T/sock.env" AEGIS_DATABASE_URL)"
[ "$(pandora_env_file_value "$T/sock.env" AEGIS_REDIS_URL)" = "unix://:$pw_vk@/opt/aegispanel/deploy/run/valkey/valkey.sock?db=0" ] \
  || fail "redis URL: $(pandora_env_file_value "$T/sock.env" AEGIS_REDIS_URL)"
# 迁移连接串不动；.env 收紧到 0600；回退说明只写一份且不带口令
[ "$(pandora_env_file_value "$T/sock.env" AEGIS_MIGRATION_DATABASE_URL)" = 'postgres://aegis:super@127.0.0.1:5433/aegis?sslmode=disable' ] || fail 'migration DSN changed'
[ "$(stat -c %a "$T/sock.env" 2>/dev/null || stat -f %Lp "$T/sock.env")" = 600 ] || fail '.env is not 0600 after rewrite'
[ "$(grep -cFx "$SOCKET_ROLLBACK_NOTE" "$T/sock.env")" = 1 ] || fail 'rollback note missing'
if grep '^#' "$T/sock.env" | grep -q -e "$pw_app" -e "$pw_vk"; then fail 'rollback note leaks a password'; fi
# 新形式能被 shell 安全地 source（bootstrap.sh、psql.sh、备份脚本都这么读）
( set -a; . "$T/sock.env"; set +a
  [ "$AEGIS_DATABASE_URL" = "postgres://aegis_app:$pw_app@/aegis?host=/opt/aegispanel/deploy/run/postgresql" ] ) || fail 'sourced value differs'
# 幂等：再跑一遍文件一字不变
cp "$T/sock.env" "$T/sock.before"
switch_env_to_sockets "$T/sock.env" /opt/aegispanel/deploy/run/postgresql /opt/aegispanel/deploy/run/valkey/valkey.sock >/dev/null
cmp -s "$T/sock.env" "$T/sock.before" || fail 'second run changed the file'
# 手工改过的值不碰
printf 'AEGIS_DATABASE_URL=postgres://aegis_app:x@db.internal:5432/aegis?sslmode=require\nAEGIS_REDIS_URL=redis://:y@127.0.0.1:6390/1\n' >"$T/custom.env"
cp "$T/custom.env" "$T/custom.before"
out="$(switch_env_to_sockets "$T/custom.env" /opt/aegispanel/deploy/run/postgresql /opt/aegispanel/deploy/run/valkey/valkey.sock)"
[ "$out" = $'AEGIS_DATABASE_URL custom\nAEGIS_REDIS_URL custom' ] || fail "custom forms: $out"
cmp -s "$T/custom.env" "$T/custom.before" || fail 'custom values were rewritten'
# 不合规的 socket 路径拒绝
if switch_env_to_sockets "$T/custom.env" 'relative/dir' /x.sock >/dev/null 2>&1; then fail 'relative socket dir accepted'; fi
[ "$(conn_form 'postgres://aegis_app:a@127.0.0.1:5433/aegis?sslmode=disable')" = tcp ] || fail 'conn_form tcp'

# --- 升级时要不要接管 nginx 边缘 ---------------------------------------------------
unset PANDORA_ACME PANDORA_CERTBOT
pandora_edge_wanted install "$T/nginx/conf.d/aegis.conf" </dev/null || fail 'first install must set up the edge'
if pandora_edge_wanted upgrade "$T/nginx/conf.d/aegis.conf" </dev/null; then fail 'upgrade took over nginx without consent'; fi
if PANDORA_ASSUME_YES=1 pandora_edge_wanted upgrade "$T/nginx/conf.d/aegis.conf"; then fail 'unattended upgrade took over nginx'; fi
PANDORA_ACME=1 pandora_edge_wanted upgrade "$T/nginx/conf.d/aegis.conf" </dev/null || fail 'PANDORA_ACME=1 ignored'
PANDORA_CERTBOT=1 pandora_edge_wanted upgrade "$T/nginx/conf.d/aegis.conf" </dev/null || fail 'legacy PANDORA_CERTBOT=1 ignored'
touch "$T/nginx/conf.d/aegis.conf"
pandora_edge_wanted upgrade "$T/nginx/conf.d/aegis.conf" </dev/null || fail 'an existing edge was not re-rendered on upgrade'

# --- 静态：主流程的顺序与红线 ------------------------------------------------------
inst="$DEPLOY/install.sh"
# 升级前必备份：不再有「跳过备份」，导出后核对目录
refute -q '跳过备份' "$inst"
grep -Fq 'pg_restore --list /tmp/pre-upgrade.dump' "$inst" || fail 'install.sh does not verify the pre-upgrade dump'
# socket 改写在迁移之前、边缘配置在健康检查之后
line() { grep -nF "$1" "$inst" | head -1 | cut -d: -f1; }
[ "$(line '"$DEST/deploy/.env" "$PG_SOCKET_DIR" "$VK_SOCKET"')" -lt "$(line 'pandora_run_migrations "$MODE"')" ] || fail 'socket switch is not before migrations'
[ "$(line 'bash "$DEST/deploy/edge-tls.sh" setup "$DEST/deploy/.env"')" -gt "$(line '服务起来了但健康检查没通过')" ] || fail 'edge setup is not after the health check'
# edge-tls.sh 的退出码：0 正规证书、3 自签兜底（不算失败）、其余停下
grep -Fq '3) EDGE_STATE=selfsigned ;;' "$inst" && grep -Fq '0) EDGE_STATE=trusted ;;' "$inst" || fail 'install.sh does not map the edge exit codes'
grep -Fq 'pandora_edge_wanted "$MODE" "$NGINX_DIR/conf.d/aegis.conf"' "$inst" || fail 'install.sh does not gate the edge on upgrade'
# 无人值守的环境变量写在头注释里
for v in PANDORA_PUBLIC_BASE_URL PANDORA_ACME=0 PANDORA_ACME=1 PANDORA_ACME_EMAIL PANDORA_ACME_SERVER PANDORA_SKIP_NGINX PANDORA_ASSUME_YES; do
  sed -n '1,40p' "$inst" | grep -Fq "$v" || fail "install.sh header does not document $v"
done
# install-native.sh 同一条 HTTPS 边缘：拷 edge-tls.sh、装续期单元（换安装目录）、按同样的条件调 setup
native_edge="$DEPLOY/install-native.sh"
grep -Fq '"$SCRIPT_DIR/edge-tls.sh"' "$native_edge" || fail 'install-native.sh does not copy edge-tls.sh'
grep -Fq 'for u in aegis-tls-renew.service aegis-tls-renew.timer; do' "$native_edge" || fail 'install-native.sh does not install the renewal units'
grep -Fq 'bash "$INSTALL_DIR/deploy/edge-tls.sh" setup "$ENV_FILE"' "$native_edge" || fail 'install-native.sh does not run edge setup'
grep -Fq 'pandora_edge_wanted "$MODE"' "$native_edge" || fail 'install-native.sh does not gate the edge on upgrade'
# install.sh 认的 socket 位置与 compose 的挂载一致；冒烟栈用同样的容器内路径
compose="$DEPLOY/docker-compose.yml"
grep -Fq -- '- ./run/postgresql:/var/run/postgresql' "$compose" || fail 'compose does not expose the PG socket under run/'
grep -Fq -- '- ./run/valkey:/data/sock' "$compose" || fail 'compose does not expose the Valkey socket under run/'
grep -Fq -- '- /data/sock/valkey.sock' "$compose" || fail 'Valkey does not listen on /data/sock/valkey.sock'
# valkey.sock 只给属主（容器里的 valkey）：容器里 valkey 组的 gid 在宿主上常是第一个普通用户的组
# （Vultr 的 linuxuser 是 1000），770 等于让那个宿主用户连得上；网关以 root 运行，不靠组权限
awk '/--unixsocketperm/ { getline; print; exit }' "$compose" | grep -Fq '"700"' || fail 'valkey.sock must be 700, not group-accessible'
refute -Eq -- '--unixsocketperm[[:space:]]+"?7[1-7]' "$compose"
grep -Fq 'PG_SOCKET_DIR="$DEST/deploy/run/postgresql"' "$inst" && grep -Fq 'VK_SOCKET="$DEST/deploy/run/valkey/valkey.sock"' "$inst" \
  || fail 'install.sh socket paths drifted from compose'
grep -Fq ':/var/run/postgresql"' "$DEPLOY/run-smoke-stack.sh" && grep -Fq -- '--unixsocket /data/sock/valkey.sock' "$DEPLOY/run-smoke-stack.sh" \
  || fail 'smoke stack does not exercise the socket mounts'
grep -Fq 'AEGIS_REDIS_URL=unix://' "$DEPLOY/run-smoke-stack.sh" && grep -Fq '?host=${SOCK_PG_DIR}' "$DEPLOY/run-smoke-stack.sh" \
  || fail 'smoke gateways do not connect over the sockets'

# 三个网关的停机都是「等在途请求 + join 后台循环」两段，各限 AEGIS_SHUTDOWN_TIMEOUT（缺省 20 秒）：
# TimeoutStopSec 一律 45 秒（5k-r4：aegis-node 还停在 30 秒）
for unit in aegis-public aegis-admin aegis-node; do
  grep -qx 'TimeoutStopSec=45' "$DEPLOY/systemd/$unit.service" || fail "$unit.service must stop within TimeoutStopSec=45"
done

# install-native.sh：收敛失败即停，不再兜底 GRANT；只有全新库才跳过预检；升级前 pg_dump
native="$DEPLOY/install-native.sh"
refute -Eq 'GRANT[^;]*ON ALL TABLES' "$native"
refute -q '^export PANDORA_SKIP_PRECHECK_FRESH_DB' "$native"
grep -Fq 't) FRESH_DB=yes ;;' "$native" || fail 'native precheck skip is not tied to a fresh database'
grep -Fq 'pg_dump -Fc -d aegis' "$native" || fail 'install-native.sh has no pre-upgrade dump'
awk '/pg_dump -Fc -d aegis/ { dump = NR } /pandora_run_migrations "\$MODE"/ { mig = NR } END { exit !(dump && mig && dump < mig) }' "$native" \
  || fail 'native dump does not come before the migration'

printf 'install-chain mock: PASS\n'

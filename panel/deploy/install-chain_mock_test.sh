#!/usr/bin/env bash
# install.sh 安装链的桩测试：不需要 root、Docker、nginx 或数据库。
#
# install.sh 以 PANDORA_INSTALL_LIB=1 被 source 时只定义步骤函数、不执行安装；这里把
# nginx / systemctl / certbot / ufw 换成记账的桩，路径都指到临时目录，逐个验证：
#   CHANGE_ME 计数不算注释、连接串换成 unix socket、ufw 放行、停用发行版默认站点、
#   certbot webroot 申请、渲染 + nginx -t + reload 与失败回滚；
# 再静态核对升级前必备份、install-native.sh 不再兜底 GRANT、compose 与冒烟栈的 socket 挂载。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'install-chain: %s\n' "$*" >&2; exit 1; }
refute() { if grep "$@"; then fail "unexpected match: $*"; fi; }

# --- 桩：每次调用记一行到 calls.log ---------------------------------------------
mkdir -p "$T/bin"
stub() {
  local name="$1" body="${2:-}"
  printf '#!/usr/bin/env bash\nprintf "%%s %%s\\n" "%s" "$*" >>"%s/calls.log"\n%s\n' "$name" "$T" "$body" >"$T/bin/$name"
  chmod +x "$T/bin/$name"
}
stub systemctl 'exit 0'
stub ufw 'if [ "${1:-}" = status ]; then echo "Status: ${UFW_STATE:-active}"; fi; exit 0'
stub nginx 'if [ "${NGINX_T_FAIL:-}" = 1 ]; then echo "nginx: [emerg] stub failure" >&2; exit 1; fi; exit 0'
# certbot 桩：成功时按 -d 写出证书文件（模拟 /etc/letsencrypt/live/<域名>/）
stub certbot '
[ "${CERTBOT_FAIL:-}" = 1 ] && exit 1
while [ $# -gt 0 ]; do [ "$1" = -d ] && d="$2"; shift; done
mkdir -p "$PANDORA_LE_LIVE_DIR/$d"; echo cert >"$PANDORA_LE_LIVE_DIR/$d/fullchain.pem"; echo key >"$PANDORA_LE_LIVE_DIR/$d/privkey.pem"'
export PATH="$T/bin:$PATH"
calls() { cat "$T/calls.log" 2>/dev/null || true; }
reset_calls() { : >"$T/calls.log"; }

export PANDORA_NGINX_DIR="$T/nginx" PANDORA_LE_LIVE_DIR="$T/le" PANDORA_ACME_WEBROOT="$T/acme"
export PANDORA_REALIP_FILE="$T/realip.conf" PANDORA_BACKUP_DIR="$T/backups" PANDORA_NGINX_VERSION=1.26.3
mkdir -p "$T/nginx/conf.d" "$T/nginx/sites-available" "$T/nginx/sites-enabled"

# shellcheck source=public-base-url.sh
. "$DEPLOY/public-base-url.sh"
# shellcheck source=install.sh
PANDORA_INSTALL_LIB=1 . "$DEPLOY/install.sh"
set +E; trap 'rm -rf -- "$T"' EXIT
declare -F switch_env_to_sockets apply_edge_config obtain_certificate >/dev/null || fail 'library mode did not define the step functions'

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

# --- ④ ufw：开着才放行 80/443 -----------------------------------------------------
reset_calls; UFW_STATE=active open_firewall >/dev/null
calls | grep -qx 'ufw allow 80/tcp' && calls | grep -qx 'ufw allow 443/tcp' || fail "ufw active: $(calls)"
reset_calls; UFW_STATE=inactive open_firewall >/dev/null
refute -q 'allow' "$T/calls.log"

# --- ② 默认站点：只停用发行版原样的链接 -------------------------------------------
printf 'server {\n    listen 80 default_server;\n    listen [::]:80 default_server;\n}\n' >"$T/nginx/sites-available/default"
ln -s "$T/nginx/sites-available/default" "$T/nginx/sites-enabled/default"
disable_stock_default_site >/dev/null
[ ! -e "$T/nginx/sites-enabled/default" ] || fail 'stock default site still enabled'
[ -f "$T/nginx/sites-available/default" ] || fail 'stock default site file was deleted'
printf 'server { listen 80 default_server; }\n' >"$T/nginx/sites-available/mine"
ln -s "$T/nginx/sites-available/mine" "$T/nginx/sites-enabled/default"
disable_stock_default_site >/dev/null
[ -L "$T/nginx/sites-enabled/default" ] || fail 'a non-stock link named default was removed'
rm -f "$T/nginx/sites-enabled/default"

# --- ② 证书：已有不申请；没同意不申请；同意后走 webroot ------------------------------
domain=panel.example.test
reset_calls
rc=0; PANDORA_ASSUME_YES=1 obtain_certificate "$domain" </dev/null >/dev/null 2>&1 || rc=$?
[ "$rc" = 2 ] || fail "no consent should return 2, got $rc"
refute -q '^certbot' "$T/calls.log"
reset_calls
rc=0; PANDORA_CERTBOT=1 obtain_certificate "$domain" >/dev/null 2>&1 || rc=$?
[ "$rc" = 0 ] || fail "certbot flow returned $rc: $(calls)"
calls | grep -q "^certbot certonly --webroot -w $T/acme -d $domain --non-interactive --agree-tos --register-unsafely-without-email$" \
  || fail "certbot arguments: $(calls)"
calls | grep -qx 'nginx -t' || fail 'nginx -t not run before certbot'
[ ! -e "$T/nginx/conf.d/aegis-acme.conf" ] || fail 'temporary ACME site left behind'
reset_calls
rc=0; obtain_certificate "$domain" >/dev/null 2>&1 || rc=$?
[ "$rc" = 0 ] && ! calls | grep -q '^certbot' || fail 'existing certificate was requested again'
rm -rf "$T/le"
reset_calls
rc=0; PANDORA_CERTBOT=1 PANDORA_CERTBOT_EMAIL=ops@example.test CERTBOT_FAIL=1 obtain_certificate "$domain" >/dev/null 2>&1 || rc=$?
[ "$rc" = 1 ] || fail "failed certbot should return 1, got $rc"
calls | grep -q -- '--email ops@example.test' || fail 'email not passed to certbot'
[ ! -e "$T/nginx/conf.d/aegis-acme.conf" ] || fail 'temporary ACME site left behind after failure'

# --- ③ 渲染 → nginx -t → reload；失败换回原配置 -----------------------------------
printf 'AEGIS_ADMIN_PATH=ops_0123456789abcdef0123456789abcdef\nAEGIS_PUBLIC_BASE_URL=https://%s\n' "$domain" >"$T/edge.env"
reset_calls
apply_edge_config "$DEPLOY/render-nginx.sh" "$T/edge.env" >/dev/null || fail "apply failed: $(calls)"
grep -Fq "server_name $domain;" "$T/nginx/conf.d/aegis.conf" || fail 'aegis.conf not rendered'
grep -Eq '^[[:space:]]*http2 on;' "$T/nginx/conf.d/aegis.conf" || fail 'rendered without http2 on'
calls | grep -qx 'nginx -t' && calls | grep -qx 'systemctl reload nginx' || fail "no nginx -t + reload: $(calls)"
# 已有配置：先备份；nginx -t 失败就一字不差地换回去，不 reload
echo '# previous good config' >"$T/nginx/conf.d/aegis.conf"
reset_calls
if NGINX_T_FAIL=1 apply_edge_config "$DEPLOY/render-nginx.sh" "$T/edge.env" >/dev/null 2>&1; then fail 'nginx -t failure not reported'; fi
[ "$(cat "$T/nginx/conf.d/aegis.conf")" = '# previous good config' ] || fail 'previous config not restored'
refute -q 'reload' "$T/calls.log"
ls "$T/backups"/nginx-aegis.conf.* >/dev/null 2>&1 || fail 'previous config was not backed up'
# 原来没有配置、nginx -t 失败：新文件删掉
rm -f "$T/nginx/conf.d/aegis.conf"
if NGINX_T_FAIL=1 apply_edge_config "$DEPLOY/render-nginx.sh" "$T/edge.env" >/dev/null 2>&1; then fail 'nginx -t failure not reported'; fi
[ ! -e "$T/nginx/conf.d/aegis.conf" ] || fail 'failed render left a new aegis.conf'
# 渲染器拒绝（例如 .env 缺后台前缀）：不动 nginx
echo '# keep me' >"$T/nginx/conf.d/aegis.conf"
printf 'AEGIS_PUBLIC_BASE_URL=https://%s\n' "$domain" >"$T/bad.env"
reset_calls
if apply_edge_config "$DEPLOY/render-nginx.sh" "$T/bad.env" >/dev/null 2>&1; then fail 'render refusal not reported'; fi
[ "$(cat "$T/nginx/conf.d/aegis.conf")" = '# keep me' ] && ! calls | grep -q '^nginx' || fail 'nginx touched after render refusal'

# --- 静态：主流程的顺序与红线 ------------------------------------------------------
inst="$DEPLOY/install.sh"
# 升级前必备份：不再有「跳过备份」，导出后核对目录
refute -q '跳过备份' "$inst"
grep -Fq 'pg_restore --list /tmp/pre-upgrade.dump' "$inst" || fail 'install.sh does not verify the pre-upgrade dump'
# socket 改写在迁移之前、边缘配置在健康检查之后
line() { grep -nF "$1" "$inst" | head -1 | cut -d: -f1; }
[ "$(line '"$DEST/deploy/.env" "$PG_SOCKET_DIR" "$VK_SOCKET"')" -lt "$(line 'bash ./migrate.sh up')" ] || fail 'socket switch is not before migrations'
[ "$(line 'apply_edge_config "$DEST/deploy/render-nginx.sh"')" -gt "$(line '服务起来了但健康检查没通过')" ] || fail 'edge config is not after the health check'
# install.sh 认的 socket 位置与 compose 的挂载一致；冒烟栈用同样的容器内路径
compose="$DEPLOY/docker-compose.yml"
grep -Fq -- '- ./run/postgresql:/var/run/postgresql' "$compose" || fail 'compose does not expose the PG socket under run/'
grep -Fq -- '- ./run/valkey:/data/sock' "$compose" || fail 'compose does not expose the Valkey socket under run/'
grep -Fq -- '- /data/sock/valkey.sock' "$compose" || fail 'Valkey does not listen on /data/sock/valkey.sock'
grep -Fq 'PG_SOCKET_DIR="$DEST/deploy/run/postgresql"' "$inst" && grep -Fq 'VK_SOCKET="$DEST/deploy/run/valkey/valkey.sock"' "$inst" \
  || fail 'install.sh socket paths drifted from compose'
grep -Fq ':/var/run/postgresql"' "$DEPLOY/run-smoke-stack.sh" && grep -Fq -- '--unixsocket /data/sock/valkey.sock' "$DEPLOY/run-smoke-stack.sh" \
  || fail 'smoke stack does not exercise the socket mounts'
grep -Fq 'AEGIS_REDIS_URL=unix://' "$DEPLOY/run-smoke-stack.sh" && grep -Fq '?host=${SOCK_PG_DIR}' "$DEPLOY/run-smoke-stack.sh" \
  || fail 'smoke gateways do not connect over the sockets'

# install-native.sh：收敛失败即停，不再兜底 GRANT；只有全新库才跳过预检；升级前 pg_dump
native="$DEPLOY/install-native.sh"
refute -Eq 'GRANT[^;]*ON ALL TABLES' "$native"
refute -q '^export PANDORA_SKIP_PRECHECK_FRESH_DB' "$native"
grep -Fq 't) export PANDORA_SKIP_PRECHECK_FRESH_DB=yes-empty-database' "$native" || fail 'native precheck skip is not tied to a fresh database'
grep -Fq 'pg_dump -Fc -d aegis' "$native" || fail 'install-native.sh has no pre-upgrade dump'
awk '/pg_dump -Fc -d aegis/ { dump = NR } /"\$INSTALL_DIR\/deploy\/migrate.sh" up/ { mig = NR } END { exit !(dump && mig && dump < mig) }' "$native" \
  || fail 'native dump does not come before the migration'

printf 'install-chain mock: PASS\n'

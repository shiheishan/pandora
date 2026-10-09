#!/usr/bin/env bash
# install-native.sh --from-docker（docker 布局迁到直装）的桩测试，不需要 root、Docker、PostgreSQL：
#   ① fd_render_env：应用密钥与其余设置原样沿用，只换连库连缓存的键，路径换成直装的，去掉 socket 说明，
#      每个键只出现一次；
#   ② fd_roles_sql：属性照搬，特权角色、怪名字拒绝；
#   ③ fd_save_units / fd_restore_units：同名单元原样放回，之前没有的删掉；
#   ④ fd_abort：只在「docker 写入者已停、直装还没接管」时回滚——放回单元、拉起 docker 的网关与备份 timer、
#      状态记 rolled-back；接管之后或还没停服时什么都不做；
#   ⑤ fd_finalize：只停容器（compose stop），从不删卷；
#   ⑥ fd_fingerprint：两边都以超级用户读、跑迁移的角色各自归一成 <m>，口令不进命令行参数；
#   ⑦ fd_preflight：容器没跑、发布包比库旧、特权角色、角色成员关系、口令含怪字符、密钥没填、磁盘不够都在动手前停下；
#   ⑧ 静态：主流程的顺序（核对 → 写状态 → 停写入者 → 导出 → 恢复 → 核对指纹 → 迁移 → 存单元 → 换单元 → 健康检查 → 接管 → 停容器），
#      回滚 trap 在停写入者之前就装好；删卷、删 /opt/aegispanel 只出现在提示里。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
NATIVE="$DEPLOY/install-native.sh"
LIB="$DEPLOY/install-native-lib.sh"
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-fromdocker.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'install-native from-docker: %s\n' "$*" >&2; exit 1; }

mkdir -p "$T/bin"
# systemctl 桩：记录，is-active 按 $T/active.<单元> 答
cat >"$T/bin/systemctl" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
printf 'systemctl %s\n' "$*" >>"$root/calls"
if [ "$1" = is-active ]; then
  unit="${@: -1}"
  [ -f "$root/active.$unit" ]
fi
MOCK
# docker 桩：口令只许在环境里；按 SQL 内容答
cat >"$T/bin/docker" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
printf 'docker %s | PGPASSWORD=%s\n' "$*" "${PGPASSWORD:-}" >>"$root/calls"
args=" $* "
case "$args" in
  *' inspect '*) cat "$root/docker.running" 2>/dev/null; exit 0 ;;
  *' compose stop '*) [ ! -f "$root/fail.compose" ] || exit 1; exit 0 ;;
  *' stop aegis-postgres aegis-valkey '*) [ ! -f "$root/fail.stop" ] || exit 1; exit 0 ;;
esac
case "$args" in
  *goose_db_version*) cat "$root/src.version" ;;
  *rolsuper*) cat "$root/roles.plan" 2>/dev/null || true ;;
  *pg_auth_members*) cat "$root/memberships" 2>/dev/null || true ;;
  *pg_database_size*) echo 102400 ;;
  *' psql '*) cat >"$root/docker.stdin" ;;
esac
MOCK
cat >"$T/bin/runuser" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
printf 'runuser %s\n' "$*" >>"$root/calls"
cat >"$root/runuser.stdin"
MOCK
printf '#!/usr/bin/env bash\nexit 0\n' >"$T/bin/sleep"
chmod 0755 "$T/bin/"*
export PATH="$T/bin:$PATH"

. "$DEPLOY/public-base-url.sh"
. "$LIB"
set -euo pipefail

# --- ① fd_render_env ---------------------------------------------------------------
# 按 install.sh 的做法由 .env.example 生成一份 docker 布局的 .env，再换成 socket 连接串、带上说明注释
sed -e 's|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=owner-fixture-pw-aaaaaaaa|' \
    -e 's|^AEGIS_MIGRATION_DATABASE_URL=.*|AEGIS_MIGRATION_DATABASE_URL=postgres://aegis:owner-fixture-pw-aaaaaaaa@127.0.0.1:5433/aegis?sslmode=disable|' \
    -e 's|^AEGIS_DB_APP_PASSWORD=.*|AEGIS_DB_APP_PASSWORD=app-fixture-pw-aaaaaaaaaaaaaaaaaaaaaaaa|' \
    -e 's|^VALKEY_PASSWORD=.*|VALKEY_PASSWORD=vk-fixture-pw-aaaaaaaa|' \
    -e 's|^AEGIS_DATABASE_URL=.*|AEGIS_DATABASE_URL=postgres://aegis_app:app-fixture-pw-aaaaaaaaaaaaaaaaaaaaaaaa@/aegis?host=/opt/aegispanel/deploy/run/postgresql|' \
    -e 's|^AEGIS_REDIS_URL=.*|AEGIS_REDIS_URL=unix://:vk-fixture-pw-aaaaaaaa@/opt/aegispanel/deploy/run/valkey/valkey.sock?db=0|' \
    -e 's|^AEGIS_ADMIN_PATH=.*|AEGIS_ADMIN_PATH=ops_0123456789abcdef|' \
    -e 's|^AEGIS_ENV=.*|AEGIS_ENV=production|' \
    -e 's|^AEGIS_PUBLIC_BASE_URL=.*|AEGIS_PUBLIC_BASE_URL=https://panel.example.test|' \
    -e 's|^AEGIS_MASTER_KEY=.*|AEGIS_MASTER_KEY=bWFzdGVyLWtleS1zZW50aW5lbC0wMTIzNDU2Nzg5YWI=|' \
    -e 's|^AEGIS_JWT_PUBLIC_SECRET=.*|AEGIS_JWT_PUBLIC_SECRET=jwt-public-sentinel|' \
    -e 's|^AEGIS_JWT_ADMIN_SECRET=.*|AEGIS_JWT_ADMIN_SECRET=jwt-admin-sentinel|' \
    -e 's|^AEGIS_CONFIG_SIGNING_SEED=.*|AEGIS_CONFIG_SIGNING_SEED=c2lnbmluZy1zZWVkLXNlbnRpbmVs|' \
    -e 's|^AEGIS_BACKUP_AGE_RECIPIENT=.*|AEGIS_BACKUP_AGE_RECIPIENT=age1dockerrecipient|' \
    -e 's|^AEGIS_BACKUP_AGE_IDENTITY=.*|AEGIS_BACKUP_AGE_IDENTITY=/opt/aegispanel/secrets/backup-age.key|' \
    "$DEPLOY/.env.example" >"$T/docker.env"
cat >>"$T/docker.env" <<'ENV'
AEGIS_ALERT_TG_CHAT=-1000000000001

# 网关经 unix socket 连 PG 与 Valkey（install.sh 换的，socket 由 docker-compose.yml 挂到 deploy/run/）。
# 回退到不带 run/ 挂载的旧发布包之前，先把这两条改回回环形式（口令不变）：
#   postgres://aegis_app:<AEGIS_DB_APP_PASSWORD>@127.0.0.1:5433/aegis?sslmode=disable
#   redis://:<VALKEY_PASSWORD>@127.0.0.1:6380/0
ENV
INSTALL_DIR=/opt/pandora PG_PORT=5432 VK_PORT=6379 PG_SUPER_PASS=super-fixture-pw-aaaaaaaa
DB_PASS=owner-fixture-pw-aaaaaaaa APP_PASS=app-fixture-pw-aaaaaaaaaaaaaaaaaaaaaaaa VK_PASS=vk-fixture-pw-aaaaaaaa
fd_render_env "$T/docker.env" >"$T/native.env"
want() { grep -qxF "$1" "$T/native.env" || fail "rendered .env lacks: $1"; }
want 'POSTGRES_USER=aegis'
want 'POSTGRES_DB=aegis'
want 'POSTGRES_PORT=5432'
want 'POSTGRES_SUPER_PASSWORD=super-fixture-pw-aaaaaaaa'
want 'AEGIS_MIGRATION_DATABASE_URL=postgres://postgres:super-fixture-pw-aaaaaaaa@127.0.0.1:5432/aegis?sslmode=disable'
want 'AEGIS_DATABASE_URL=postgres://aegis_app:app-fixture-pw-aaaaaaaaaaaaaaaaaaaaaaaa@127.0.0.1:5432/aegis?sslmode=disable'
want 'AEGIS_REDIS_URL=redis://:vk-fixture-pw-aaaaaaaa@127.0.0.1:6379/0'
want 'VALKEY_PORT=6379'
want 'PANDORA_DB_LAYOUT=native'
want 'AEGIS_BACKUP_DIR=/var/backups/pandora'
want 'AEGIS_BACKUP_AGE_IDENTITY=/opt/pandora/secrets/backup-age.key'
want 'AEGIS_BACKUP_WEBDAV_BIN=/opt/pandora/bin/aegis-backup-webdav'
# 应用密钥与其余设置逐字沿用
for key in AEGIS_MASTER_KEY AEGIS_JWT_PUBLIC_SECRET AEGIS_JWT_ADMIN_SECRET AEGIS_CONFIG_SIGNING_SEED AEGIS_ADMIN_PATH \
    AEGIS_PUBLIC_BASE_URL AEGIS_ENV AEGIS_BACKUP_AGE_RECIPIENT AEGIS_ALERT_TG_CHAT AEGIS_ACCESS_TOKEN_TTL AEGIS_PUBLIC_ADDR; do
  line="$(grep "^$key=" "$T/docker.env")"
  want "$line"
done
dups="$(grep -oE '^[A-Za-z_][A-Za-z0-9_]*=' "$T/native.env" | sort | uniq -d)"
[ -z "$dups" ] || fail "duplicate keys: $dups"
if grep -Eq '/opt/aegispanel|/var/backups/aegispanel|deploy/run/|127\.0\.0\.1:5433|:6380|unix://' "$T/native.env"; then
  fail "docker-layout leftovers: $(grep -E '/opt/aegispanel|/var/backups/aegispanel|deploy/run/|5433|6380|unix://' "$T/native.env")"
fi
if grep -Fq '网关经 unix socket' "$T/native.env"; then fail 'the docker socket note was kept'; fi
[ "$(grep -c . "$T/native.env")" -gt 40 ] || fail 'comments and other settings were dropped'

# --- ② fd_roles_sql ----------------------------------------------------------------
sql="$(printf '%s\n' 'aegis_app f f f t f f f' 'aegis_idempotency_owner f f f f f f f' | fd_roles_sql)"
grep -Fxq "SELECT 'CREATE ROLE aegis_app NOSUPERUSER NOREPLICATION NOBYPASSRLS LOGIN NOINHERIT NOCREATEDB NOCREATEROLE' WHERE NOT EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = 'aegis_app') \\gexec" <<<"$sql" \
  || fail "roles sql: $sql"
grep -Fq "CREATE ROLE aegis_idempotency_owner NOSUPERUSER NOREPLICATION NOBYPASSRLS NOLOGIN NOINHERIT" <<<"$sql" || fail "owner role sql: $sql"
if printf 'evil t f f t t f f\n' | fd_roles_sql >/dev/null 2>&1; then fail 'a superuser role was accepted'; fi
if printf 'rep f t f f t f f\n' | fd_roles_sql >/dev/null 2>&1; then fail 'a replication role was accepted'; fi
if printf 'byp f f t f t f f\n' | fd_roles_sql >/dev/null 2>&1; then fail 'a bypassrls role was accepted'; fi
if printf 'Bad-Name f f f f t f f\n' | fd_roles_sql >/dev/null 2>&1; then fail 'an unusual role name was accepted'; fi

# --- ③ 单元存取 ----------------------------------------------------------------------
FD_SYSTEMD_DIR="$T/systemd"; mkdir -p "$FD_SYSTEMD_DIR"
for u in aegis-public.service aegis-admin.service aegis-node.service aegis-health.service aegis-health.timer; do
  printf 'docker %s\n' "$u" >"$FD_SYSTEMD_DIR/$u"
done
FD_UNITS_BACKUP="$T/units-backup"
fd_save_units
for u in "${FD_UNITS[@]}"; do printf 'native %s\n' "$u" >"$FD_SYSTEMD_DIR/$u"; done
: >"$T/calls"
fd_restore_units
grep -qx 'docker aegis-public.service' "$FD_SYSTEMD_DIR/aegis-public.service" || fail 'gateway unit was not put back'
[ ! -e "$FD_SYSTEMD_DIR/aegis-backup.service" ] || fail 'a unit that did not exist before was left behind'
grep -qx 'systemctl daemon-reload' "$T/calls" || fail 'no daemon-reload after restoring units'
# 退回 Docker 的提示用 aegis-* 通配：备份目录里不能有会被拷进 systemd 目录的标记文件
if ls "$FD_UNITS_BACKUP"/aegis-* | grep -v -E '\.(service|timer)$'; then fail 'marker files match the aegis-* restore glob'; fi

# --- ④ fd_abort --------------------------------------------------------------------
FD_STATE_FILE="$T/state"; : >"$FD_STATE_FILE"
SERVICES=(aegis-public aegis-admin aegis-node)
run_abort() { ( exit 1 ) || fd_abort >"$T/abort.out" 2>&1 || true; }
# 还没停服：什么都不做
FD_WRITERS_STOPPED=0 FD_CUTOVER=0 FD_UNITS_SWAPPED=0; : >"$T/calls"
run_abort
[ ! -s "$T/calls" ] || fail "abort acted before writers were stopped: $(cat "$T/calls")"
# 已接管：什么都不做
FD_WRITERS_STOPPED=1 FD_CUTOVER=1; : >"$T/calls"
run_abort
[ ! -s "$T/calls" ] || fail "abort acted after cutover: $(cat "$T/calls")"
# 停服之后、换单元之前失败：只把 docker 的网关与备份 timer 拉回来
FD_WRITERS_STOPPED=1 FD_CUTOVER=0 FD_UNITS_SWAPPED=0 FD_BACKUP_TIMER_WAS_ACTIVE=1; : >"$T/calls"
run_abort
grep -qx 'systemctl start aegis-public aegis-admin aegis-node' "$T/calls" || fail "old writers not restarted: $(cat "$T/calls")"
grep -qx 'systemctl start aegis-backup.timer' "$T/calls" || fail 'backup timer not restarted'
if grep -q 'daemon-reload' "$T/calls"; then fail 'units restored although they were never swapped'; fi
[ "$(fd_state_get state)" = rolled-back ] || fail "state after abort: $(fd_state_get state)"
# 换了单元之后失败：先停直装的网关、放回单元，再拉起 docker 的
for u in "${FD_UNITS[@]}"; do printf 'native %s\n' "$u" >"$FD_SYSTEMD_DIR/$u"; done
FD_UNITS_SWAPPED=1 FD_BACKUP_TIMER_WAS_ACTIVE=0; : >"$T/calls"
run_abort
awk '/systemctl stop aegis-public aegis-admin aegis-node/ { s = NR } /daemon-reload/ { r = NR } /systemctl start aegis-public aegis-admin aegis-node/ { st = NR }
     END { exit !(s && r && st && s < r && r < st) }' "$T/calls" || fail "abort order after swap: $(cat "$T/calls")"
grep -qx 'docker aegis-public.service' "$FD_SYSTEMD_DIR/aegis-public.service" || fail 'abort did not put the docker unit back'
if grep -q 'aegis-backup.timer' "$T/calls"; then fail 'a backup timer that was not running got started'; fi
if grep -Eq 'docker .*(down|rm|volume)' "$T/calls"; then fail 'abort touched docker containers or volumes'; fi

# --- ⑤ fd_finalize：只停容器 ----------------------------------------------------------
DOCKER_DIR="$T/aegispanel"; mkdir -p "$DOCKER_DIR/deploy"
: >"$T/calls"; FD_BACKUP_TIMER_WAS_ACTIVE=1
fd_finalize >/dev/null
grep -q '^docker compose stop' "$T/calls" || fail "containers not stopped: $(cat "$T/calls")"
grep -qx 'systemctl start aegis-backup.timer' "$T/calls" || fail 'backup timer not resumed after cutover'
[ "$(fd_state_get state)" = done ] || fail 'state not done after finalize'
touch "$T/fail.compose"; : >"$T/calls"; FD_BACKUP_TIMER_WAS_ACTIVE=0
fd_finalize >/dev/null 2>&1
grep -q '^docker stop aegis-postgres aegis-valkey' "$T/calls" || fail 'no fallback docker stop'
rm -f "$T/fail.compose"
if grep -Eq 'down|volume|rm ' "$T/calls"; then fail "finalize removed something: $(cat "$T/calls")"; fi

# --- ⑥ 指纹：两边都以超级用户读，跑迁移的角色归一，口令只在环境里 ---------------------------
FD_DOCKER_PG_USER=aegis FD_DOCKER_PG_PASSWORD=owner-fixture-pw-aaaaaaaa FD_DOCKER_PG_DB=aegis
: >"$T/calls"
fd_fingerprint docker
grep -q '^docker exec -i -e PGPASSWORD aegis-postgres psql -U aegis -X -q -At -d aegis -v migrator=aegis | PGPASSWORD=owner-fixture-pw-aaaaaaaa$' "$T/calls" \
  || fail "docker fingerprint call: $(cat "$T/calls")"
cmp -s "$T/docker.stdin" <(fd_fingerprint_sql) || fail 'docker side did not get the fingerprint SQL'
fd_fingerprint native
grep -q '^runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 -p 5432 -d aegis -At -v migrator=postgres$' "$T/calls" \
  || fail "native fingerprint call: $(cat "$T/calls")"
cmp -s "$T/runuser.stdin" <(fd_fingerprint_sql) || fail 'native side did not get the fingerprint SQL'
if sed 's/ | PGPASSWORD=.*//' "$T/calls" | grep -Eq 'owner-fixture|super-fixture'; then fail 'a password went into argv'; fi
fp="$(fd_fingerprint_sql)"
for needle in "goose " "rows=" "acl=" "'<m>'" "definer=" "seq " "policy " "trigger " "col " "ext " "schema "; do
  grep -Fq -- "$needle" <<<"$fp" || fail "fingerprint does not cover: $needle"
done

# --- ⑦ fd_preflight ----------------------------------------------------------------
mkdir -p "$T/rel/deploy" "$T/rel/migrations"
for v in 00001 00150; do printf -- '-- +goose Up\n' >"$T/rel/migrations/${v}_x.sql"; done
SCRIPT_DIR="$T/rel/deploy"
cp "$T/docker.env" "$DOCKER_DIR/deploy/.env"
cat >"$T/bin/df" <<'MOCK'
#!/usr/bin/env bash
printf 'Filesystem 1024-blocks Used Available Capacity Mounted\n/dev/x 100 1 %s 1%% /\n' "$(cat "$(dirname "$0")/../df.avail")"
MOCK
printf '#!/usr/bin/env bash\necho 2049\n' >"$T/bin/stat"
chmod 0755 "$T/bin/df" "$T/bin/stat"
reset_pf() {
  cp "$T/docker.env" "$DOCKER_DIR/deploy/.env"
  echo true >"$T/docker.running"; echo 150 >"$T/src.version"; echo 99999999 >"$T/df.avail"
  printf '%s\n' 'aegis_app f f f t f f f' 'aegis_idempotency_owner f f f f f f f' >"$T/roles.plan"
  : >"$T/memberships"
}
pf_ok() { ( fd_preflight ) >"$T/pf.out" 2>&1 || fail "$1: preflight refused: $(cat "$T/pf.out")"; }
pf_stop() { if ( fd_preflight ) >"$T/pf.out" 2>&1; then fail "$1: preflight accepted"; fi
  grep -Fq -- "$2" "$T/pf.out" || fail "$1: message lacks '$2': $(cat "$T/pf.out")"; }
reset_pf; pf_ok 'healthy docker layout'
grep -Fq '迁移版本 150' "$T/pf.out" || fail "preflight summary: $(cat "$T/pf.out")"
reset_pf; echo false >"$T/docker.running"; pf_stop 'container down' '容器 aegis-postgres 没在跑'
reset_pf; echo 151 >"$T/src.version"; pf_stop 'release older than database' '这个发布包比 docker 布局的库还旧'
reset_pf; echo 'evil t f f t t f f' >>"$T/roles.plan"; pf_stop 'superuser role' 'SUPERUSER'
reset_pf; echo 'aegis_app -> someone' >"$T/memberships"; pf_stop 'role membership' '角色成员关系'
reset_pf; sed -i.bak 's|^VALKEY_PASSWORD=.*|VALKEY_PASSWORD=has space"quote|' "$DOCKER_DIR/deploy/.env"; pf_stop 'unsafe password' 'VALKEY_PASSWORD'
reset_pf; sed -i.bak 's|^AEGIS_MASTER_KEY=.*|AEGIS_MASTER_KEY=CHANGE_ME|' "$DOCKER_DIR/deploy/.env"; pf_stop 'missing master key' 'AEGIS_MASTER_KEY'
reset_pf; echo 100 >"$T/df.avail"; pf_stop 'disk too small' '磁盘不够'
reset_pf; rm "$DOCKER_DIR/deploy/.env"; pf_stop 'no docker layout' '找不到 docker 布局'
# 核对阶段只读：没停任何服务、没动任何容器
if grep -Eq 'systemctl (stop|start|restart)|docker (stop|compose)' "$T/calls"; then fail "preflight changed something: $(grep -E 'systemctl|docker (stop|compose)' "$T/calls")"; fi
rm -f "$T/bin/df" "$T/bin/stat"

# --- ⑧ 静态：主流程顺序与红线 ----------------------------------------------------------
# 每一步取「上一步之后」第一次出现的行：逐个都找得到，顺序就对
order() {
  local prev=0 n pat
  for pat in "$@"; do
    n="$(awk -v from="$prev" -v pat="$pat" 'NR > from && index($0, pat) { print NR; exit }' "$NATIVE")"
    [ -n "$n" ] || fail "install-native.sh lacks '$pat' after line $prev (missing or out of order)"
    prev="$n"
  done
}
order 'fd_preflight' 'fd_state_set state=preparing' 'trap fd_abort EXIT' 'fd_stop_docker_writers' \
  'fd_docker_pg pg_dump -d "$FD_DOCKER_PG_DB" -Fc' 'pg_restore -p "$PG_PORT" -d aegis --exit-on-error --single-transaction' \
  'REASSIGN OWNED BY' 'diff -u "$FD_DUMP.docker.fingerprint" "$FD_DUMP.native.fingerprint"' \
  'pandora_run_migrations "$MODE"' 'bash "$INSTALL_DIR/deploy/bootstrap.sh"' 'fd_save_units' 'FD_UNITS_SWAPPED=1' \
  'cp -f "$RELEASE_BIN"/* "$INSTALL_DIR/bin/"' 'fd_gateways_healthy' 'FD_CUTOVER=1' 'fd_state_set state=cutover' 'fd_finalize'
code="$(cat "$NATIVE" "$LIB" | grep -v '^[[:space:]]*#' | grep -v '^[[:space:]]*say ')"
if grep -Eq 'compose down|volume rm|docker rm|rm -rf "?\$DOCKER_DIR|docker system prune' <<<"$code"; then
  fail 'install-native.sh deletes docker containers, volumes or the docker install directory'
fi
grep -Fq 'docker compose down -v' "$NATIVE" || fail 'the summary does not print the manual volume-removal command'
grep -Fq 'trap fd_abort EXIT' "$NATIVE" || fail 'no rollback trap'

# --- ⑩ 发布控制器的缺省安装目录跟着布局走 -----------------------------------------------
eval "$(awk '/^default_app_dir\(\) \{$/ { p = 1 } p { print } p && /^}$/ { exit }' "$DEPLOY/release-stop-the-world.sh")"
declare -F default_app_dir >/dev/null || fail 'release-stop-the-world.sh has no default_app_dir'
L="$T/layouts"; mkdir -p "$L/docker/deploy" "$L/native/deploy"
[ "$(default_app_dir "$L/docker" "$L/native")" = "$L/docker" ] || fail 'nothing installed: controller default is not the docker path'
touch "$L/native/deploy/.env"
[ "$(default_app_dir "$L/docker" "$L/native")" = "$L/native" ] || fail 'native install not picked by default'
touch "$L/docker/deploy/.env"
if default_app_dir "$L/docker" "$L/native" >/dev/null 2>&1; then fail 'both layouts installed: controller guessed instead of asking'; fi
printf 'state=done\n' >"$L/native/deploy/from-docker.state"
[ "$(default_app_dir "$L/docker" "$L/native")" = "$L/native" ] || fail 'a finished --from-docker host is not treated as native'
rm "$L/native/deploy/.env" "$L/native/deploy/from-docker.state"
[ "$(default_app_dir "$L/docker" "$L/native")" = "$L/docker" ] || fail 'docker-only host not picked'

printf 'install-native from-docker mock: PASS\n'

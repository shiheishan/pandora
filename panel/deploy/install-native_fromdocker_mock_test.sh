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
#      回滚 trap 在停写入者之前就装好；删卷、删 /opt/aegispanel 只出现在提示里；
#   ⑨ install.sh 入口：全新安装在任何前置检查之前交给 install-native.sh，PANDORA_LAYOUT=docker 才走 docker 布局；
#   ⑩ 发布控制器的缺省安装目录跟着布局走，两种都在且不是迁完的直装时要人明说。
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
if [ "$1" = stop ] && [ -f "$root/fail.systemctl-stop" ]; then exit 1; fi
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
case "$*" in *datdba*) cat "$root/owned-dbs" 2>/dev/null; exit 0 ;; esac
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
# 口令不进 awk 的命令行参数：PATH 里放一个记 argv 的 awk 包装
real_awk="$(command -v awk)"
printf '#!/usr/bin/env bash\nprintf "%%s\\n" "$*" >>"%s/awk.argv"\nexec "%s" "$@"\n' "$T" "$real_awk" >"$T/bin/awk"
chmod 0755 "$T/bin/awk"; hash -r
fd_render_env "$T/docker.env" >"$T/native.env"
rm -f "$T/bin/awk"; hash -r
[ -s "$T/awk.argv" ] || fail 'the awk wrapper did not see fd_render_env'
if grep -Eq 'fixture-pw' "$T/awk.argv"; then fail 'fd_render_env passes passwords in awk argv'; fi
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

# 重跑（上次硬中断）：状态文件里记着第一次存的原件就沿用，不拿眼下的单元覆盖它
FD_STATE_FILE="$T/state-units"; printf 'units_backup=%s\n' "$FD_UNITS_BACKUP" >"$FD_STATE_FILE"
for u in "${FD_UNITS[@]}"; do printf 'ExecStart=/opt/pandora/bin/x %s\n' "$u" >"$FD_SYSTEMD_DIR/$u"; done
FD_UNITS_BACKUP="$T/units-backup-second"
fd_save_units 2>/dev/null
[ "$FD_UNITS_BACKUP" = "$T/units-backup" ] || fail "rerun did not reuse the first backup: $FD_UNITS_BACKUP"
[ ! -e "$T/units-backup-second" ] || fail 'rerun wrote a second backup from native units'
grep -qx 'docker aegis-public.service' "$T/units-backup/aegis-public.service" || fail 'the original docker unit backup was overwritten'
# 没有记录、眼下的单元已经指向直装：停下，不把它当原件存
FD_STATE_FILE="$T/state-none"; : >"$FD_STATE_FILE"; FD_UNITS_BACKUP="$T/units-backup-third"
if ( fd_save_units ) >"$T/save.out" 2>&1; then fail 'native units were saved as the docker originals'; fi
grep -Fq '已经指向直装目录' "$T/save.out" || fail "refusal message: $(cat "$T/save.out")"
[ ! -e "$T/units-backup-third" ] || fail 'a backup directory was created before refusing'
# 眼下的单元还是 docker 的（回滚之后按 docker 布局升级过），就算状态里记着旧档也重新存：再回滚要放回现在这份
for u in aegis-public.service aegis-admin.service aegis-node.service aegis-health.service aegis-health.timer; do
  printf 'docker-upgraded %s\n' "$u" >"$FD_SYSTEMD_DIR/$u"
done
for u in aegis-backup.service aegis-backup.timer aegis-tls-renew.service aegis-tls-renew.timer; do rm -f "$FD_SYSTEMD_DIR/$u"; done
FD_STATE_FILE="$T/state-units"; FD_UNITS_BACKUP="$T/units-backup-fourth"
fd_save_units 2>/dev/null
[ "$FD_UNITS_BACKUP" = "$T/units-backup-fourth" ] || fail "docker units were not saved afresh: $FD_UNITS_BACKUP"
grep -qx 'docker-upgraded aegis-public.service' "$T/units-backup-fourth/aegis-public.service" || fail 'the current docker units were not saved'
FD_STATE_FILE=""
for u in aegis-public.service aegis-admin.service aegis-node.service aegis-health.service aegis-health.timer; do
  printf 'docker %s\n' "$u" >"$FD_SYSTEMD_DIR/$u"
done
for u in aegis-backup.service aegis-backup.timer aegis-tls-renew.service aegis-tls-renew.timer; do rm -f "$FD_SYSTEMD_DIR/$u"; done
FD_UNITS_BACKUP="$T/units-backup"

# --- ④ fd_abort --------------------------------------------------------------------
FD_STATE_FILE="$T/state"; : >"$FD_STATE_FILE"
SERVICES=(aegis-public aegis-admin aegis-node)
# fd_abort 最后 exit，所以放进子 shell 跑
run_abort() { ( ( exit 1 ) || fd_abort ) >"$T/abort.out" 2>&1 || true; }
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

# ssh 断开：终端没了（写标准错误失败）再来一个 HUP。回滚照样做完：放回单元、拉起 docker 的网关与备份 timer、
# 状态记 rolled-back、日志文件里有记录。在独立的 bash 里按主流程的样子装 trap、开 errexit，
# 不放在 || 后面调用（那会关掉 errexit，测不出 set -e 下中途退出）
# trap 行从 install-native.sh 里原样抽出来（不照抄）：生产里改了 trap，探针跟着变
NATIVE_TRAPS="$(grep -E "^  trap (fd_abort EXIT|'FD_SIGNAL_RC=[0-9]+; exit [0-9]+' (HUP|INT|TERM))\$" "$NATIVE" | sed 's/^  //')"
[ "$(grep -c . <<<"$NATIVE_TRAPS")" -eq 4 ] && grep -q ' HUP$' <<<"$NATIVE_TRAPS" && grep -qx 'trap fd_abort EXIT' <<<"$NATIVE_TRAPS" \
  || fail "install-native.sh does not set the EXIT/HUP/INT/TERM traps for --from-docker: $NATIVE_TRAPS"
cat >"$T/hup.sh" <<HUP
set -euo pipefail
export PATH="$T/bin:\$PATH"
. "$DEPLOY/public-base-url.sh"
. "$LIB"
FD_SYSTEMD_DIR="$T/systemd" FD_UNITS_BACKUP="$T/units-backup" FD_STATE_FILE="$T/hup-state"
FD_LOG="$T/hup.log"; : >"\$FD_STATE_FILE"
FD_WRITERS_STOPPED=1 FD_CUTOVER=0 FD_UNITS_SWAPPED=1 FD_BACKUP_TIMER_WAS_ACTIVE=1
$NATIVE_TRAPS
exec 2>&-
kill -HUP \$\$
sleep 1
HUP
for u in "${FD_UNITS[@]}"; do printf 'native %s\n' "$u" >"$FD_SYSTEMD_DIR/$u"; done
: >"$T/calls"; rm -f "$T/hup.log"
# 再让「停直装网关」这一步失败：回滚里任何一步失败都不能让它半途退出
touch "$T/fail.systemctl-stop"
# 起它之前把 HUP 复位成缺省：外层若以 nohup / 后台循环跑（检查机就是），HUP 在进程入口就被忽略，
# bash 对「入口时已忽略的信号」装不上 trap，kill -HUP 什么也不会发生——那测不到我们要的路径
reset_hup() {
  if command -v python3 >/dev/null 2>&1; then
    python3 -c 'import os, signal, sys; signal.signal(signal.SIGHUP, signal.SIG_DFL); os.execvp(sys.argv[1], sys.argv[1:])' "$@"
  elif command -v perl >/dev/null 2>&1; then
    perl -e '$SIG{HUP} = "DEFAULT"; exec @ARGV or die' "$@"
  else
    "$@"
  fi
}
hup_rc=0; reset_hup bash "$T/hup.sh" >/dev/null || hup_rc=$?
rm -f "$T/fail.systemctl-stop"
[ "$hup_rc" -eq 129 ] || fail "HUP run exit code $hup_rc"
grep -qx 'systemctl start aegis-public aegis-admin aegis-node' "$T/calls" || fail "HUP with a dead terminal left docker writers down: $(cat "$T/calls")"
grep -qx 'systemctl start aegis-backup.timer' "$T/calls" || fail 'HUP: backup timer not restarted'
grep -qx 'systemctl daemon-reload' "$T/calls" || fail 'HUP: units not restored'
grep -qx 'docker aegis-public.service' "$FD_SYSTEMD_DIR/aegis-public.service" || fail 'HUP: docker unit not put back'
grep -qx 'state=rolled-back' "$T/hup-state" || fail "HUP: state not rolled-back: $(cat "$T/hup-state")"
grep -Fq '正在把 docker 布局的服务拉回来' "$T/hup.log" || fail 'HUP: rollback not logged'
grep -Fq 'docker 布局已恢复服务' "$T/hup.log" || fail 'HUP: rollback end not logged'

# --- ⑤ fd_finalize：只停容器 ----------------------------------------------------------
DOCKER_DIR="$T/aegispanel"; mkdir -p "$DOCKER_DIR/deploy"
printf 'POSTGRES_PASSWORD=x\n' >"$DOCKER_DIR/deploy/.env"
: >"$T/calls"; FD_BACKUP_TIMER_WAS_ACTIVE=1
fd_finalize >/dev/null 2>&1
# docker 的 .env 改名不删：旧布局的工具（install.sh 升级、/opt/aegispanel/deploy 下的脚本）从此找不到它
[ ! -e "$DOCKER_DIR/deploy/.env" ] || fail 'the docker .env is still in place after cutover'
grep -qx 'POSTGRES_PASSWORD=x' "$DOCKER_DIR/deploy/.env.migrated-to-native" || fail 'the docker .env was not kept as .env.migrated-to-native'
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
if grep -q 'string_agg' <<<"$(fd_fingerprint_sql)"; then fail 'fingerprint still aggregates with string_agg (memory grows with rows)'; fi
for needle in "goose " "rows=" " sum=" 'md5(t::text)' '::bit(64)::bigint::numeric' "SET TimeZone = 'UTC'" "acl=" "'<m>'" "definer=" "seq " "policy " "trigger " "col " "ext " "schema "; do
  grep -Fq -- "$needle" <<<"$fp" || fail "fingerprint does not cover: $needle"
done

# native_reassign_in_db：只换一个库里的对象属主；aegis 名下别的库（上次没迁完留下的 aegis_stale_*）换完改回
printf 'aegis_stale_20261009\n' >"$T/owned-dbs"; : >"$T/calls"
native_reassign_in_db 5432 aegis aegis postgres aegis || fail "native_reassign_in_db failed: $(cat "$T/calls")"
grep -qx 'runuser -u postgres -- psql -X -q -v ON_ERROR_STOP=1 -p 5432 -d aegis -v db=aegis -v src=aegis -v dst=postgres -v owner=aegis' "$T/calls" \
  || fail "reassign call: $(cat "$T/calls")"
[ "$(grep -c 'runuser' "$T/calls")" -eq 1 ] || fail "reassign is not a single psql: $(cat "$T/calls")"
for want in 'BEGIN;' 'REASSIGN OWNED BY :"src" TO :"dst";' \
    "SELECT pg_catalog.format('ALTER DATABASE %I OWNER TO %I', datname, :'src') FROM pandora_reassign_other_dbs \\gexec" 'COMMIT;'; do
  grep -Fq -- "$want" "$T/runuser.stdin" || fail "native reassign SQL lacks: $want"
done
grep -Fq 'native_reassign_in_db "$PG_PORT" aegis "$FD_DOCKER_PG_USER" postgres aegis' "$NATIVE" || fail '--from-docker does not use native_reassign_in_db'
rm -f "$T/owned-dbs"

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
  'native_reassign_in_db "$PG_PORT" aegis' 'diff -u "$FD_DUMP.docker.fingerprint" "$FD_DUMP.native.fingerprint"' \
  'pandora_run_migrations "$MODE"' 'bash "$INSTALL_DIR/deploy/bootstrap.sh"' 'fd_save_units' 'FD_UNITS_SWAPPED=1' \
  'cp -f "$RELEASE_BIN"/* "$INSTALL_DIR/bin/"' 'fd_gateways_healthy' 'FD_CUTOVER=1' 'fd_state_set state=cutover' 'fd_finalize'
code="$(cat "$NATIVE" "$LIB" | grep -v '^[[:space:]]*#' | grep -v '^[[:space:]]*say ')"
if grep -Eq 'compose down|volume rm|docker rm|rm -rf "?\$DOCKER_DIR|docker system prune' <<<"$code"; then
  fail 'install-native.sh deletes docker containers, volumes or the docker install directory'
fi
grep -Fq 'down -v   # 删容器与卷' "$NATIVE" || fail 'the summary does not print the manual volume-removal command'
grep -Fq 'trap fd_abort EXIT' "$NATIVE" || fail 'no rollback trap'

# --- ⑨ install.sh 入口：跑真的 install.sh，两个布局目录用 PANDORA_ENTRY_* 指到临时目录（不读真机的 /opt） -------
mkdir -p "$T/entry/deploy"
cp "$DEPLOY/install.sh" "$T/entry/deploy/install.sh"
printf '#!/usr/bin/env bash\nprintf "NATIVE-INSTALLER %%s\\n" "$*"\n' >"$T/entry/deploy/install-native.sh"
EN="$T/entry-real"
# run_entry <期望 native|docker|stop> <PANDORA_LAYOUT> <场景>：native = 交给了 install-native.sh；
# docker = 走到 docker 布局的前置检查（本机不是 Linux 或不是 root，会在那里停）；stop = 入口判断停下、什么都没动
run_entry() {
  local out
  out="$(PANDORA_ENTRY_TEST=1 PANDORA_ENTRY_DOCKER_DIR="$EN/docker" PANDORA_ENTRY_NATIVE_DIR="$EN/native" PANDORA_LAYOUT="$2" \
    bash "$T/entry/deploy/install.sh" 2>&1 </dev/null || true)"
  case "$1" in
    native) grep -q '^NATIVE-INSTALLER' <<<"$out" || fail "real install.sh, $3: did not hand off to install-native.sh: $out" ;;
    docker)
      if grep -q 'NATIVE-INSTALLER' <<<"$out"; then fail "real install.sh, $3: handed off to install-native.sh"; fi
      grep -q '检查运行环境' <<<"$out" || fail "real install.sh, $3: did not go on with the docker layout: $out" ;;
    stop)
      if grep -q 'NATIVE-INSTALLER' <<<"$out"; then fail "real install.sh, $3: handed off to install-native.sh"; fi
      if grep -q '检查运行环境' <<<"$out"; then fail "real install.sh, $3: went on with the docker layout"; fi
      grep -q '没动手' <<<"$out" || fail "real install.sh, $3: no stop message: $out" ;;
  esac
}
reset_en() { rm -rf "$EN"; mkdir -p "$EN/docker/deploy" "$EN/native/deploy"; }
reset_en;                                                   run_entry native '' 'fresh host'
                                                            run_entry docker docker 'fresh host, docker asked'
touch "$EN/docker/deploy/.env";                             run_entry docker '' 'docker only'
# 迁完：docker 的 .env 已改名、状态 done → 交给直装；要 docker 拒绝
mv "$EN/docker/deploy/.env" "$EN/docker/deploy/.env.migrated-to-native"; touch "$EN/native/deploy/.env"
echo state=done >"$EN/native/deploy/from-docker.state";    run_entry native '' 'migrated'
                                                            run_entry stop docker 'migrated, docker asked'
# 测试开关没开：覆盖变量不起作用（生产里误设了一个绕不过入口判断）。这里覆盖目录说「只有 docker」，
# 真机目录是空的（CI 机），所以不认覆盖时走的是「全新安装交给直装」
rm -rf "$T/entry-off"; mkdir -p "$T/entry-off/docker/deploy" "$T/entry-off/native/deploy"; touch "$T/entry-off/docker/deploy/.env"
if [ ! -e /opt/aegispanel/deploy/.env ] && [ ! -e /opt/pandora/deploy/.env ]; then
  out="$(PANDORA_ENTRY_DOCKER_DIR="$T/entry-off/docker" PANDORA_ENTRY_NATIVE_DIR="$T/entry-off/native" \
    bash "$T/entry/deploy/install.sh" 2>&1 </dev/null || true)"
  grep -q '^NATIVE-INSTALLER' <<<"$out" || fail "PANDORA_ENTRY_* took effect without PANDORA_ENTRY_TEST=1: $out"
fi
grep -Fq 'if [ "${PANDORA_ENTRY_TEST:-}" = 1 ]; then' "$DEPLOY/install.sh" || fail 'the entry overrides are not gated by PANDORA_ENTRY_TEST'
# 交接只看入口判断的结论（不另写一套条件：在新口径下老条件与它在每个场景上结果相同，行为测试分不出来）
grep -qx 'if \[ "\$ENTRY_LAYOUT" = native \]; then' "$DEPLOY/install.sh" || fail 'install.sh hands off on its own condition instead of ENTRY_LAYOUT'
# 迁完但 docker 的 .env 没改名（收尾改名没成）：真跑 install.sh 也停下，要人改名
touch "$EN/docker/deploy/.env";                             run_entry stop '' 'migrated, docker .env not parked'
                                                            run_entry stop docker 'migrated, docker .env not parked, docker asked'
rm "$EN/docker/deploy/.env"
# 照 RUNBOOK 退回 Docker：两个 .env 与状态文件都改名 → docker 布局照旧升级
mv "$EN/docker/deploy/.env.migrated-to-native" "$EN/docker/deploy/.env"
mv "$EN/native/deploy/.env" "$EN/native/deploy/.env.retired"
mv "$EN/native/deploy/from-docker.state" "$EN/native/deploy/from-docker.state.retired"
                                                            run_entry docker '' 'rolled back to docker'
# 退回时漏了改名状态文件（还记着 done）：停下说清楚；明说 docker 才按 docker 升级
echo state=done >"$EN/native/deploy/from-docker.state";    run_entry stop '' 'rolled back, state still done'
                                                            run_entry docker docker 'rolled back, state still done, docker asked'
# 两种都在、没有记录：停下，明说也不行
reset_en; touch "$EN/native/deploy/.env" "$EN/docker/deploy/.env"
                                                            run_entry stop '' 'both, no record'
                                                            run_entry stop docker 'both, no record, docker asked'
                                                            run_entry stop native 'both, no record, native asked'
echo state=rolled-back >"$EN/native/deploy/from-docker.state"; run_entry stop '' 'migration rolled back'
                                                            run_entry docker docker 'migration rolled back, docker asked'
echo state=cutover >"$EN/native/deploy/from-docker.state";  run_entry stop '' 'cutover not finalized'
# 交接在任何前置检查（要 docker）之前；docker 布局升级的收尾提示怎么迁
awk '/exec bash "\$HERE\/install-native.sh"/ { h = NR } /^step "检查运行环境"/ { c = NR } END { exit !(h && c && h < c) }' "$DEPLOY/install.sh" \
  || fail 'install.sh hands off after its docker prerequisite checks'
grep -Fq 'install-native.sh --from-docker' "$DEPLOY/install.sh" || fail 'docker upgrades do not point at --from-docker'
grep -Fq 'PANDORA_LAYOUT=docker bash ./install.sh' "$DEPLOY/test-install.sh" || fail 'test-install.sh no longer pins the docker layout'
# 入口判断 pandora_entry_layout：迁完的直装不会被悄悄切回 docker；两种都在又没记录、迁移没收尾都要人明说
eval "$(awk '/^pandora_entry_layout\(\) \{$/ { p = 1 } p { print } p && /^}$/ { exit }' "$DEPLOY/install.sh")"
declare -F pandora_entry_layout >/dev/null || fail 'install.sh has no pandora_entry_layout'
E="$T/entry-layouts"
entry() {  # entry <期望 native|docker|stop> <PANDORA_LAYOUT> <场景>
  local got rc=0
  got="$(pandora_entry_layout "$E/docker" "$E/native" "$2" 2>"$T/entry.err")" || rc=$?
  case "$1" in
    stop) [ "$rc" -ne 0 ] || fail "$3: expected a stop, got $got" ;;
    *) [ "$rc" -eq 0 ] && [ "$got" = "$1" ] || fail "$3: expected $1, got '$got' rc=$rc $(cat "$T/entry.err")" ;;
  esac
}
reset_e() { rm -rf "$E"; mkdir -p "$E/docker/deploy" "$E/native/deploy"; }
reset_e;                                                   entry native '' 'fresh host'
                                                           entry docker docker 'fresh host, docker asked'
touch "$E/docker/deploy/.env";                             entry docker '' 'docker only'
reset_e; touch "$E/native/deploy/.env";                    entry native '' 'native only'
                                                           entry stop docker 'native only, docker asked'
touch "$E/docker/deploy/.env"; echo state=done >"$E/native/deploy/from-docker.state"
                                                           entry stop '' 'migrated, old docker .env still there'
grep -Fq '把不用的那套的 deploy/.env 改名' "$T/entry.err" || fail "done-with-both message: $(cat "$T/entry.err")"
                                                           entry stop docker 'migrated, docker asked'
mv "$E/docker/deploy/.env" "$E/docker/deploy/.env.migrated-to-native"
                                                           entry native '' 'migrated, docker .env parked'
reset_e; touch "$E/native/deploy/.env" "$E/docker/deploy/.env"
                                                           entry stop '' 'both, no record'
                                                           entry stop docker 'both, no record, docker asked'
grep -Fq '把不用的那套的 deploy/.env 改名' "$T/entry.err" || fail "both-layouts message does not say what to do: $(cat "$T/entry.err")"
echo state=rolled-back >"$E/native/deploy/from-docker.state"
                                                           entry stop '' 'migration rolled back'
                                                           entry docker docker 'migration rolled back, docker asked'
echo state=cutover >"$E/native/deploy/from-docker.state";  entry stop '' 'cutover not finalized'
                                                           entry stop docker 'cutover not finalized, docker asked'
echo state=done >"$E/native/deploy/from-docker.state"; rm "$E/native/deploy/.env"
                                                           entry stop '' 'rolled back to docker, state still done'
grep -Fq '又退回了 Docker' "$T/entry.err" || fail "rolled-back-with-done message is wrong: $(cat "$T/entry.err")"
                                                           entry docker docker 'rolled back to docker, state still done, docker asked'
touch "$E/native/deploy/.env"; echo state=cutover >"$E/native/deploy/from-docker.state"
rm "$E/docker/deploy/.env"; echo state=prepared >"$E/native/deploy/from-docker.state"
                                                           entry stop '' 'record but no docker .env'
                                                           entry stop k8s 'bogus layout'
# install.sh 的交接用这个判断（不再只看 /opt/aegispanel/deploy/.env 在不在）
grep -Fq 'ENTRY_LAYOUT="$(pandora_entry_layout "$ENTRY_DOCKER_DIR" "$NATIVE_DEST" "${PANDORA_LAYOUT:-}")"' "$DEPLOY/install.sh" \
  || fail 'install.sh does not decide the layout with pandora_entry_layout'

# install-native.sh 普通模式：docker 布局还在服务就不动手；迁完的样子（state=done、直装 .env 在、docker 的已改名）才放行
plain() {  # plain <ok|stop> <docker .env 在不在 yes|no> <直装 .env 在不在 yes|no> <state> <场景>
  rm -rf "$T/plain"; mkdir -p "$T/plain/docker/deploy" "$T/plain/native/deploy"
  [ "$2" = no ] || touch "$T/plain/docker/deploy/.env"
  [ "$3" = no ] || touch "$T/plain/native/deploy/.env"
  if ( native_plain_mode_guard "$T/plain/docker" "$T/plain/native" "$4" ) >"$T/plain.out" 2>&1; then
    [ "$1" = ok ] || fail "plain mode $5: went ahead"
  else
    [ "$1" = stop ] || fail "plain mode $5: stopped: $(cat "$T/plain.out")"
  fi
}
plain ok no no '' 'fresh'
plain ok no yes '' 'native'
plain stop yes no '' 'docker host'
plain stop yes yes rolled-back 'after a rolled-back migration'
plain stop yes yes prepared 'migration in progress'
plain ok no yes done 'migrated'
plain stop yes no done 'rolled back to docker, state still done'
grep -Fq '又退回了 Docker' "$T/plain.out" || fail "rolled-back message: $(cat "$T/plain.out")"
plain stop yes yes done 'done but both .env present'
plain stop no no done 'done but no .env at all'
plain stop no yes cutover 'cutover not finalized'
plain stop no no rolled-back 'record without docker .env'
grep -Fq 'native_plain_mode_guard "$DOCKER_DIR" "$INSTALL_DIR" "$(fd_state_get state)"' "$NATIVE" || fail 'install-native.sh plain mode does not run the guard'
# 退回 Docker 的提示把状态文件一起改名；--from-docker 认得改名后的状态文件，按重来处理
grep -Fq 'mv $FD_STATE_FILE $FD_STATE_FILE.retired' "$NATIVE" || fail 'the roll-back instructions do not retire from-docker.state'
grep -Fq 'elif [[ -f "$FD_STATE_FILE.retired" ]]; then' "$NATIVE" || fail '--from-docker does not treat a retired migration as a retry'

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
# 迁完记录在、两个 .env 却都在（收尾改名没成）：与 install.sh、install-native.sh 一样停下要人改名
if default_app_dir "$L/docker" "$L/native" >/dev/null 2>&1; then fail 'done with both .env: controller picked a layout instead of asking'; fi
rm "$L/native/deploy/.env" "$L/native/deploy/from-docker.state"
[ "$(default_app_dir "$L/docker" "$L/native")" = "$L/docker" ] || fail 'docker-only host not picked'

printf 'install-native from-docker mock: PASS\n'

#!/usr/bin/env bash
# install.sh 端到端桩测试：不需要 root、数据库、Redis、nginx、网络（CI：GNU coreutils）。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"
trap 'rm -rf -- "$T"' EXIT

EV="$T/events.log"
ARGV="$T/argv.log"
OUT="$T/out.log"
REL="$T/rel"
SB="$T/sb"
STUB="$T/stub"

fail() {
  if [ -f "$OUT" ]; then
    printf '--- last 40 lines of out.log ---\n' >&2
    tail -n 40 "$OUT" >&2 || true
  fi
  printf 'install-mock: %s\n' "$*" >&2
  exit 1
}

event() { printf '%s\n' "$*" >>"$EV"; }
argv_log() { printf '%s\n' "$*" >>"$ARGV"; }

reset_logs() { : >"$EV"; : >"$ARGV"; : >"$OUT"; : >"$T/psql.stdin.log"; }

reset_flags() {
  rm -rf "$T/flags" "$T/state"
  mkdir -p "$T/flags" "$T/state"
}

prepare_sandbox() {
  rm -rf "$SB"
  mkdir -p "$SB/etc/redis" "$SB/etc/postgresql/18/main/conf.d" \
    "$SB/etc/nginx" "$SB/etc/logrotate.d" "$SB/etc/systemd/system" \
    "$SB/usr/lib/postgresql/18/bin" "$SB/var/backups/pandora" \
    "$SB/sys/fs/cgroup" "$SB/proc/net"
  printf '%s\n' \
    'bind 127.0.0.1 -::1' \
    'port 6379' \
    '# requirepass foobared' >"$SB/etc/redis/redis.conf"
  printf '%s\n' "include_dir = 'conf.d'" >"$SB/etc/postgresql/18/main/postgresql.conf"
  printf '#!/bin/sh\nexit 0\n' >"$SB/usr/lib/postgresql/18/bin/postgres"
  chmod 0755 "$SB/usr/lib/postgresql/18/bin/postgres"
  printf '00000000000000000000000000000001 01 80 10 80       lo\n' >"$SB/proc/net/if_inet6"
}

deploy_real_files() {
  local root="$1/deploy"
  mkdir -p "$root/systemd"
  local f
  for f in install.sh install-lib.sh public-base-url.sh platform.sh bootstrap.sh admin-url.sh \
    render-nginx.sh configure-app-role.sql nginx-pandora.conf postgresql-pandora.conf \
    config.example.yaml secrets.env.example; do
    cp "$DEPLOY/$f" "$root/$f"
  done
  for f in backup-postgres.sh verify-backup.sh restore-postgres.sh healthcheck.sh \
    update-cloudflare-realip.sh pg-revive.sh backup-webdav.example.json RUNBOOK.md; do
    printf '# p7-ops placeholder: %s\n' "$f" >"$root/$f"
  done
  printf '# logrotate placeholder\n' >"$root/logrotate-pandora"
  cat >"$root/edge-tls.sh" <<'EDGE'
#!/usr/bin/env bash
set -euo pipefail
T="__TEST_ROOT__"
EV="$T/events.log"
printf 'edge-tls %s\n' "$*" >>"$EV"
rc=3
[ -f "$T/flags/edge_rc" ] && rc="$(cat "$T/flags/edge_rc")"
exit "$rc"
EDGE
  sed -i.bak "s|__TEST_ROOT__|$T|g" "$root/edge-tls.sh" && rm -f "$root/edge-tls.sh.bak"
  chmod 0755 "$root/edge-tls.sh"
  cp "$DEPLOY/systemd/pandora-serve.service" "$DEPLOY/systemd/pandora-worker.service" "$root/systemd/"
  for u in pandora-backup.service pandora-backup.timer pandora-health.service pandora-health.timer \
    pandora-tls-renew.service pandora-tls-renew.timer; do
    printf '# placeholder %s\n' "$u" >"$root/systemd/$u"
  done
}

make_release() {
  local ver="$1" marker="$2"
  local root="$REL/pandora-panel_${ver}_linux_amd64"
  rm -rf "$root"
  mkdir -p "$root/bin" "$root/pdnd-dist" "$root/geoip" "$root/deploy/systemd"
  cat >"$root/bin/pandora" <<PAN
#!/usr/bin/env bash
# $marker
set -euo pipefail
T='$T'
EV="\$T/events.log"
ARGV="\$T/argv.log"
argv_log() { printf '%s\\n' "\$*" >>"\$ARGV"; }
event() { printf '%s\\n' "\$*" >>"\$EV"; }
argv_log "pandora \$*"
# install-lib 的 pandora_cli 总是先给 --config <路径>
if [ "\${1:-}" = --config ]; then shift 2; fi
event "pandora \$*"
db=0; mig=0; canary="\${LEAK_CANARY:-}"
[ -n "\${PANDORA_DATABASE_URL:-}" ] && db=1
[ -n "\${PANDORA_DATABASE_MIGRATION_URL:-}" ] && mig=1
event "pandora-env \$1 db=\$db mig=\$mig canary=\$canary"
case "\${1:-}" in
  migrate)
    [ "\${2:-}" = up ] || exit 2
    if [ -f "\$T/flags/migrate_fail" ]; then
      printf 'goose: mock failure\\n' >&2
      exit 1
    fi
    mkdir -p "\$T/flags"
    touch "\$T/flags/has_schema"
    exit 0
    ;;
  admin)
    case "\${2:-}" in
      has-admin)
        [ -f "\$T/state/admin" ] && exit 0
        exit 3
        ;;
      create)
        stdin="\$(cat)"
        exp="\$(cat "\$T/expected_admin_pw" 2>/dev/null || true)"
        if [ "\$stdin" = "\$exp" ] && [ -n "\$exp" ]; then
          event admin-pw-ok
        else
          event admin-pw-bad
        fi
        touch "\$T/state/admin"
        printf '管理员已就绪\\n'
        exit 0
        ;;
    esac
    ;;
esac
exit 0
PAN
  chmod 0755 "$root/bin/pandora"
  printf 'pdnd-amd64-%s\n' "$ver" >"$root/pdnd-dist/pandora-native-linux-amd64"
  printf 'pdnd-arm64-%s\n' "$ver" >"$root/pdnd-dist/pandora-native-linux-arm64"
  printf 'geoip-%s\n' "$ver" >"$root/geoip/ip2region_v4.xdb"
  deploy_real_files "$root"
  cat >"$root/deploy/release-artifact.env" <<EOF
PANDORA_RELEASE_VERSION=$ver
PANDORA_BINARY_SHA256=deadbeef
PANDORA_NATIVE_ARTIFACT_AMD64_SHA256=deadbeef
PANDORA_NATIVE_ARTIFACT_ARM64_SHA256=deadbeef
PANDORA_GEOIP_V4_SHA256=deadbeef
EOF
  ( cd "$root" && find bin pdnd-dist geoip deploy -type f -print0 | sort -z | xargs -0 sha256sum >SHA256SUMS )
  printf '%s\n' "$root"
}

make_stubs() {
  mkdir -p "$STUB"
  cat >"$STUB/apt-get" <<'S'
#!/usr/bin/env bash
T='__T__'
printf '%s\n' "apt-get $*" >>"$T/events.log"
printf '%s\n' "apt-get $*" >>"$T/argv.log"
exit 0
S
  cat >"$STUB/systemctl" <<'S'
#!/usr/bin/env bash
T='__T__'
printf '%s\n' "systemctl $*" >>"$T/events.log"
printf '%s\n' "systemctl $*" >>"$T/argv.log"
case "$1" in
  is-active)
    if [ "$2" = pandora-worker ] && [ -f "$T/flags/worker_down" ]; then exit 3; fi
    exit 0
    ;;
  is-enabled) exit 1 ;;
esac
exit 0
S
  cat >"$STUB/runuser" <<'S'
#!/usr/bin/env bash
T='__T__'
user=''
while [ $# -gt 0 ]; do
  case "$1" in
    -u) user="$2"; shift 2 ;;
    --) shift; break ;;
    *) shift ;;
  esac
done
printf '%s\n' "runuser $user $*" >>"$T/events.log"
exec "$@"
S
  cat >"$STUB/psql" <<'S'
#!/usr/bin/env bash
T='__T__'
ARGV="$T/argv.log"
EV="$T/events.log"
printf '%s\n' "psql $*" >>"$ARGV"
printf '%s\n' "psql $*" >>"$EV"
own=0; app=0; super=0
[ -n "${PANDORA_OWNER_PASSWORD:-}" ] && own=1
[ -n "${PANDORA_APP_PASSWORD:-}" ] && app=1
[ -n "${PANDORA_SUPER_PASSWORD:-}" ] && super=1
echo "psql-env owner=$own app=$app super=$super" >>"$EV"
q=''
while [ $# -gt 0 ]; do
  case "$1" in
    -Atc|-c) q="$2"; shift 2 ;;
    *) shift ;;
  esac
done
if [[ "$q" == *"datname = 'pandora'"* ]]; then
  [ -f "$T/flags/has_schema" ] && printf '1\n' || true
  exit 0
fi
if [[ "$q" == *goose_db_version* ]]; then
  [ -f "$T/flags/has_schema" ] && printf '1\n' || true
  exit 0
fi
[ -z "$q" ] || exit 0
stdin_all="$(cat)"
printf '%s' "$stdin_all" >>"$T/psql.stdin.log"
if [[ "$stdin_all" == *pandora_owner* ]] && [[ "$stdin_all" == *getenv* ]]; then
  echo bootstrap-roles >>"$EV"
fi
if [[ "$stdin_all" == *configure-app-role* ]] || [[ "$stdin_all" == *'缺少角色 pandora_owner'* ]] || [[ "$stdin_all" == *'pandora_app IN DATABASE'* ]]; then
  echo bootstrap-grants >>"$EV"
fi
exit 0
S
  # age-keygen 桩：-o 写一把虚构私钥，-y 打印虚构收件人
  cat >"$STUB/age-keygen" <<'S'
#!/usr/bin/env bash
T='__T__'
printf '%s\n' "age-keygen $*" >>"$T/argv.log"
case "$1" in
  -o) printf '%s\n' 'AGE-SECRET-KEY-1MOCKMOCKMOCKMOCKMOCKMOCKMOCKMOCKMOCKMOCKMOCKMOCKMOCKMOCKMOCK' >"$2" ;;
  -y) printf '%s\n' 'age1mockrecipientmockrecipientmockrecipientmockrecipientmock' ;;
  *) exit 2 ;;
esac
S
  for name in pg_createcluster pg_restore journalctl sleep useradd; do
    cat >"$STUB/$name" <<'S'
#!/usr/bin/env bash
T='__T__'
printf '%s\n' "NAME $*" >>"$T/argv.log"
exit 0
S
    sed -i.bak "s/NAME/$name/" "$STUB/$name" && rm -f "$STUB/$name.bak"
  done
  cat >"$STUB/pg_lsclusters" <<'S'
#!/usr/bin/env bash
T='__T__'
printf '%s\n' 'pg_lsclusters 18 main 5432 online' >>"$T/events.log"
printf '18  main    5432 online postgres /var/lib/postgresql/18/main /var/log/postgresql/postgresql-18-main.log\n'
S
  cat >"$STUB/pg_dump" <<'S'
#!/usr/bin/env bash
T='__T__'
printf '%s\n' "pg_dump $*" >>"$T/argv.log"
printf '%s\n' "pg_dump $*" >>"$T/events.log"
printf 'FAKEDUMP\n'
S
  cat >"$STUB/redis-server" <<'S'
#!/usr/bin/env bash
if [ "${1:-}" = --version ]; then
  printf '%s\n' 'Redis server v=7.0.15 sha=00000000:0 malloc=jemalloc-5.3.0 bits=64 build=0'
  exit 0
fi
exit 0
S
  cat >"$STUB/redis-cli" <<'S'
#!/usr/bin/env bash
[ -n "${REDISCLI_AUTH:-}" ] && printf 'PONG\n'
exit 0
S
  cat >"$STUB/nginx" <<'S'
#!/usr/bin/env bash
if [ "${1:-}" = -v ]; then
  printf 'nginx version: nginx/1.26.3\n' >&2
fi
exit 0
S
  cat >"$STUB/ufw" <<'S'
#!/usr/bin/env bash
T='__T__'
printf '%s\n' "ufw $*" >>"$T/argv.log"
if [ "$1" = status ]; then printf 'Status: active\n'; fi
exit 0
S
  cat >"$STUB/firewall-cmd" <<'S'
#!/usr/bin/env bash
exit 1
S
  cat >"$STUB/getent" <<'S'
#!/usr/bin/env bash
T='__T__'
[ "$1" = passwd ] && [ "$2" = pandora ] && [ -f "$T/state/user" ] && exit 0
exit 1
S
  cat >"$STUB/useradd" <<'S'
#!/usr/bin/env bash
T='__T__'
mkdir -p "$T/state"
touch "$T/state/user"
printf '%s\n' "useradd $*" >>"$T/events.log"
exit 0
S
  cat >"$STUB/chown" <<'S'
#!/usr/bin/env bash
T='__T__'
owner="$1"; path="$2"
printf '%s\n' "chown $owner $path" >>"$T/events.log"
printf '%s\n' "chown $owner $path" >>"$T/argv.log"
exit 0
S
  cat >"$STUB/id" <<'S'
#!/usr/bin/env bash
if [ "${1:-}" = -u ]; then printf '0\n'; exit 0; fi
exec /usr/bin/id "$@"
S
  cat >"$STUB/curl" <<'S'
#!/usr/bin/env bash
T='__T__'
printf '%s\n' "curl $*" >>"$T/argv.log"
out=''; code='200'
[ -f "$T/flags/readyz_fail" ] && code='503'
args=("$@")
i=0
while [ $i -lt ${#args[@]} ]; do
  if [ "${args[$i]}" = -o ]; then : >"${args[$((i+1))]}"; fi
  i=$((i+1))
done
if [ -n "$out" ]; then printf '%s' "$out"; else printf '%s' "$code"; fi
exit 0
S
  for s in "$STUB"/*; do
    [ -f "$s" ] || continue
    sed -i.bak "s|__T__|$T|g" "$s" && rm -f "$s.bak"
    chmod 0755 "$s"
  done
}

run_install() {
  local rel_root="$1" admin_email="${2:-}"
  reset_logs
  local env_args=(
    "PANDORA_INSTALL_SANDBOX=$SB"
    "PANDORA_ASSUME_YES=1"
    "PANDORA_PUBLIC_BASE_URL=https://panel.example.test"
    "LEAK_CANARY=leak"
    "PATH=$STUB:/usr/bin:/bin"
  )
  [ -n "$admin_email" ] && env_args+=("PANDORA_ADMIN_EMAIL=$admin_email")
  local rc=0
  if [ -n "$admin_email" ] && [ -f "$T/expected_admin_pw" ]; then
    printf '%s\n' "$(cat "$T/expected_admin_pw")" | env "${env_args[@]}" bash "$rel_root/deploy/install.sh" >"$OUT" 2>&1 || rc=$?
  else
    env "${env_args[@]}" bash "$rel_root/deploy/install.sh" >"$OUT" 2>&1 || rc=$?
  fi
  return "$rc"
}

lineno() { grep -n -m1 -e "$1" "$EV" | cut -d: -f1; }
lineno_last() { grep -n -e "$1" "$EV" | tail -1 | cut -d: -f1; }

secret_values() {
  local sec="$SB/etc/pandora/secrets.env" dep="$SB/etc/pandora/deploy.env" admin_pw=""
  [ -f "$T/expected_admin_pw" ] && admin_pw="$(cat "$T/expected_admin_pw")"
  python3 - "$sec" "$dep" "$admin_pw" <<'PY'
import re, sys, base64
sec, dep, admin = sys.argv[1], sys.argv[2], sys.argv[3]
def kv(path):
    d={}
    for line in open(path):
        line=line.strip()
        if not line or line.startswith('#') or '=' not in line: continue
        k,v=line.split('=',1); d[k]=v
    return d
s=kv(sec); e=kv(dep)
vals=[]
for k in ('PANDORA_SECRETS_MASTER_KEY','PANDORA_AUTH_PUBLIC_JWT_SECRET','PANDORA_AUTH_ADMIN_JWT_SECRET'):
    vals.append(s[k])
vals.append(e.get('POSTGRES_SUPER_PASSWORD',''))
u=s.get('PANDORA_DATABASE_URL','')
m=re.match(r'^postgres://[^:]+:([^@]+)@', u)
if m: vals.append(m.group(1))
u=s.get('PANDORA_REDIS_URL','')
m=re.match(r'^redis://:([^@]+)@', u)
if m: vals.append(m.group(1))
u=e.get('PANDORA_DATABASE_MIGRATION_URL','')
m=re.match(r'^postgres://[^:]+:([^@]+)@', u)
if m: vals.append(m.group(1))
if admin: vals.append(admin)
for v in vals:
    if v: print(v)
PY
}

assert_no_secret_leaks() {
  local v
  while IFS= read -r v; do
    [ -n "$v" ] || continue
    if grep -Fq "$v" "$ARGV" "$OUT" "$EV" "$T/psql.stdin.log" 2>/dev/null; then
      fail "secret leaked into logs: (${#v} chars)"
    fi
  done < <(secret_values)
  local ap
  ap="$(awk -F= '/^PANDORA_ADMIN_PATH=/{print $2}' "$SB/etc/pandora/deploy.env" | tail -1)"
  if [ -n "$ap" ] && grep -Fq "$ap" "$ARGV" "$EV"; then fail 'admin path in argv/events'; fi
  return 0
}

# --- 初始化 ----------------------------------------------------------------
make_stubs
prepare_sandbox
reset_flags
REL_ROOT="$(make_release t1 '# build=t1')"

# --- 用例 1：首装（带管理员） ------------------------------------------------
ADMIN_PW="Mock-Admin-$(openssl rand -hex 8)"
printf '%s\n' "$ADMIN_PW" >"$T/expected_admin_pw"
run_install "$REL_ROOT" 'admin@example.test' || fail "case1 exit $?: $(tail -5 "$OUT")"

for want in 'env: production' 'public_base_url: https://panel.example.test' 'addr: 127.0.0.1:9000' \
  'addr: 127.0.0.1:9001' 'public_host: panel.example.test'; do
  grep -q "$want" "$SB/etc/pandora/config.yaml" || fail "config missing: $want"
done
keys_sec=$(grep -E '^[A-Z][A-Z0-9_]*=' "$SB/etc/pandora/secrets.env" | cut -d= -f1 | sort)
keys_dep=$(grep -E '^[A-Z][A-Z0-9_]*=' "$SB/etc/pandora/deploy.env" | cut -d= -f1 | sort)
[ "$keys_sec" = "PANDORA_AUTH_ADMIN_JWT_SECRET
PANDORA_AUTH_PUBLIC_JWT_SECRET
PANDORA_DATABASE_URL
PANDORA_REDIS_URL
PANDORA_SECRETS_MASTER_KEY" ] || fail "secrets keys: $keys_sec"
[ "$keys_dep" = "PANDORA_ADMIN_PATH
PANDORA_BACKUP_AGE_IDENTITY
PANDORA_BACKUP_AGE_RECIPIENT
PANDORA_BACKUP_RETENTION_DAYS
PANDORA_DATABASE_MIGRATION_URL
PANDORA_SYSTEMD_HARDENING
POSTGRES_DB
POSTGRES_PORT
POSTGRES_SUPER_PASSWORD" ] || fail "deploy keys: $keys_dep"
grep -q '^PANDORA_SECRETS_MASTER_KEY=' "$SB/etc/pandora/secrets.env" || fail 'no master key'
jwt_pub="$(awk -F= '/^PANDORA_AUTH_PUBLIC_JWT_SECRET=/{print $2}' "$SB/etc/pandora/secrets.env")"
jwt_adm="$(awk -F= '/^PANDORA_AUTH_ADMIN_JWT_SECRET=/{print $2}' "$SB/etc/pandora/secrets.env")"
[ "$jwt_pub" != "$jwt_adm" ] || fail 'JWT secrets must differ'
master="$(awk -F= '/^PANDORA_SECRETS_MASTER_KEY=/{print $2}' "$SB/etc/pandora/secrets.env")"
python3 -c "import base64,sys; d=base64.b64decode(sys.argv[1]); sys.exit(0 if len(d)==32 else 1)" "$master" || fail 'master key not 32 bytes b64'
[ "$(stat -c %a "$SB/etc/pandora/secrets.env")" = 640 ] || fail 'secrets mode'
[ "$(stat -c %a "$SB/etc/pandora/config.yaml")" = 640 ] || fail 'config mode'
[ "$(stat -c %a "$SB/etc/pandora/deploy.env")" = 600 ] || fail 'deploy mode'
[ "$(stat -c %a "$SB/etc/pandora/backup-age.key")" = 600 ] || fail 'backup key mode'
grep -q "chown root:root $SB/etc/pandora/backup-age.key" "$EV" || fail 'chown backup key'
grep -q '^PANDORA_BACKUP_AGE_IDENTITY=/etc/pandora/backup-age.key$' "$SB/etc/pandora/deploy.env" || fail 'backup identity path'
grep -q '^PANDORA_BACKUP_AGE_RECIPIENT=age1' "$SB/etc/pandora/deploy.env" || fail 'backup recipient'

grep -q "chown root:pandora $SB/etc/pandora/secrets.env" "$EV" || fail 'chown secrets'
grep -q "chown root:pandora $SB/etc/pandora/config.yaml" "$EV" || fail 'chown config'
grep -q "chown root:root $SB/etc/pandora/deploy.env" "$EV" || fail 'chown deploy'
cmp -s "$DEPLOY/postgresql-pandora.conf" "$SB/etc/postgresql/18/main/conf.d/pandora.conf" || fail 'pg conf'
[ -f "$SB/etc/systemd/system/postgresql@18-main.service.d/pandora.conf" ] || fail 'pg dropin'
[ -f "$SB/etc/systemd/system/redis-server.service.d/pandora.conf" ] || fail 'redis dropin'
for u in pandora-serve.service pandora-worker.service pandora-backup.service pandora-backup.timer \
  pandora-health.service pandora-health.timer pandora-tls-renew.service pandora-tls-renew.timer; do
  [ -f "$SB/etc/systemd/system/$u" ] || fail "missing unit $u"
done
[ -x "$SB/opt/pandora/bin/pandora" ] || fail 'no pandora bin'
[ -f "$SB/opt/pandora/pdnd-dist/pandora-native-linux-amd64" ] || fail 'pdnd amd64'
[ -f "$SB/opt/pandora/pdnd-dist/pandora-native-linux-arm64" ] || fail 'pdnd arm64'
[ -f "$SB/opt/pandora/geoip/ip2region_v4.xdb" ] || fail 'geoip'
[ -f "$SB/opt/pandora/release.env" ] || fail 'release.env'
for f in admin-url.sh bootstrap.sh render-nginx.sh backup-postgres.sh verify-backup.sh restore-postgres.sh \
  healthcheck.sh edge-tls.sh update-cloudflare-realip.sh pg-revive.sh; do
  [ -f "$SB/opt/pandora/deploy/$f" ] || fail "host script $f"
done
redis_conf="$SB/etc/redis/redis.conf"
[ "$(grep -c '^# >>> pandora' "$redis_conf")" = 1 ] || fail 'redis pandora blocks'
grep -q 'maxmemory 256mb' "$redis_conf" && grep -q 'maxmemory-policy noeviction' "$redis_conf" || fail 'redis memory'
grep -q 'appendonly yes' "$redis_conf" && grep -q 'appendfsync everysec' "$redis_conf" || fail 'redis aof'
grep -q 'rename-command FLUSHALL ""' "$redis_conf" || fail 'redis flushall'
grep -q 'rename-command FLUSHDB ""' "$redis_conf" || fail 'redis flushdb'
[ "$(grep -c '^requirepass ' "$redis_conf")" = 1 ] || fail 'requirepass count'
rpw="$(awk '/^requirepass /{print $2}' "$redis_conf")"
url_pw="$(awk -F= '/^PANDORA_REDIS_URL=/{print $2}' "$SB/etc/pandora/secrets.env" | sed -n 's|^redis://:\([^@]*\)@.*|\1|p')"
[ "$rpw" = "$url_pw" ] || fail 'redis pass mismatch'
roles_ln="$(lineno 'bootstrap-roles')"
mig_ln="$(lineno 'pandora migrate up')"
grants_ln="$(lineno 'bootstrap-grants')"
restart_ln="$(lineno 'systemctl restart pandora-serve pandora-worker')"
admin_ln="$(lineno 'pandora admin create')"
edge_ln="$(lineno 'edge-tls setup')"
[ -n "$roles_ln" ] && [ -n "$mig_ln" ] && [ -n "$grants_ln" ] && [ -n "$restart_ln" ] && [ -n "$admin_ln" ] && [ -n "$edge_ln" ] \
  || fail "order markers missing: roles=$roles_ln mig=$mig_ln grants=$grants_ln"
[ "$roles_ln" -lt "$mig_ln" ] || fail 'roles after migrate'
[ "$mig_ln" -lt "$grants_ln" ] || fail 'migrate after grants'
[ "$grants_ln" -lt "$restart_ln" ] || fail 'grants after restart'
[ "$restart_ln" -lt "$admin_ln" ] || fail 'restart after admin'
[ "$admin_ln" -lt "$edge_ln" ] || fail 'admin after edge'
grep -q 'pandora-env migrate db=1 mig=1 canary=' "$EV" || fail 'migrate env'
grep -q 'pandora-env admin.*mig=0' "$EV" || fail 'admin mig=0'
grep -q 'admin-pw-ok' "$EV" || fail 'admin pw ok'
grep -q 'admin-pw-bad' "$EV" && fail 'admin pw bad'
grep -q 'psql-env owner=1 app=1 super=1' "$EV" || fail 'bootstrap passwords in env'
# pandora_cli 清空了继承的环境：LEAK_CANARY 不该到达 pandora
if grep -q '^pandora-env .*canary=[^[:space:]]' "$EV"; then
  fail "canary leaked: $(grep '^pandora-env .*canary=[^[:space:]]' "$EV" | head -1)"
fi
grep -q 'ufw allow 9443/tcp' "$ARGV" || fail 'ufw 9443'
grep -q 'ufw allow 443/tcp' "$ARGV" || fail 'ufw 443'
assert_no_secret_leaks
HASH_SEC="$(sha256sum <"$SB/etc/pandora/secrets.env" | awk '{print $1}')"
HASH_CFG="$(sha256sum <"$SB/etc/pandora/config.yaml" | awk '{print $1}')"
HASH_DEP="$(sha256sum <"$SB/etc/pandora/deploy.env" | awk '{print $1}')"

# --- 用例 2：重跑幂等 --------------------------------------------------------
run_install "$REL_ROOT" '' || fail "case2 exit $?: $(tail -5 "$OUT")"
grep -q '升级' "$OUT" || fail 'case2 no upgrade text'
[ "$(sha256sum <"$SB/etc/pandora/secrets.env" | awk '{print $1}')" = "$HASH_SEC" ] || fail 'secrets changed'
[ "$(sha256sum <"$SB/etc/pandora/config.yaml" | awk '{print $1}')" = "$HASH_CFG" ] || fail 'config changed'
[ "$(sha256sum <"$SB/etc/pandora/deploy.env" | awk '{print $1}')" = "$HASH_DEP" ] || fail 'deploy changed'
[ "$(grep -c '^# >>> pandora' "$redis_conf")" = 1 ] || fail 'case2 redis blocks'
[ "$(grep -c '^requirepass ' "$redis_conf")" = 1 ] || fail 'case2 requirepass'
stop_ln="$(lineno 'systemctl stop pandora-serve pandora-worker')"
mig2_ln="$(lineno 'pandora migrate up')"
[ -n "$stop_ln" ] && [ -n "$mig2_ln" ] && [ "$stop_ln" -lt "$mig2_ln" ] || fail 'case2 stop before migrate'
! grep -q 'pandora admin create' "$EV" || fail 'case2 admin create again'

# --- 用例 3：升级 ------------------------------------------------------------
REL_ROOT2="$(make_release t2 '# build=t2')"
run_install "$REL_ROOT2" '' || fail "case3 exit $?: $(tail -5 "$OUT")"
grep -q 'pg_dump' "$EV" || fail 'case3 no pg_dump'
ls "$SB/var/backups/pandora"/pre-upgrade-*.dump >/dev/null 2>&1 || fail 'no pre-upgrade dump'
grep -q '# build=t2' "$SB/opt/pandora/bin/pandora" || fail 'not t2'
grep -q '# build=t1' "$SB/opt/pandora/bin/pandora.prev" || fail 'no prev t1'
grep -q 'PANDORA_RELEASE_VERSION=t2' "$SB/opt/pandora/release.env" || fail 'release t2'
stop3="$(lineno_last 'systemctl stop pandora-serve pandora-worker')"
dump3="$(lineno_last 'pg_dump')"
mig3="$(lineno_last 'pandora migrate up')"
start3="$(lineno_last 'systemctl restart pandora-serve pandora-worker')"
[ -n "$stop3" ] && [ -n "$dump3" ] && [ -n "$mig3" ] && [ -n "$start3" ] || fail 'case3 markers'
[ "$stop3" -lt "$dump3" ] && [ "$dump3" -lt "$mig3" ] && [ "$mig3" -lt "$start3" ] || fail 'case3 order'
[ "$(sha256sum <"$SB/etc/pandora/secrets.env" | awk '{print $1}')" = "$HASH_SEC" ] || fail 'case3 secrets'
assert_no_secret_leaks

# --- 用例 4：首装迁移失败 ----------------------------------------------------
prepare_sandbox
reset_flags
REL_C4="$(make_release t1-c4 '# build=t1-c4')"
touch "$T/flags/migrate_fail"
rc=0; run_install "$REL_C4" '' || rc=$?
[ "$rc" -ne 0 ] || fail 'case4 should fail'
grep -q '迁移失败' "$OUT" || fail 'case4 no migrate failure msg'
if grep -qE 'systemctl (enable|start|restart) pandora-(serve|worker)' "$EV"; then
  fail "case4 started pandora services: $(grep systemctl "$EV")"
fi
grep -q 'pandora admin create' "$EV" && fail 'case4 admin create'
grep -q 'edge-tls setup' "$EV" && fail 'case4 edge-tls'

# --- 用例 5：升级迁移失败 ----------------------------------------------------
rm -f "$T/flags/migrate_fail"
run_install "$REL_C4" '' || fail 'case5 first success'
touch "$T/flags/migrate_fail"
rc=0; run_install "$REL_C4" '' || rc=$?
[ "$rc" -ne 0 ] || fail 'case5 second should fail'
grep -q 'systemctl stop pandora-serve pandora-worker' "$EV" || fail 'case5 no stop'
stop5="$(lineno 'systemctl stop pandora-serve pandora-worker')"
if tail -n +"$((stop5 + 1))" "$EV" | grep -qE 'systemctl (enable|start|restart) pandora-(serve|worker)'; then
  fail 'case5 started services after stop'
fi

# --- 用例 6：无管理员信息的首装 ------------------------------------------------
prepare_sandbox
reset_flags
rm -f "$T/expected_admin_pw"
REL_C6="$(make_release t1-c6 '# build=t1-c6')"
run_install "$REL_C6" '' || fail 'case6 should succeed'
grep -q 'pandora admin create' "$EV" && fail 'case6 admin create'
grep -q 'admin create --email' "$OUT" && grep -q -- '--password-stdin' "$OUT" || fail 'case6 manual admin hint'

# --- 用例 7：健康检查不过 ----------------------------------------------------
prepare_sandbox
reset_flags
REL_C7="$(make_release t1-c7 '# build=t1-c7')"
touch "$T/flags/readyz_fail"
rc=0; run_install "$REL_C7" '' || rc=$?
[ "$rc" -ne 0 ] || fail 'case7 should fail'
grep -q 'journalctl' "$OUT" || fail 'case7 no journalctl hint'

# --- 用例 8：发布包被改过 ----------------------------------------------------
prepare_sandbox
reset_flags
REL_C8="$(make_release t1-c8 '# build=t1-c8')"
printf '\n# tamper\n' >>"$REL_C8/bin/pandora"
rc=0; run_install "$REL_C8" '' || rc=$?
[ "$rc" -ne 0 ] || fail 'case8 should fail'
grep -q 'apt-get' "$EV" && fail 'case8 ran apt-get'
grep -q '^systemctl ' "$EV" && fail 'case8 ran systemctl'

printf 'install mock: 8 cases passed\n'

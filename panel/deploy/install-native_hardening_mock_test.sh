#!/usr/bin/env bash
# 直装布局的 PostgreSQL 与 Valkey 加固，不需要 root、systemd：
#   ① drop-in 内容：两份都整套写全，不依赖发行版单元（Debian 12 的 redis 单元把 ProtectSystem 改回 true）；
#      内存上限与 docker-compose.yml 里的容器上限逐字一致（docker 布局改了，这里跟着红）；被挡的系统调用返回 EPERM；
#      PostgreSQL 不许带 MemoryDenyWriteExecute（超级用户会话的 JIT 要可写可执行内存），Valkey 必须带；
#      Valkey 的系统调用允许清单先清空再写，放行写的目录随 valkey / redis 走；
#   ② Valkey 配置块：与 docker-compose.yml 里 valkey 的启动参数同口径（禁 FLUSHALL / FLUSHDB、不落盘、内存上限与淘汰），
#      另加只听回环与保护模式；重复跑一字不变；不带口令，口令不进命令行参数；
#   ③ 写 drop-in、加固 PostgreSQL 与 Valkey：内容没变不重启；带着新 drop-in 起不来就撤回、照原样拉起、停下；
#   ④ 静态：安装器在建角色、迁移之前加固 PostgreSQL，Valkey 走 native_harden_valkey。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
NATIVE="$DEPLOY/install-native.sh"
LIB="$DEPLOY/install-native-lib.sh"
COMPOSE="$DEPLOY/docker-compose.yml"
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-hardening.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'install-native hardening: %s\n' "$*" >&2; exit 1; }

mkdir -p "$T/bin"
# systemctl：「起不来」开关开着且该单元带着 pandora drop-in 时，restart 与 is-active 失败。
# postgresql@ 例外：Debian 单元的 ExecStart 带「-」前缀，起不来 restart 也返回 0，只能看 pg_lsclusters
cat >"$T/bin/systemctl" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
printf 'systemctl %s\n' "$*" >>"$root/calls"
broken() { [ -f "$root/fail.restart" ] && [ -f "$root/systemd/$1/pandora-hardening.conf" ]; }
case "$1" in
  restart) case "$2" in postgresql@*) exit 0 ;; esac; ! broken "$2.d" ;;
  is-active) ! broken "$3.d" ;;
esac
MOCK
# pg_lsclusters：drop-in 在且「起不来」开关开着时报 down，否则 online
cat >"$T/bin/pg_lsclusters" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
status=online
if [ -f "$root/fail.restart" ] && [ -f "$root/systemd/postgresql@18-main.service.d/pandora-hardening.conf" ]; then status=down; fi
printf 'Ver Cluster Port Status Owner Data directory Log file\n18 main 5432 %s postgres /var/lib/postgresql/18/main /dev/null\n' "$status"
MOCK
printf '#!/usr/bin/env bash\nexit 0\n' >"$T/bin/sleep"
chmod 0755 "$T/bin/"*
export PATH="$T/bin:$PATH"
. "$LIB"
set -euo pipefail
NATIVE_SYSTEMD_DIR="$T/systemd"

# --- ① drop-in 内容 -------------------------------------------------------------------
pg="$(native_pg_hardening_dropin)"
common=('NoNewPrivileges=yes' 'CapabilityBoundingSet=' 'AmbientCapabilities=' \
  'PrivateTmp=yes' 'PrivateDevices=yes' 'ProtectHome=yes' 'ProtectSystem=strict' \
  'ProtectKernelTunables=yes' 'ProtectKernelModules=yes' 'ProtectKernelLogs=yes' 'ProtectControlGroups=yes' \
  'ProtectClock=yes' 'ProtectHostname=yes' 'ProtectProc=invisible' 'RestrictNamespaces=yes' 'RestrictRealtime=yes' \
  'RestrictSUIDSGID=yes' 'LockPersonality=yes' 'RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6' \
  'IPAddressDeny=any' 'IPAddressAllow=localhost' 'SystemCallArchitectures=native' 'SystemCallErrorNumber=EPERM')
for want in "${common[@]}" 'User=postgres' 'Group=postgres' \
    'ExecStartPre=+/usr/bin/install -d -m 2775 -o postgres -g postgres /run/postgresql' \
    'ReadWritePaths=/var/lib/postgresql -/var/log/postgresql /run/postgresql'; do
  grep -qxF "$want" <<<"$pg" || fail "PostgreSQL drop-in lacks: $want"
done
grep -q '^SystemCallFilter=~.*@mount.*@reboot' <<<"$pg" || fail 'PostgreSQL drop-in has no syscall deny list'
if grep -q '^MemoryDenyWriteExecute' <<<"$pg"; then fail 'PostgreSQL drop-in blocks W^X memory (breaks JIT for superuser sessions)'; fi
for flavor in valkey redis; do
  vk="$(native_valkey_hardening_dropin "$flavor")"
  for want in "${common[@]}" 'MemoryDenyWriteExecute=yes' "ReadWritePaths=-/var/lib/$flavor -/var/log/$flavor -/run/$flavor"; do
    grep -qxF "$want" <<<"$vk" || fail "$flavor drop-in lacks: $want"
  done
  # 允许清单：先清空（发行版单元里的不叠进来），再给 @system-service，最后挡 @privileged @resources
  [ "$(grep '^SystemCallFilter=' <<<"$vk")" = $'SystemCallFilter=\nSystemCallFilter=@system-service\nSystemCallFilter=~@privileged @resources' ] \
    || fail "$flavor drop-in syscall filter: $(grep '^SystemCallFilter=' <<<"$vk")"
done
vk="$(native_valkey_hardening_dropin valkey)"
# 内存上限与 docker 布局的容器上限一致
compose_limit() { awk -v svc="  $1:" '$0 == svc { p = 1; next } p && /^  [a-z]/ { p = 0 } p && /memory:/ { print $2; exit }' "$COMPOSE"; }
pg_limit="$(compose_limit postgres)"; vk_limit="$(compose_limit valkey)"
[ -n "$pg_limit" ] && [ -n "$vk_limit" ] || fail "cannot read the container memory limits from docker-compose.yml"
grep -qx "MemoryMax=$pg_limit" <<<"$pg" || fail "PostgreSQL MemoryMax differs from the container limit $pg_limit"
grep -qx "MemoryMax=$vk_limit" <<<"$vk" || fail "Valkey MemoryMax differs from the container limit $vk_limit"

# --- ② Valkey 配置块：与 docker 布局的启动参数同口径 --------------------------------------------
block="$(native_valkey_hardening_block)"
compose_vk="$(awk '/^  valkey:/ { p = 1; next } p && /^  [a-z]/ { p = 0 } p' "$COMPOSE")"
vk_arg() { awk -v k="- --$1" '$0 ~ "^ +" k "$" { getline; sub(/^ +- */, ""); print; exit }' <<<"$compose_vk"; }
[ "$(vk_arg maxmemory)" = 96mb ] && grep -qx 'maxmemory 96mb' <<<"$block" || fail "maxmemory differs from docker-compose.yml ($(vk_arg maxmemory))"
grep -qx "maxmemory-policy $(vk_arg maxmemory-policy)" <<<"$block" || fail 'maxmemory-policy differs from docker-compose.yml'
grep -qx 'appendonly no' <<<"$block" && grep -qx 'save ""' <<<"$block" || fail 'the block persists data while the container does not'
for cmd in FLUSHALL FLUSHDB; do
  grep -A1 -- '- --rename-command' <<<"$compose_vk" | grep -q -- "- $cmd" || fail "docker-compose.yml no longer renames $cmd (update this test and the block together)"
  grep -qx "rename-command $cmd \"\"" <<<"$block" || fail "the block does not disable $cmd"
done
grep -qx 'bind 127.0.0.1 -::1' <<<"$block" && grep -qx 'protected-mode yes' <<<"$block" || fail 'the block does not pin loopback and protected mode'
if grep -qi 'requirepass' <<<"$block"; then fail 'the block carries the password'; fi
# 写进配置：其余行不动、块在末尾、重复跑一字不变、旧块被替换、口令不进命令行参数
conf="$T/valkey.conf"
printf 'port 6379\n# save 3600 1\nrequirepass vk-fixture-pw-aaaaaaaa\n\n' >"$conf"; chmod 0640 "$conf"
for tool in awk cat cmp; do
  real="$(command -v "$tool")"
  printf '#!/usr/bin/env bash\nprintf "%s %%s\\n" "$*" >>"%s/tools.argv"\nexec "%s" "$@"\n' "$tool" "$T" "$real" >"$T/bin/$tool"
  chmod 0755 "$T/bin/$tool"
done
hash -r
native_set_valkey_hardening "$conf" || fail 'a new block was reported as unchanged'
for tool in awk cat cmp; do rm -f "$T/bin/$tool"; done; hash -r
grep -qx 'port 6379' "$conf" && grep -qx 'requirepass vk-fixture-pw-aaaaaaaa' "$conf" || fail 'other lines were changed'
[ "$(tail -n "$(grep -c . <<<"$block")" "$conf")" = "$block" ] || fail 'the block is not at the end of the file'
if grep -q 'vk-fixture-pw' "$T/tools.argv"; then fail 'the Valkey password reached a command line'; fi
cp "$conf" "$T/conf.once"
if native_set_valkey_hardening "$conf"; then fail 'an unchanged block asked for a restart'; fi
cmp -s "$conf" "$T/conf.once" || fail 'a second run changed the file'
sed -i.bak 's/^maxmemory 96mb$/maxmemory 1gb/' "$conf"
native_set_valkey_hardening "$conf" || fail 'a hand-edited block was not restored'
[ "$(grep -c "^$(printf '%s' "$NATIVE_VALKEY_BLOCK_BEGIN" | sed 's/[][\.*^$/]/\\&/g')$" "$conf")" -eq 1 ] || fail 'the block was duplicated'
grep -qx 'maxmemory 96mb' "$conf" && ! grep -qx 'maxmemory 1gb' "$conf" || fail 'the old block was not replaced'
mode="$(stat -c %a "$conf" 2>/dev/null || stat -f %Lp "$conf")"; [ "$mode" = 640 ] || fail "valkey.conf mode became $mode"

# --- ③ 写 drop-in 与加固 ------------------------------------------------------------------
native_apply_dropin demo.service 'x=1' || fail 'a new drop-in was reported as unchanged'
[ "$(cat "$T/systemd/demo.service.d/pandora-hardening.conf")" = 'x=1' ] || fail 'drop-in content'
if native_apply_dropin demo.service 'x=1'; then fail 'an unchanged drop-in asked for a restart'; fi
# PostgreSQL：首次加固重启并确认在线；再跑不重启
: >"$T/calls"
( native_harden_pg_unit 18 ) >"$T/out" 2>&1 || fail "PostgreSQL hardening failed: $(cat "$T/out")"
grep -qx 'systemctl restart postgresql@18-main.service' "$T/calls" || fail "PostgreSQL not restarted after hardening: $(cat "$T/calls")"
[ -f "$T/systemd/postgresql@18-main.service.d/pandora-hardening.conf" ] || fail 'PostgreSQL drop-in not written'
: >"$T/calls"
( native_harden_pg_unit 18 ) >/dev/null 2>&1
if grep -q restart "$T/calls"; then fail 'PostgreSQL restarted although the drop-in did not change'; fi
# 带着新 drop-in 起不来：撤回、照原样拉起、停下
rm -rf "$T/systemd"; touch "$T/fail.restart"; : >"$T/calls"
if ( native_harden_pg_unit 18 ) >"$T/out" 2>&1; then fail 'a PostgreSQL that would not start was accepted'; fi
grep -Fq '已撤回' "$T/out" || fail "PostgreSQL revert message: $(cat "$T/out")"
[ ! -f "$T/systemd/postgresql@18-main.service.d/pandora-hardening.conf" ] || fail 'the failing PostgreSQL drop-in was left behind'
[ "$(grep -c 'systemctl restart postgresql@18-main.service' "$T/calls")" -eq 2 ] || fail "PostgreSQL not restarted without the drop-in: $(cat "$T/calls")"
rm -f "$T/fail.restart"
# Valkey：口令、配置块、drop-in 任一变了才重启
printf 'port 6379\nrequirepass old-pw-fixture\n' >"$conf"; rm -rf "$T/systemd"; : >"$T/calls"
( native_harden_valkey valkey-server "$conf" new-pw-fixture ) >"$T/out" 2>&1 || fail "Valkey hardening failed: $(cat "$T/out")"
grep -qx 'systemctl restart valkey-server.service' "$T/calls" || fail 'Valkey not restarted after hardening'
grep -qx 'requirepass new-pw-fixture' "$conf" && [ -f "$T/systemd/valkey-server.service.d/pandora-hardening.conf" ] || fail 'Valkey password or drop-in not applied'
[ "$(cat "$T/systemd/valkey-server.service.d/pandora-hardening.conf")" = "$(native_valkey_hardening_dropin valkey)" ] || fail 'valkey-server got the wrong drop-in'
( native_harden_valkey redis-server "$conf" new-pw-fixture ) >/dev/null 2>&1 || fail 'redis-server hardening failed'
[ "$(cat "$T/systemd/redis-server.service.d/pandora-hardening.conf")" = "$(native_valkey_hardening_dropin redis)" ] || fail 'redis-server got the wrong drop-in'
: >"$T/calls"
( native_harden_valkey valkey-server "$conf" new-pw-fixture ) >/dev/null 2>&1
if grep -q restart "$T/calls"; then fail 'Valkey restarted although nothing changed'; fi
rm -rf "$T/systemd"; touch "$T/fail.restart"; : >"$T/calls"
if ( native_harden_valkey valkey-server "$conf" new-pw-fixture ) >"$T/out" 2>&1; then fail 'a Valkey that would not start was accepted'; fi
grep -Fq '已撤回' "$T/out" || fail "Valkey revert message: $(cat "$T/out")"
[ ! -f "$T/systemd/valkey-server.service.d/pandora-hardening.conf" ] || fail 'the failing Valkey drop-in was left behind'
rm -f "$T/fail.restart"

# --- ④ 静态：安装器在建角色、迁移之前加固 PostgreSQL ------------------------------------------
awk '/^native_ensure_pg_cluster "\$PG_VERSION"$/ { e = NR } /^native_harden_pg_unit "\$PG_VERSION"$/ { h = NR }
     /^native_pg_peer -p "\$PG_PORT" -d postgres >\/dev\/null <<SQL/ { r = NR }
     END { exit !(e && h && r && e < h && h < r) }' "$NATIVE" || fail 'PostgreSQL is not hardened between creating the cluster and creating roles'
grep -Fq '[[ -z "$VK_UNIT" ]] || native_harden_valkey "$VK_UNIT" "$VK_CONF" "$VK_PASS"' "$NATIVE" || fail 'Valkey is not hardened by the installer'
if grep -Fq 'native_set_valkey_password "$VK_CONF"' "$NATIVE"; then fail 'the installer still sets the Valkey password outside native_harden_valkey'; fi

printf 'install-native hardening mock: PASS\n'

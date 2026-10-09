#!/usr/bin/env bash
# install-native.sh 的 PostgreSQL 集群处理：不需要 root、PostgreSQL 或 systemd。
# 以前的安装器对在线的旧版本集群跑 pg_upgrade，不管成没成、跑没跑，最后都 pg_dropcluster——旧库直接没了。
# 这里证明：
#   ① 静态：install-native.sh 与 install-native-lib.sh 里没有任何删除或停掉集群、删库、删数据目录的命令（任何失败路径上都删不了）；
#   ② 动态：source install-native-lib.sh 取函数，pg_lsclusters / runuser 换成桩，
#      逐个场景跑 native_check_foreign_clusters 与 native_ensure_pg_cluster：
#      旧集群里有 aegis 库而 PG18 不是接班人就停下（首装、升级 .env 指着旧集群、查不了旧集群），
#      PG18 已接班只提示，升级时 .env 的端口不是 PG18 的也停下；建集群只建只启动；
#      每个场景（含停下的）都核对桩记录：pg_dropcluster、pg_upgrade、pg_ctlcluster、dropdb 一次都没被调用。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
NATIVE="$DEPLOY/install-native.sh"
LIB="$DEPLOY/install-native-lib.sh"
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-pgcluster.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'install-native pgcluster: %s\n' "$*" >&2; exit 1; }

# --- ① 静态：删库删集群的命令一个都不许有 -------------------------------------------
code="$(cat "$NATIVE" "$LIB" | grep -v '^[[:space:]]*#')"
for forbidden in pg_dropcluster pg_upgrade pg_ctlcluster dropdb 'DROP DATABASE' 'PANDORA_SKIP_PG_UPGRADE'; do
  if grep -Fq -- "$forbidden" <<<"$code"; then
    fail "install-native.sh still runs $forbidden"
  fi
done
if grep -Eq 'rm[[:space:]]+-[A-Za-z]*r[A-Za-z]*[[:space:]].*(/var/lib/postgresql|NEW_DATA|OLD_DATA)' <<<"$code"; then
  fail 'install-native.sh removes a PostgreSQL data directory'
fi
# 版本钉死：不再取「装着的最新版本」
grep -Fq 'PANDORA_PG_MAJOR=18' "$LIB" || fail 'the PostgreSQL major version is not pinned'
grep -Fq 'PG_VERSION="$PANDORA_PG_MAJOR"' "$NATIVE" || fail 'PG_VERSION does not come from the pinned major'
if grep -Fq 'sort -V | tail -1' <<<"$code"; then fail 'install-native.sh still picks the newest installed PostgreSQL'; fi

# 新建的库用 template0、UTF8（不随集群缺省落成 SQL_ASCII）
grep -Fq "CREATE DATABASE aegis OWNER aegis TEMPLATE template0 ENCODING ''UTF8''" "$NATIVE" \
  || fail 'the aegis database is not created from template0 with UTF8'
# 安装器 source 的函数库进发布包（复制与归档两处清单）、安装器先找它
[ "$(grep -c 'for script in .* install-native-lib.sh .*; do' "$DEPLOY/build-release.sh")" = 2 ] \
  || fail 'build-release.sh does not ship install-native-lib.sh in both script loops'
grep -Fq '. "$SCRIPT_DIR/install-native-lib.sh"' "$NATIVE" || fail 'install-native.sh does not source install-native-lib.sh'

# --- ② 桩 ---------------------------------------------------------------------------
mkdir -p "$T/bin"
# 集群表：$T/clusters，格式同 pg_lsclusters（第一行是表头）
cat >"$T/bin/pg_lsclusters" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
printf 'Ver Cluster Port Status Owner    Data directory              Log file\n'
cat "$root/clusters"
MOCK
# runuser -u postgres -- psql ... -p <端口> ...：按 $T/aegis.<端口> 答「有没有 aegis 库」
#   yes → 1；fail → 退出 2（查不了）；不存在 → 空（没有）
cat >"$T/bin/runuser" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
printf 'runuser %s\n' "$*" >>"$root/calls"
port=""; prev=""
for a in "$@"; do
  [ "$prev" = -p ] && port="$a"
  prev="$a"
done
[ -n "$port" ] || exit 3
f="$root/aegis.$port"
[ -f "$f" ] || exit 0
case "$(cat "$f")" in
  yes) echo 1 ;;
  fail) exit 2 ;;
esac
MOCK
# 建集群：记录，并把 18/main 写进集群表（在线）
cat >"$T/bin/pg_createcluster" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
printf 'pg_createcluster %s\n' "$*" >>"$root/calls"
ver="${@: -2:1}"
printf '%s main 5433 down postgres /var/lib/postgresql/%s/main /dev/null\n' "$ver" "$ver" >>"$root/clusters"
MOCK
cat >"$T/bin/systemctl" <<'MOCK'
#!/usr/bin/env bash
root="$(cd "$(dirname "$0")/.." && pwd)"
printf 'systemctl %s\n' "$*" >>"$root/calls"
if [ "$1" = start ] && [[ "$2" =~ ^postgresql@([0-9]+)-main$ ]]; then
  sed -i.bak -E "s/^(${BASH_REMATCH[1]} main [0-9]+) down /\1 online /" "$root/clusters"
fi
MOCK
# 破坏性命令：一旦被调用就留痕（不论成败）
for destructive in pg_dropcluster pg_upgrade pg_ctlcluster dropdb; do
  cat >"$T/bin/$destructive" <<MOCK
#!/usr/bin/env bash
printf 'DESTRUCTIVE $destructive %s\n' "\$*" >>"$T/destructive"
exit 0
MOCK
done
printf '#!/usr/bin/env bash\nexit 0\n' >"$T/bin/sleep"
chmod 0755 "$T/bin/"*

export PATH="$T/bin:$PATH"
. "$DEPLOY/install-native-lib.sh"
set -euo pipefail
declare -F native_check_foreign_clusters native_ensure_pg_cluster native_pg_cluster_port >/dev/null \
  || fail 'library mode did not define the cluster functions'

reset() { : >"$T/clusters"; rm -f "$T"/aegis.* "$T/calls" "$T/out"; }
cluster() { printf '%s %s %s %s postgres /var/lib/postgresql/%s/%s /dev/null\n' "$1" "$2" "$3" "$4" "$1" "$2" >>"$T/clusters"; }
no_destruction() {
  [ ! -s "$T/destructive" ] || fail "$1: a destructive command ran: $(cat "$T/destructive")"
}
# check <场景> <ok|stop> <期望输出片段> <参数…>
check() {
  local name="$1" want="$2" needle="$3" rc=0; shift 3
  ( native_check_foreign_clusters "$@" ) >"$T/out" 2>&1 || rc=$?
  case "$want" in
    ok) [ "$rc" -eq 0 ] || fail "$name: stopped unexpectedly: $(cat "$T/out")" ;;
    stop) [ "$rc" -ne 0 ] || fail "$name: did not stop: $(cat "$T/out")" ;;
  esac
  [ -z "$needle" ] || grep -Fq -- "$needle" "$T/out" || fail "$name: output lacks '$needle': $(cat "$T/out")"
  no_destruction "$name"
}

# 只有 PG18：放行
reset; cluster 18 main 5432 online
check 'only PG18' ok '' 18 5432 install ''

# 首装：旧 16 在线且有 aegis 库，PG18 里还没有 → 停下，什么都没删
reset; cluster 16 main 5432 online; cluster 18 main 5433 online; echo yes >"$T/aegis.5432"
check 'install with data in PG16' stop 'PostgreSQL 16/main（端口 5432）里有 aegis 库' 18 5433 install ''
grep -Fq '任何集群都没停、没删' "$T/out" || fail 'stop message does not say nothing was touched'
check 'from-docker with data in PG16' stop 'PG18 里还没有' 18 5433 from-docker ''

# 首装：旧 16 有 aegis，但 PG18 也有（已接班）→ 只提示
echo yes >"$T/aegis.5433"
check 'install, PG18 already has aegis' ok '早先的遗留' 18 5433 install ''

# 首装：旧 16 查不了 → 按「有」算，PG18 没有 → 停下
reset; cluster 16 main 5432 online; cluster 18 main 5433 online; echo fail >"$T/aegis.5432"
check 'install, PG16 unreadable' stop '查不清有没有 aegis 库' 18 5433 install ''

# 旧 16 里没有 aegis 库：不碰它，放行
reset; cluster 16 main 5432 online; cluster 18 main 5433 online
check 'install, PG16 without aegis' ok '里面没有 aegis 库' 18 5433 install ''

# 旧 17 没在线：不碰、放行
reset; cluster 17 main 5432 down; cluster 18 main 5433 online
check 'PG17 down' ok '安装器不碰它' 18 5433 install ''
if grep -Fq 'runuser' "$T/calls" 2>/dev/null; then fail 'queried a cluster that is down'; fi

# 升级：.env 的 POSTGRES_PORT 指着旧 16 → 停下
reset; cluster 16 main 5432 online; cluster 18 main 5433 online; echo yes >"$T/aegis.5432"; echo yes >"$T/aegis.5433"
check 'upgrade on PG16' stop '.env 的 POSTGRES_PORT 指着它' 18 5433 upgrade 5432
# 升级：.env 指着 PG18 → 旧 16 是遗留，放行
check 'upgrade on PG18 with PG16 leftover' ok '早先的遗留' 18 5433 upgrade 5433
# 升级：.env 的端口既不是旧集群也不是 PG18 → 停下
reset; cluster 18 main 5433 online
check 'upgrade, port mismatch' stop 'POSTGRES_PORT=5440' 18 5433 upgrade 5440

# 以后的 19 也一样是「别的集群」：不碰
reset; cluster 18 main 5432 online; cluster 19 main 5433 online; echo yes >"$T/aegis.5432"
check 'PG19 alongside' ok '里面没有 aegis 库' 18 5432 upgrade 5432

# --- native_ensure_pg_cluster：没有就建，然后启动；只建只启动 --------------------------
reset; cluster 16 main 5432 online
( native_ensure_pg_cluster 18 ) >"$T/out" 2>&1 || fail "ensure: $(cat "$T/out")"
grep -Fxq 'pg_createcluster --locale=C.UTF-8 --encoding=UTF8 18 main' "$T/calls" || fail "ensure did not create a UTF8 18/main: $(cat "$T/calls")"
grep -Fxq 'systemctl start postgresql@18-main' "$T/calls" || fail 'ensure did not start 18/main'
[ "$(native_pg_cluster_port 18)" = 5433 ] || fail 'cluster port was not read from pg_lsclusters'
grep -q '^16 main 5432 online' "$T/clusters" || fail 'the PG16 cluster was touched'
no_destruction 'ensure (create)'
# 已存在但没起：不重建，只启动
rm -f "$T/calls"; sed -i.bak 's/^18 main 5433 online/18 main 5433 down/' "$T/clusters"
( native_ensure_pg_cluster 18 ) >"$T/out" 2>&1 || fail "ensure (start): $(cat "$T/out")"
if grep -Fq 'pg_createcluster' "$T/calls"; then fail 'ensure recreated an existing cluster'; fi
grep -Fxq 'systemctl start postgresql@18-main' "$T/calls" || fail 'ensure did not start the existing cluster'
no_destruction 'ensure (start)'
# 起不来：停下，同样什么都不删
reset; cluster 18 main 5433 down
cat >"$T/bin/systemctl" <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
chmod 0755 "$T/bin/systemctl"
if ( native_ensure_pg_cluster 18 ) >"$T/out" 2>&1; then fail 'ensure accepted a cluster that never came online'; fi
grep -Fq '起不来' "$T/out" || fail "ensure failure message: $(cat "$T/out")"
no_destruction 'ensure (never online)'

printf 'install-native pgcluster mock: PASS\n'

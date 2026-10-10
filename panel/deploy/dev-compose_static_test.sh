#!/usr/bin/env bash
# 开发数据基座 panel/dev/docker-compose.yml 与生产参数一致（静态，不要 docker、不要 root）：
#   ① PostgreSQL 的 -c 参数与 postgresql-pandora.conf（install.sh 装进 conf.d 的那份）的有效行逐项相同：
#      键的集合一样、每个值一样（键不分大小写；值去掉成对的单 / 双引号再比，timezone 'UTC' 与 UTC 算相同）；
#   ② ports 只绑 127.0.0.1，且没有 network_mode: host；
#   ③ 镜像的超级用户是 postgres、口令取 POSTGRES_SUPER_PASSWORD：deploy/ 下的脚本按直装的身份连它；
#      「#」只在引号外才起注释（两边都一样）：引号里带 # 的值照常比较，不一样就红；
#      postgres 服务的 command 列表只许有 postgres 和成对的「-c」加「键=值」（--键=值、-c键=值、行内写法都红）；
#      另外几条旁路都不许有，否则参数会在 -c 之外悄悄改掉：dev/initdb 下（先去掉 SQL 的 -- 与 /* */ 注释再匹配）
#      出现 ALTER SYSTEM、ALTER DATABASE / ROLE / USER … SET，或任何文件写 postgresql.conf / postgresql.auto.conf，
#      compose 里出现 PGOPTIONS，或 POSTGRES_INITDB_ARGS 带 -c / --set；
#   ④ Valkey 的启动参数与 install.sh 写进 Valkey 配置的 pandora 块（install-lib.sh 的 native_valkey_hardening_block）
#      逐项相同；块里的 bind 与 protected-mode 只管主机上的服务（容器靠 ②），口令不在块里，这三项不比。
# 另把同一套检查跑在几份改坏的副本上（改一个值、删一个参数、端口绑到 0.0.0.0、改 Valkey 内存上限、引号里 # 后的值不同、「#」前无空白不算注释、command 里的 --键=值 与 -c键=值、initdb 里加 ALTER SYSTEM / ALTER … SET / 写 postgresql.auto.conf、compose 里加 PGOPTIONS 或 initdb 的 -c），都必须报错，
# 免得解析失灵时空过。
set -euo pipefail
export LC_ALL=C

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE="$DEPLOY/../dev/docker-compose.yml"
INITDB="$DEPLOY/../dev/initdb"
CONF="$DEPLOY/postgresql-pandora.conf"
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-dev-compose.XXXXXX")"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'dev-compose static: %s\n' "$*" >&2; exit 1; }
[ -r "$COMPOSE" ] || fail "missing $COMPOSE"
[ -r "$CONF" ] || fail "missing $CONF"
[ -d "$INITDB" ] || fail "missing $INITDB"

# 值的规范化：去掉首尾空白与成对的引号
NORM='function norm(v) { gsub(/^[ \t]+|[ \t]+$/, "", v); if (v ~ /^'\''.*'\''$/ || v ~ /^".*"$/) v = substr(v, 2, length(v) - 2); return v }'
# 去注释：「#」只在引号外才算注释；ws=1 时还要求它前面是空白（YAML 列表项的写法）
UNCOMMENT='function uncomment(s, ws,   i, c, q) { q = ""; for (i = 1; i <= length(s); i++) { c = substr(s, i, 1)
  if (q != "") { if (c == q) q = "" } else if (c == "'\''" || c == "\"") q = c
  else if (c == "#" && (!ws || i == 1 || substr(s, i - 1, 1) ~ /[ \t]/)) return substr(s, 1, i - 1) }
  return s }'

# postgresql.conf 的有效行 → 「键=值」一行一个（# 起注释；「键 = 值」与「键 值」两种写法都认）
conf_params() {
  awk "$NORM$UNCOMMENT"'
    { $0 = uncomment($0, 0); if ($0 ~ /^[ \t]*$/) next
      line = $0; sub(/^[ \t]+/, "", line)
      if (match(line, /^[A-Za-z_][A-Za-z0-9_.]*[ \t]*=/)) { key = substr(line, 1, RLENGTH - 1); val = substr(line, RLENGTH + 1) }
      else if (match(line, /^[A-Za-z_][A-Za-z0-9_.]*[ \t]+/)) { key = substr(line, 1, RLENGTH); val = substr(line, RLENGTH + 1) }
      else { print "UNPARSED " $0; next }
      gsub(/[ \t]/, "", key); print tolower(key) "=" norm(val) }' "$1" | sort
}

# compose 里 postgres 服务 command 列表中每个 -c 之后那一项 → 「键=值」；列表里除 postgres 与成对的 -c 以外的项一律 UNPARSED
compose_params() {
  awk "$NORM$UNCOMMENT"'
    /^  [A-Za-z0-9_-]+:/ { svc = $1 }
    /^[A-Za-z]/ { svc = "" }
    svc == "postgres:" && /^    [A-Za-z_]+:/ { incmd = ($1 == "command:")
      if (incmd) { rest = uncomment($0, 1); sub(/^[ \t]*command:[ \t]*/, "", rest); if (rest !~ /^[ \t]*$/) print "UNPARSED inline command: " rest }
      next }
    svc == "postgres:" && incmd && /^ +- / {
      item = $0; sub(/^ +- /, "", item); item = norm(uncomment(item, 1))
      if (want) { want = 0; eq = index(item, "="); if (!eq) { print "UNPARSED " item; next }
        print tolower(substr(item, 1, eq - 1)) "=" norm(substr(item, eq + 1)) }
      else if (item == "-c") want = 1
      else if (item != "postgres") print "UNPARSED command item: " item }
    END { if (want) print "UNPARSED dangling -c" }' "$1" | sort
}

# initdb 下全部文件原样连成一段，不去注释（I4）：去注释要认 shell 通配、dollar 引号、E 串，写错一处就会把后面的
# 真语句吞掉（假绿）；不去注释最多让注释里提到这些词的地方判红（假红，改个说法就行），不会漏。
# 语句按分号与 psql 的 \gexec 切开，免得 ALTER … 与后面别的语句里的 SET 连成一句（假红）
initdb_text() {
  local f
  while IFS= read -r f; do
    sed 's/\\gexec/;/g' "$f" | tr '\n' ' '   # 连成一行：跨行写的 ALTER\n SYSTEM 也认得出
    echo ';'
  done < <(find "$1" -type f -print | sort)
}
# initdb 里只许 .sql 与 .sh（镜像入口还会执行 .sql.gz、.sql.xz 等压缩文件，grep 看不到里面）
initdb_odd_files() { find "$1" -type f ! -name '*.sql' ! -name '*.sh' -print; }

# 全部 ports 列表项（去引号）
compose_ports() {
  awk "$NORM"'
    /^ +ports:/ { inports = 1; ind = match($0, /[^ ]/); next }
    inports && /^ +- / { item = $0; sub(/^ +- /, "", item); sub(/[ \t]+#.*$/, "", item); print norm(item); next }
    inports && match($0, /[^ ]/) <= ind { inports = 0 }' "$1"
}

# compose 里 valkey 服务的启动参数 → 与 Valkey 配置文件同形的「选项 值…」一行一个（空串写成 ""，口令不出）
compose_valkey() {
  awk "$NORM"'
    /^  [A-Za-z0-9_-]+:/ { svc = $1 }
    /^[A-Za-z]/ { svc = "" }
    svc == "valkey:" && /^    [A-Za-z_]+:/ { incmd = ($1 == "command:"); next }
    svc == "valkey:" && incmd && /^ +- / {
      item = $0; sub(/^ +- /, "", item); sub(/[ \t]+#.*$/, "", item); item = norm(item)
      if (item ~ /^--/) { if (line != "") print line; line = substr(item, 3); if (line == "requirepass") { skip = 1; line = "" } else skip = 0; next }
      if (!skip && line != "") line = line " " (item == "" ? "\"\"" : item) }
    END { if (line != "") print line }' "$1" | sort
}

# 生产的 pandora 块（去掉块头块尾、bind、protected-mode）
prod_valkey() {
  ( . "$DEPLOY/install-lib.sh"; native_valkey_hardening_block 127.0.0.1 ) \
    | grep -v -e '^#' -e '^bind ' -e '^protected-mode ' | sort
}
PROD_VALKEY="$(prod_valkey)" || fail "cannot render the production Valkey block from install-lib.sh"
[ "$(wc -l <<<"$PROD_VALKEY" | tr -d ' ')" -ge 4 ] || fail "the production Valkey block looks empty: $PROD_VALKEY"

# 一份 compose 对一份 conf 的全部检查；不过就打印原因并返回 1
check() {
  local compose="$1" conf="$2" initdb="${3:-$INITDB}" want got both dup p n=0
  want="$(conf_params "$conf")"; got="$(compose_params "$compose")"
  [ -n "$want" ] || { echo "no active parameters parsed from $conf"; return 1; }
  both="$(printf '%s\n%s\n' "$want" "$got")"
  if grep -q '^UNPARSED' <<<"$both"; then echo "unparsed: $(grep '^UNPARSED' <<<"$both" | tr '\n' ' ')"; return 1; fi
  dup="$( { cut -d= -f1 <<<"$want" | uniq -d; cut -d= -f1 <<<"$got" | uniq -d; } | tr '\n' ' ')"
  [ -z "${dup// /}" ] || { echo "a PostgreSQL parameter is set twice: $dup"; return 1; }
  if [ "$want" != "$got" ]; then
    echo "PostgreSQL parameters differ (< postgresql-pandora.conf, > dev compose):"
    diff <(printf '%s\n' "$want") <(printf '%s\n' "$got") | grep '^[<>]' || true
    return 1
  fi
  # 旁路：-c 之外不许有别的办法改 PostgreSQL 参数
  local txt odd
  odd="$(initdb_odd_files "$initdb")"
  [ -z "$odd" ] || { echo "only .sql and .sh files may live under dev/initdb (the image also runs compressed ones grep cannot read): $odd"; return 1; }
  # postgres 服务不许改 entrypoint：entrypoint 里也能塞 -c 覆盖参数
  if awk '/^  postgres:/ { p = 1; next } /^  [A-Za-z0-9_-]+:/ || /^[A-Za-z]/ { p = 0 } p && /^    entrypoint:/ { f = 1 } END { exit !f }' "$compose"; then
    echo "the postgres service overrides entrypoint: (it can carry -c parameter overrides)"; return 1
  fi
  txt="$(initdb_text "$initdb")"
  # 同一句里 alter 与 system 之间隔什么都算（空白、注释、换行），宁可多判也不漏
  if grep -Eiq '(^|[^a-z_])alter[^;]*[^a-z_]system([^a-z_]|$)' <<<"$txt"; then
    echo "ALTER SYSTEM under dev/initdb overrides the -c parameters"; return 1
  fi
  if grep -Eiq '(^|[^a-z_])alter[^;]*[^a-z_](database|role|user|group)[^;]*[^a-z_]set([^a-z_]|$)' <<<"$txt"; then
    echo "ALTER DATABASE / ROLE / USER ... SET under dev/initdb overrides the -c parameters"; return 1
  fi
  if grep -rIEiq 'postgresql(\.auto)?\.conf' "$initdb"; then
    echo "a file under dev/initdb mentions postgresql.conf / postgresql.auto.conf: parameters must only come from -c"; return 1
  fi
  if grep -rIq PGOPTIONS "$compose" "$initdb"; then echo "PGOPTIONS overrides PostgreSQL parameters outside -c"; return 1; fi
  if grep -E '^[[:space:]]*POSTGRES_INITDB_ARGS:' "$compose" | grep -Eq '(^|[[:space:]"=])(-c|--set)([[:space:]=]|"|$)'; then
    echo "POSTGRES_INITDB_ARGS carries -c / --set parameter overrides"; return 1
  fi
  while IFS= read -r p; do
    [ -n "$p" ] || continue
    n=$((n + 1))
    [[ "$p" == 127.0.0.1:* ]] || { echo "port not bound to 127.0.0.1 only: $p"; return 1; }
  done < <(compose_ports "$compose")
  [ "$n" -ge 2 ] || { echo "expected the PostgreSQL and Valkey port mappings, found $n"; return 1; }
  if grep -Eq '^[[:space:]]*network_mode:[[:space:]]*"?host' "$compose"; then echo "network_mode: host publishes every listener"; return 1; fi
  awk '/^  postgres:/ { p = 1; next } /^  [A-Za-z0-9_-]+:/ || /^[A-Za-z]/ { p = 0 }
       p && /^ +POSTGRES_USER: *"?postgres"? *$/ { u = 1 }
       p && /^ +POSTGRES_PASSWORD: *"?\$\{POSTGRES_SUPER_PASSWORD[?:}]/ { pw = 1 }
       END { exit !(u && pw) }' "$compose" \
    || { echo "the image superuser must be postgres with password POSTGRES_SUPER_PASSWORD"; return 1; }
  got="$(compose_valkey "$compose")"
  if [ "$got" != "$PROD_VALKEY" ]; then
    echo "Valkey settings differ (< install.sh pandora block, > dev compose):"
    diff <(printf '%s\n' "$PROD_VALKEY") <(printf '%s\n' "$got") | grep '^[<>]' || true
    return 1
  fi
}

out="$(check "$COMPOSE" "$CONF")" || fail "$out"
count="$(conf_params "$CONF" | wc -l | tr -d ' ')"

# 改坏的副本必须被拦下：证明上面不是空过
sed 's/^      - jit=off$/      - jit=on/' "$COMPOSE" >"$T/value.yml"
cmp -s "$COMPOSE" "$T/value.yml" && fail "mutation premise: jit=off not found in the dev compose"
check "$T/value.yml" "$CONF" >/dev/null && fail "a changed value (jit=on) was not detected"
# 删一个参数要连同它前面的 -c 一起删，否则测到的是「悬空的 -c」而不是「少了一项」
awk '{ lines[NR] = $0 } END { for (i = 1; i <= NR; i++) { if (lines[i] == "      - -c" && lines[i + 1] == "      - log_checkpoints=on") { i++; continue } print lines[i] } }' "$COMPOSE" >"$T/missing.yml"
cmp -s "$COMPOSE" "$T/missing.yml" && fail "mutation premise: log_checkpoints=on not found in the dev compose"
check "$T/missing.yml" "$CONF" >/dev/null && fail "a missing parameter (log_checkpoints) was not detected"
sed 's/"127\.0\.0\.1:\${VALKEY_PORT/"0.0.0.0:${VALKEY_PORT/' "$COMPOSE" >"$T/port.yml"
cmp -s "$COMPOSE" "$T/port.yml" && fail "mutation premise: the Valkey port mapping was not found"
check "$T/port.yml" "$CONF" >/dev/null && fail "a port bound to 0.0.0.0 was not detected"
sed 's/^      - 96mb$/      - 128mb/' "$COMPOSE" >"$T/valkey.yml"
cmp -s "$COMPOSE" "$T/valkey.yml" && fail "mutation premise: the Valkey maxmemory argument was not found"
check "$T/valkey.yml" "$CONF" >/dev/null && fail "a changed Valkey maxmemory was not detected"
# conf 那边加引号、换「键 值」写法不算不一致
sed -e "s/^timezone = 'UTC'$/timezone = \"UTC\"/" -e 's/^work_mem = 4MB$/work_mem 4MB/' "$CONF" >"$T/quoted.conf"
cmp -s "$CONF" "$T/quoted.conf" && fail "mutation premise: timezone / work_mem lines not found in postgresql-pandora.conf"
out="$(check "$COMPOSE" "$T/quoted.conf")" || fail "quoting or 'key value' syntax was treated as a difference: $out"

# 引号里 # 之后的值不同必须红（不能把 # 当注释吞掉）：两边各改一次
printf "%s\n" "log_line_prefix = '%t #a'" >"$T/q.conf"
awk '{ print } /^      - timezone=UTC$/ { print "      - -c"; print "      - log_line_prefix='"'"'%t #a'"'"'" }' "$COMPOSE" >"$T/q-same.yml"
[ "$(conf_params "$T/q.conf")" = "$(compose_params "$T/q-same.yml" | grep '^log_line_prefix=')" ] \
  || fail "quoted '#' values are not parsed identically on both sides"
awk '{ print } /^      - timezone=UTC$/ { print "      - -c"; print "      - log_line_prefix='"'"'%t #b'"'"'" }' "$COMPOSE" >"$T/q-diff.yml"
{ cat "$CONF"; printf "%s\n" "log_line_prefix = '%t #a'"; } >"$T/q-hash.conf"
check "$T/q-same.yml" "$T/q-hash.conf" >/dev/null || fail "an identical quoted '#' value was reported as different"
check "$T/q-diff.yml" "$T/q-hash.conf" >/dev/null && fail "a quoted value differing after '#' was not detected"
# 引号外的 # 仍是注释：conf 行尾的 # 注释不进值
{ cat "$CONF"; printf "%s\n" "log_line_prefix = '%t #a' # trailing"; } >"$T/q-trail.conf"
check "$T/q-same.yml" "$T/q-trail.conf" >/dev/null || fail "a trailing comment after a quoted value was taken as part of it"
# compose 里「#」前面有空白才是注释（YAML 列表项的写法）：「jit=off#x」是一个值，与 conf 不同必须红；「jit=off # note」去掉注释后相同必须绿
sed 's/^      - jit=off$/      - jit=off#x/' "$COMPOSE" >"$T/hash-nows.yml"
cmp -s "$COMPOSE" "$T/hash-nows.yml" && fail "mutation premise: jit=off not found in the dev compose"
check "$T/hash-nows.yml" "$CONF" >/dev/null && fail "a '#' without preceding whitespace was taken as a comment (jit=off#x passed)"
sed 's/^      - jit=off$/      - jit=off # note/' "$COMPOSE" >"$T/hash-ws.yml"
out="$(check "$T/hash-ws.yml" "$CONF")" || fail "a ' # note' comment after a list item was taken as part of the value: $out"
# command 列表只许 postgres 与成对的 -c 加「键=值」：--键=值、-c键=值、行内写法都红
sed 's/^      - postgres$/      - postgres\n      - --work_mem=64MB/' "$COMPOSE" >"$T/cmd-long.yml"
cmp -s "$COMPOSE" "$T/cmd-long.yml" && fail "mutation premise: the postgres command item not found in the dev compose"
check "$T/cmd-long.yml" "$CONF" >/dev/null && fail "a --key=value item in the postgres command was not detected"
sed 's/^      - postgres$/      - postgres\n      - -cwork_mem=64MB/' "$COMPOSE" >"$T/cmd-glued.yml"
check "$T/cmd-glued.yml" "$CONF" >/dev/null && fail "a -ckey=value item in the postgres command was not detected"
sed 's/^    command:$/    command: [postgres, -c, jit=off]/' "$COMPOSE" >"$T/cmd-inline.yml"
cmp -s "$COMPOSE" "$T/cmd-inline.yml" && fail "mutation premise: the command: line not found in the dev compose"
check "$T/cmd-inline.yml" "$CONF" >/dev/null && fail "an inline command: [...] was not detected"
# initdb 里的 ALTER SYSTEM / ALTER … SET / 写配置文件、compose 里的 PGOPTIONS、initdb 参数里的 -c 都必须红
cp -R "$INITDB" "$T/initdb-bad"
printf 'ALTER SYSTEM SET work_mem = %s;\n' "'64MB'" >"$T/initdb-bad/90-bad.sql"
check "$COMPOSE" "$CONF" "$T/initdb-bad" >/dev/null && fail "ALTER SYSTEM under initdb was not detected"
cp -R "$INITDB" "$T/initdb-bad2"
printf 'alter\n  system set jit = on;\n' >"$T/initdb-bad2/90-bad.sql"
check "$COMPOSE" "$CONF" "$T/initdb-bad2" >/dev/null && fail "a line-split ALTER SYSTEM under initdb was not detected"
# 逐条写一个坏文件到 initdb 副本里：文件名 → 内容，都必须红
n=0
while IFS='|' read -r name body; do
  n=$((n + 1)); rm -rf "$T/initdb-x"; cp -R "$INITDB" "$T/initdb-x"
  printf '%b\n' "$body" >"$T/initdb-x/$name"
  check "$COMPOSE" "$CONF" "$T/initdb-x" >/dev/null && fail "initdb bypass #$n ($name: $body) was not detected"
done <<'BAD'
90.sql|ALTER DATABASE aegis SET work_mem = '64MB';
90.sql|ALTER ROLE aegis SET work_mem = '64MB';
90.sql|alter user aegis\n  set jit = on;
90.sql|ALTER ROLE aegis IN DATABASE aegis SET jit = on;
90.sql|ALTER/**/SYSTEM SET jit = on;
90.sql|ALTER /* a /* nested */ b */ SYSTEM SET jit = on;
90.sql|ALTER -- note\nSYSTEM SET jit = on;
90.sql|SELECT '--'; ALTER SYSTEM SET jit = on;
90.sh|echo jit=on >> "$PGDATA/postgresql.auto.conf"
90.sh|echo jit=on >> /var/lib/postgresql/data/postgresql.conf
90.sh|psql --command "ALTER SYSTEM SET jit = on"
BAD
# 不去注释：dollar 引号、E 串、shell 通配后面藏的语句都看得见（I4 的三种绕过）
n=0
while IFS='|' read -r name body; do
  n=$((n + 1)); rm -rf "$T/initdb-x"; cp -R "$INITDB" "$T/initdb-x"
  printf '%b\n' "$body" >"$T/initdb-x/$name"
  check "$COMPOSE" "$CONF" "$T/initdb-x" >/dev/null && fail "initdb bypass behind quoting #$n ($name) was not detected"
done <<'BAD'
91.sh|ls /docker-entrypoint-initdb.d/*.sql\npsql -c "ALTER SYSTEM SET jit = on"
91.sql|SELECT $q$ -- $q$; ALTER SYSTEM SET jit = on;
91.sql|SELECT E'\\' -- '; ALTER SYSTEM SET jit = on;
BAD
# I5：initdb 只许 .sql / .sh；postgres 服务不许改 entrypoint
rm -rf "$T/initdb-gz"; cp -R "$INITDB" "$T/initdb-gz"
printf 'x' >"$T/initdb-gz/90-bad.sql.gz"
check "$COMPOSE" "$CONF" "$T/initdb-gz" >/dev/null && fail "a compressed file under initdb was not detected"
sed 's/^    command:$/    entrypoint: ["docker-entrypoint.sh", "postgres", "-c", "work_mem=64MB"]\n    command:/' "$COMPOSE" >"$T/entrypoint.yml"
cmp -s "$COMPOSE" "$T/entrypoint.yml" && fail "mutation premise: the command: line not found in the dev compose"
check "$T/entrypoint.yml" "$CONF" >/dev/null && fail "an entrypoint: override on the postgres service was not detected"
# \gexec 也算语句结尾：ALTER ROLE … \gexec 之后另一句里的 SET 不跟它连成「ALTER ROLE … SET」
rm -rf "$T/initdb-gexec"; cp -R "$INITDB" "$T/initdb-gexec"
printf '%s\n' "SELECT format('ALTER ROLE %I NOLOGIN', 'x') \\gexec" "UPDATE pg_temp.t SET x = 1;" >"$T/initdb-gexec/90-gexec.sql"
out="$(check "$COMPOSE" "$CONF" "$T/initdb-gexec")" || fail "ALTER … \\gexec followed by an unrelated SET was taken as ALTER … SET: $out"
sed 's|^      POSTGRES_USER: postgres$|      POSTGRES_USER: postgres\n      PGOPTIONS: "-c work_mem=64MB"|' "$COMPOSE" >"$T/pgoptions.yml"
cmp -s "$COMPOSE" "$T/pgoptions.yml" && fail "mutation premise: POSTGRES_USER line not found in the dev compose"
check "$T/pgoptions.yml" "$CONF" >/dev/null && fail "PGOPTIONS in the compose was not detected"
sed 's|--auth-local=scram-sha-256"|--auth-local=scram-sha-256 -c work_mem=64MB"|' "$COMPOSE" >"$T/initdbargs.yml"
cmp -s "$COMPOSE" "$T/initdbargs.yml" && fail "mutation premise: POSTGRES_INITDB_ARGS not found in the dev compose"
check "$T/initdbargs.yml" "$CONF" >/dev/null && fail "-c in POSTGRES_INITDB_ARGS was not detected"

echo "dev-compose static: $count PostgreSQL parameters match postgresql-pandora.conf, $(wc -l <<<"$PROD_VALKEY" | tr -d ' ') Valkey settings match install.sh, ports loopback-only, superuser postgres"

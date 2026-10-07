#!/usr/bin/env bash
# 在对照机的某个库上跑一份 EXPLAIN 脚本，按段输出每条 Execution Time。
# 脚本里以「-- A1 说明」「-- q2 说明」这类行开段（psql -q 不回显注释，这里先把它们换成 \echo 标记）。
# 用法（在本机）：explain.sh <库名> <脚本.sql> [on|off|both，默认 both]
# 原始输出留在对照机 /root/bench/runs/<库名>-<脚本名>-jit<on|off>.txt
set -euo pipefail
db="${1:?库名}"; sql="${2:?脚本}"; mode="${3:-both}"
host="${BENCH_HOST:-vultr-sgp-pt-bench}"; ctr="${BENCH_CONTAINER:-bench-pg}"
name="$(basename "$sql" .sql)"; tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
python3 - "$sql" "$tmp" <<'PY'
import re,sys
src,dst=sys.argv[1],sys.argv[2]
out=[]
for line in open(src, encoding='utf-8'):
    m=re.match(r'^--\s*((?:[A-Za-z]+)?[0-9]+[a-z]?)\s+(.*)$', line)
    out.append(f"\\echo == {m.group(1)} {m.group(2)}\n" if m else line)
open(dst,'w',encoding='utf-8').write(''.join(out))
PY
ssh "$host" mkdir -p /root/bench/runs && scp -q "$tmp" "$host:/root/bench/runs/$name.sql"
case "$mode" in both) modes="on off";; on|off) modes="$mode";; *) echo "jit 只能是 on/off/both" >&2; exit 2;; esac
for j in $modes; do
  ssh "$host" bash -s -- "$db" "$ctr" "$name" "$j" <<'REMOTE'
set -euo pipefail
db="$1"; ctr="$2"; name="$3"; j="$4"; out="/root/bench/runs/$db-$name-jit$j.txt"
(echo "SET jit=$j;"; cat "/root/bench/runs/$name.sql") | docker exec -i "$ctr" psql -U postgres -d "$db" -q > "$out" 2>&1 || true
echo "## $db $name jit=$j"
awk '/^== /{if (line!="") print line; line=$2": "; next}
     /Execution Time/{line=line $3"ms "}
     /ERROR/{line=line "[" $0 "] "}
     END{if (line!="") print line}' "$out"
REMOTE
done

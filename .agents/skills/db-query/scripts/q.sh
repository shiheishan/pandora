#!/usr/bin/env bash
# 把一份只读 SQL 文件送到指定库执行（SQL 走标准输入，不在命令行里内联）。
# 用法：q.sh [-t 目标] [-d 库] [-v 名=值]... <queries/xxx.sql>
#   -t local        本机 panel/deploy/psql.sh（要 docker 容器 aegis-postgres 与 deploy/.env），缺省
#   -t <ssh 别名>   面板机/测试机：远端优先 deploy/psql.sh（docker 布局），否则 postgres 用户 peer 连接（直装布局）
#   -t bench        对照机容器 bench-pg，超级用户 postgres；-d 指库名，缺省 aegis（BENCH_HOST、BENCH_CONTAINER 可覆盖）
#   -v 名=值        传给 psql 的变量（tenant、uid、email、days、n、timeout、win），值只许字母数字和 . _ : @ + -
# 安全：文件里出现写语句关键字就拒绝；发送前先 SET default_transaction_read_only = on。
# 口令不经过本脚本：远端 psql.sh 自己读 deploy/.env，本机不碰任何口令。
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
target=local; db=""; vargs=()
while getopts "t:d:v:" o; do
  case "$o" in
    t) target="$OPTARG" ;;
    d) db="$OPTARG" ;;
    v)
      [[ "$OPTARG" =~ ^[A-Za-z_][A-Za-z0-9_]*=[A-Za-z0-9._:@+-]*$ ]] \
        || { echo "q.sh: -v 只接受 名=值，值限字母数字和 . _ : @ + -：$OPTARG" >&2; exit 2; }
      vargs+=(-v "$OPTARG") ;;
    *) sed -n '2,10p' "${BASH_SOURCE[0]}" >&2; exit 2 ;;
  esac
done
shift $((OPTIND - 1))
file="${1:?缺 SQL 文件}"
[[ "$file" == *.sql && -r "$file" ]] || { echo "q.sh: 不是可读的 .sql 文件：$file" >&2; exit 2; }

# 写语句闸：去掉 -- 注释与反斜杠命令行，再找写类关键字
python3 -I - "$file" <<'PY'
import re, sys
text = open(sys.argv[1], encoding="utf-8").read()
kept = [re.sub(r"--.*$", "", l) for l in text.splitlines() if not l.lstrip().startswith("\\")]
body = re.sub(r"'(?:[^']|'')*'", "''", "\n".join(kept))   # 字符串字面量不参与匹配（如 'Lock'）
bad = re.search(r"\b(insert|update|delete|truncate|drop|alter|create|grant|revoke|copy|vacuum|reindex|"
                r"cluster|merge|call|do|lock|refresh|nextval|setval|pg_terminate_backend|"
                r"pg_cancel_backend|pg_stat_statements_reset)\b", body, re.I)
if bad:
    sys.exit("q.sh: 文件里有写类关键字 '%s'，本脚本只跑只读查询；要写库先问用户" % bad.group(1))
PY

prelude=$'\\set ON_ERROR_STOP on\n\\pset pager off\nSET default_transaction_read_only = on;\nSET lock_timeout = \'3s\';\n'
# 远端命令里的 -v 参数：逐个 %q 转义，数组为空时不输出任何东西
vq=""; for a in ${vargs[@]+"${vargs[@]}"}; do vq+=" $(printf '%q' "$a")"; done
send() { { printf '%s' "$prelude"; cat "$file"; } | "$@"; }

case "$target" in
  local)
    [[ -z "$db" ]] || echo "q.sh: local 用 deploy/.env 里的 POSTGRES_DB，忽略 -d" >&2
    send bash "$root/panel/deploy/psql.sh" -X -q ${vargs[@]+"${vargs[@]}"} ;;
  bench)
    host="${BENCH_HOST:-vultr-sgp-pt-bench}"; ctr="${BENCH_CONTAINER:-bench-pg}"; db="${db:-aegis}"
    [[ "$db" =~ ^[a-z0-9_]+$ ]] || { echo "q.sh: 库名不合法：$db" >&2; exit 2; }
    send ssh -o BatchMode=yes "$host" \
      "docker exec -i $ctr psql -U postgres -d $db -X -q$vq" ;;
  *)
    [[ -z "$db" ]] || echo "q.sh: 面板机用 .env 里的 POSTGRES_DB，忽略 -d" >&2
    read -r -d '' remote <<'REMOTE' || true
d=""
for x in /opt/aegispanel /opt/pandora; do [ -r "$x/deploy/.env" ] && { d=$x; break; }; done
[ -n "$d" ] || { echo "找不到 deploy/.env（试过 /opt/aegispanel 与 /opt/pandora）" >&2; exit 1; }
if command -v docker >/dev/null 2>&1 && [ "$(docker inspect -f '{{.State.Running}}' aegis-postgres 2>/dev/null)" = true ]; then
  exec "$d/deploy/psql.sh" -X -q "$@"
fi
envv() { awk -F= -v k="$1" '$1==k{sub(/^[^=]*=/,""); sub(/\r$/,""); v=$0} END{print v}' "$d/deploy/.env"; }
port="$(envv POSTGRES_PORT)"; name="$(envv POSTGRES_DB)"
exec runuser -u postgres -- psql -X -q -p "${port:-5432}" -d "${name:-aegis}" "$@"
REMOTE
    send ssh -o BatchMode=yes "$target" \
      "bash -c $(printf '%q' "$remote") _$vq" ;;
esac

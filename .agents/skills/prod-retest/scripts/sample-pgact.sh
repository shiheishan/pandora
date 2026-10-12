#!/usr/bin/env bash
# 连接池排队旁证（面板机上以 root 跑）：每 N 秒采一次 aegis_app 的 pg_stat_activity 与各网关到 PG 的已建连接数
# 用法：sample-pgact.sh OUT.csv [间隔=10]
# 网关没设 application_name，库里分不出是哪个网关的连接，所以在宿主机用 ss 按进程数各网关已建连接（= 池里打开的连接数）：
#   - 回环 TCP（旧装法）：到 127.0.0.1:5433 的已建连接；
#   - unix socket（install.sh 现装法）：PG 在容器自己的网络命名空间里，宿主机的 ss 看不到服务端一侧，
#     所以另用 nsenter 进容器命名空间读 PG socket 的服务端 inode，再按「对端 inode」对回宿主机上各网关进程的客户端 socket。
#     Valkey 的 socket 对端不在 PG 的服务端集合里，不会混进来。
# 两种之和写进 conn_* 三列。nsenter / docker 取不到时 unix 一侧记 0（TCP 一侧不受影响）。
OUT=$1; IV=${2:-10}
echo "ts_utc,unix_s,active,idle,idle_in_tx,wait_lock,wait_lwlock,wait_io,wait_client,total,max_active_s,conn_public,conn_admin,conn_node" > $OUT
Q="select count(*) filter (where state='active'), count(*) filter (where state='idle'),
 count(*) filter (where state like 'idle in%'), count(*) filter (where wait_event_type='Lock'),
 count(*) filter (where wait_event_type='LWLock'), count(*) filter (where wait_event_type='IO'),
 count(*) filter (where wait_event_type='Client'), count(*),
 coalesce(round(max(extract(epoch from now()-query_start)) filter (where state='active')::numeric,3),0)
 from pg_stat_activity where usename='aegis_app' and backend_type='client backend'"
cd /opt/aegispanel/deploy

# 每个网关到 PG 的 unix socket 已建连接数：参数是三个网关的 MainPID（空格分隔），输出 a,b,c
unix_conns() {
  local pgpid
  pgpid=$(docker inspect -f '{{.State.Pid}}' aegis-postgres 2>/dev/null || echo 0)
  {
    ss -Hxnp 2>/dev/null
    [ "${pgpid:-0}" -gt 0 ] && nsenter -t "$pgpid" -n ss -Hxn 2>/dev/null
  } | awk -v P="$1" '
    BEGIN { n = split(P, pid, " ") }
    {
      l = 0
      for (i = 1; i < NF; i++) if (($i == "*" || $i ~ /^\//) && $(i + 1) ~ /^[0-9]+$/) { l = i; break }
      if (!l) next
      if ($l ~ /\.s\.PGSQL\.[0-9]+$/) { if ($(l + 3) ~ /^[0-9]+$/ && $(l + 3) != 0) srv[$(l + 3)] = 1; next }
      if ($l == "*") { ino[NR] = $(l + 1); txt[NR] = $0 }
    }
    END {
      for (r in ino) if (ino[r] in srv)
        for (g = 1; g <= n; g++) if (pid[g] > 0 && index(txt[r], "pid=" pid[g] ",")) cnt[g]++
      for (g = 1; g <= n; g++) printf "%s%d", (g > 1 ? "," : ""), cnt[g] + 0
      print ""
    }'
}

while :; do
  ts=$(date -u +%FT%TZ); us=$(date +%s)
  row=$(echo "$Q" | ./psql.sh -At -F, 2>/dev/null | tail -1)
  # 各网关进程到 PG 的已建连接：回环 TCP + unix socket
  pids=""; tcp=()
  for g in public admin node; do
    pid=$(systemctl show -p MainPID --value aegis-$g)
    pids="$pids $pid"
    tcp+=("$(ss -Htnp state established '( dport = :5433 )' 2>/dev/null | grep -c "pid=$pid," || true)")
  done
  IFS=, read -r u1 u2 u3 <<< "$(unix_conns "$pids")"
  c=",$((tcp[0] + ${u1:-0})),$((tcp[1] + ${u2:-0})),$((tcp[2] + ${u3:-0}))"
  echo "$ts,$us,$row$c" >> $OUT
  sleep $IV
done

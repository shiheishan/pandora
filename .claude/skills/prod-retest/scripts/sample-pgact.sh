#!/usr/bin/env bash
# 连接池排队旁证（面板机上以 root 跑）：每 N 秒采一次 aegis_app 的 pg_stat_activity 与各网关到 :5433 的已建连接数
# 用法：sample-pgact.sh OUT.csv [间隔=10]
# 网关没设 application_name，库里分不出是哪个网关的连接，所以另用 ss 按进程数各网关已建连接（= 池里打开的连接数）。
OUT=$1; IV=${2:-10}
echo "ts_utc,unix_s,active,idle,idle_in_tx,wait_lock,wait_lwlock,wait_io,wait_client,total,max_active_s,conn_public,conn_admin,conn_node" > $OUT
Q="select count(*) filter (where state='active'), count(*) filter (where state='idle'),
 count(*) filter (where state like 'idle in%'), count(*) filter (where wait_event_type='Lock'),
 count(*) filter (where wait_event_type='LWLock'), count(*) filter (where wait_event_type='IO'),
 count(*) filter (where wait_event_type='Client'), count(*),
 coalesce(round(max(extract(epoch from now()-query_start)) filter (where state='active')::numeric,3),0)
 from pg_stat_activity where usename='aegis_app' and backend_type='client backend'"
cd /opt/aegispanel/deploy
while :; do
  ts=$(date -u +%FT%TZ); us=$(date +%s)
  row=$(echo "$Q" | ./psql.sh -At -F, 2>/dev/null | tail -1)
  # 各网关进程到 127.0.0.1:5433 的已建连接
  c=""
  for g in public admin node; do
    pid=$(systemctl show -p MainPID --value aegis-$g)
    n=$(ss -Htnp state established '( dport = :5433 )' 2>/dev/null | grep -c "pid=$pid,")
    c="$c,$n"
  done
  echo "$ts,$us,$row$c" >> $OUT
  sleep $IV
done

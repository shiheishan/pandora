#!/usr/bin/env bash
# 内存拆账快照（面板机 root，窗口外跑）：memdetail.sh > mem-detail.txt
#   各进程按名汇总 RSS / PSS / 匿名 / 文件 / 共享内存 / swap，网关与容器 cgroup 内存，PG 参数与连接数，
#   网关的 GOGC / GOMEMLIMIT / GOMAXPROCS / 连接池环境变量与 systemd 资源上限。
# 只打印这几个白名单变量，不打印 .env 与 systemd 的 Environment=（里面有连接串与口令）。
# PG 每个进程一行的 smaps_rollup、内核水位与 slab 在 quiet-collect.sh 的 pg-smaps-*.txt、kern-*.txt。
export LC_ALL=C
date -u +%FT%TZ
free -m
echo "== meminfo"
grep -E '^(MemTotal|MemFree|MemAvailable|Buffers|Cached|SwapCached|Active\(anon\)|Inactive\(anon\)|Active\(file\)|Inactive\(file\)|AnonPages|Mapped|Shmem|KReclaimable|Slab|SReclaimable|SUnreclaim|KernelStack|PageTables|Percpu|VmallocUsed|SwapTotal|SwapFree|Committed_AS|AnonHugePages):' /proc/meminfo
echo "== vm"; sysctl vm.swappiness vm.vfs_cache_pressure vm.overcommit_memory vm.min_free_kbytes 2>/dev/null
echo "== per-process (kB): comm n rss pss pss_anon pss_file pss_shmem swap"
for p in $(pgrep -x 'aegis-public|aegis-admin|aegis-node|postgres|valkey-server|redis-server|nginx|dockerd|containerd|containerd-shim|containerd-shim-runc-v2|docker-proxy|systemd-journald|sshd'); do
  c=$(cat "/proc/$p/comm" 2>/dev/null) || continue
  awk -v c="$c" '$1=="Rss:"{r=$2}$1=="Pss:"{p=$2}$1=="Pss_Anon:"{a=$2}$1=="Pss_File:"{f=$2}$1=="Pss_Shmem:"{s=$2}$1=="Swap:"{w=$2}END{print c, r+0, p+0, a+0, f+0, s+0, w+0}' "/proc/$p/smaps_rollup" 2>/dev/null
done | awk '{n[$1]++; for(i=2;i<=7;i++) v[$1,i]+=$i} END{for(k in n) printf "%-24s n=%-3d rss=%-8d pss=%-8d anon=%-8d file=%-8d shmem=%-8d swap=%d\n", k, n[k], v[k,2], v[k,3], v[k,4], v[k,5], v[k,6], v[k,7]}' | sort
echo "== gateway cgroup memory (current / anon / file)"
for g in aegis-public aegis-admin aegis-node nginx; do
  d=/sys/fs/cgroup/system.slice/$g.service
  [[ -r $d/memory.current ]] && echo "$g $(cat "$d/memory.current") $(awk '$1=="anon"||$1=="file"{printf "%s=%s ",$1,$2}' "$d/memory.stat")"
done
echo "== container / database cgroups"
for d in /sys/fs/cgroup/system.slice/docker-*.scope /sys/fs/cgroup/system.slice/postgresql*.service /sys/fs/cgroup/system.slice/valkey*.service; do
  [[ -r $d/memory.current ]] && echo "$(basename "$d" | cut -c1-28) $(cat "$d/memory.current") $(awk '$1=="anon"||$1=="file"||$1=="shmem"{printf "%s=%s ",$1,$2}' "$d/memory.stat")"
done
if command -v docker >/dev/null 2>&1; then
  docker ps --format '{{.ID}} {{.Names}}' 2>/dev/null
  docker stats --no-stream --format '{{.Name}} {{.MemUsage}}' 2>/dev/null
fi
echo "== PG settings and connections"
PSQL=""
for d in /opt/aegispanel/deploy /opt/pandora/deploy; do [[ -x $d/psql.sh ]] && { PSQL=$d/psql.sh; break; }; done
SQL="SELECT name, setting, unit FROM pg_settings WHERE name IN ('shared_buffers','work_mem','maintenance_work_mem','max_connections','effective_cache_size','huge_pages','wal_buffers','autovacuum_max_workers','max_worker_processes','max_parallel_workers','jit','temp_buffers','shared_preload_libraries','track_planning') ORDER BY 1;
SELECT usename, state, count(*) FROM pg_stat_activity GROUP BY 1,2 ORDER BY 1,2;"
if [[ -n $PSQL ]]; then
  printf '%s\n' "$SQL" | "$PSQL" -At 2>&1
elif command -v runuser >/dev/null 2>&1; then
  # 直装布局：系统包 PostgreSQL，以 postgres 身份连本机库（库名取 .env 的 POSTGRES_DB，缺省 aegis）
  db=$(awk -F= '$1=="POSTGRES_DB"{print $2}' /opt/aegispanel/deploy/.env 2>/dev/null); db=${db:-aegis}
  printf '%s\n' "$SQL" | runuser -u postgres -- psql -At -d "$db" 2>&1
fi
echo "== gateway env whitelist and systemd limits"
for g in aegis-public aegis-admin aegis-node; do
  p=$(pgrep -x "$g") && echo "$g $(tr '\0' '\n' < "/proc/$p/environ" | grep -E '^(GOGC|GOMEMLIMIT|GOMAXPROCS|AEGIS_[A-Z]+_DB_M(AX|IN)_CONNS)=' | tr '\n' ' ')"
done
systemctl show aegis-public aegis-admin aegis-node -p Names -p MemoryMax -p MemoryHigh -p CPUQuotaPerSecUSec -p CPUWeight -p NRestarts 2>/dev/null

#!/usr/bin/env bash
# 面板机故障快照（只读）：服务与 timer 状态、三网关探活、数据基座、证书、磁盘与备份、
# 各日志里关键报错的计数、nginx 状态码分布。对应 panel/deploy/RUNBOOK.md 第 0 节。
#
# 用法（本机）：ssh <别名> 'bash -s' < .claude/skills/incident-runbook/scripts/snapshot.sh
#   可选第一个参数：日志只看最后多少行（缺省 5000）：ssh <别名> 'bash -s -- 20000' < …
#
# 不读 .env 的值，不打印网关日志原文（请求日志的 path 可能带订阅令牌），只打计数和最后一条
# 「启动失败」的原因。证书段会打印面板地址：输出不贴进仓库和报告。
set -u
N="${1:-5000}"
[[ "$N" =~ ^[0-9]+$ ]] || N=5000

D=""
for x in /opt/aegispanel /opt/pandora; do [ -f "$x/deploy/.env" ] && { D=$x; break; }; done
echo "== 布局：${D:-找不到 deploy/.env（不是面板机？）}"
[ "$(id -u)" = 0 ] || echo "!! 不是 root：证书、.env 相关的几项会读不到"

echo; echo "== systemd 里 failed 的单元"
systemctl --failed --no-legend --plain 2>/dev/null | sed 's/^/  /'

echo; echo "== 服务与 timer（active / enabled）"
for u in aegis-public aegis-admin aegis-node nginx aegis-tls-renew.timer aegis-backup.timer aegis-health.timer; do
  printf '  %-24s %-10s %s\n' "$u" "$(systemctl is-active "$u" 2>/dev/null)" "$(systemctl is-enabled "$u" 2>/dev/null)"
done

echo; echo "== 探活（healthz 应为 200；readyz 503 = 依赖不通）"
for p in 9000 9001 9003; do
  printf '  :%s healthz %s\n' "$p" "$(curl -s -o /dev/null -w '%{http_code}' -m 3 "http://127.0.0.1:$p/healthz" 2>/dev/null)"
done
for p in 9000 9001; do
  printf '  :%s readyz  %s\n' "$p" "$(curl -s -o /dev/null -w '%{http_code}' -m 5 "http://127.0.0.1:$p/readyz" 2>/dev/null)"
done

echo; echo "== 数据基座"
if command -v docker >/dev/null 2>&1; then
  docker ps -a --filter name=aegis- --format '  {{.Names}}  {{.Status}}' 2>/dev/null
else
  for u in $(systemctl list-units --plain --no-legend 'postgresql@*' 'valkey-server*' 'redis-server*' 2>/dev/null | awk '{print $1}'); do
    printf '  %-28s %s\n' "$u" "$(systemctl is-active "$u")"
  done
fi

echo; echo "== 面板 HTTPS 证书"
if [ -n "$D" ] && [ -x "$D/deploy/edge-tls.sh" ]; then "$D/deploy/edge-tls.sh" status "$D/deploy/.env" 2>&1 | sed 's/^/  /'; fi
[ -f /var/lib/aegispanel/tls/status ] && sed 's/^/  status: /' /var/lib/aegispanel/tls/status

echo; echo "== 磁盘"
df -h / /tmp 2>/dev/null | sed 's/^/  /'
for bk in /var/backups/aegispanel /var/backups/pandora; do
  [ -d "$bk" ] || continue
  newest="$(ls -t "$bk"/*.age 2>/dev/null | head -1)"
  printf '  %s 共 %s，最新加密备份：%s\n' "$bk" "$(du -sh "$bk" 2>/dev/null | cut -f1)" \
    "${newest:+$(basename "$newest")（$(( ($(date +%s) - $(stat -c %Y "$newest")) / 3600 )) 小时前）}"
  printf '  未加密的升级前备份 pre-upgrade-*.dump：%s 个\n' "$(ls "$bk"/pre-upgrade-*.dump 2>/dev/null | wc -l)"
done

echo; echo "== 网关日志最后 $N 行里的关键报错计数"
keys='启动失败|请求失败|支付回调验签失败|支付回调处理失败|支付回调无法解析|主动查单巡检失败|通知派发失败|通知投递失败|节点请求验签失败|节点请求验签暂不可用|清理失败|证书订单的租约已被接手|首选 CA 预计超限|lego 自己会读的变量'
for f in /var/log/aegis/public.log /var/log/aegis/admin.log /var/log/aegis/node.log; do
  [ -f "$f" ] || { echo "  $f 不存在"; continue; }
  echo "  $(basename "$f")："
  tail -n "$N" "$f" | grep -oE "$keys" | sort | uniq -c | sort -rn | sed 's/^/    /'
  # 节点验签失败按原因（text 与 JSON 两种日志格式都认）
  tail -n "$N" "$f" | grep '节点请求验签失败' | grep -oE 'reason"?[=:]"?[^",} ]+' \
    | sed -E 's/^reason"?[=:]"?/reason=/' | sort | uniq -c | sed 's/^/    /'
  last="$(tail -n "$N" "$f" | grep '启动失败' | tail -1)"
  [ -z "$last" ] || echo "    最后一条启动失败：${last:0:300}"
done

echo; echo "== nginx 最后 $N 行的状态码"
[ -f /var/log/nginx/aegis-access.log ] \
  && tail -n "$N" /var/log/nginx/aegis-access.log | grep -oE 'status=[0-9]+' | sort | uniq -c | sort -rn | sed 's/^/  /'

if [ -n "$D" ] && [ -f "$D/logs/health.log" ]; then
  echo; echo "== 巡检 health.log 最后 3 行"; tail -n 3 "$D/logs/health.log" | sed 's/^/  /'
fi

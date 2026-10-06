#!/usr/bin/env bash
# 用法（面板主机，root）：
#   nginx-realip.sh enable <压测机 IP> [更多 IP...]
#   nginx-realip.sh disable
#   nginx-realip.sh status
set -euo pipefail
umask 022

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
TEMPLATE="$SCRIPT_DIR/nginx-loadtest-realip.conf"
TARGET="${LT_REALIP_TARGET:-/etc/aegispanel/cloudflare-realip.conf}"
BACKUP="$TARGET.loadtest-orig"
# 原文件本来就不存在时留这个标记，disable 据此删掉而不是还原

die() { printf 'nginx-realip: %s\n' "$*" >&2; exit 1; }
say() { printf '==> %s\n' "$*" >&2; }

reload_nginx() {
  nginx -t >/dev/null 2>&1 || return 1
  systemctl reload nginx
}

cmd_enable() {
  (( $# > 0 )) || die "用法：nginx-realip.sh enable <压测机 IP> [更多 IP...]"
  [[ -f "$TEMPLATE" ]] || die "找不到模板 $TEMPLATE"
  [[ ! -e "$BACKUP" ]] || die "已处于压测模式（有 $BACKUP），先 disable"
  # render-nginx.sh 在信任表缺失时会写一份默认文件；没有它说明 nginx 还没按 deploy 渲染过
  [[ -f "$TARGET" ]] || die "找不到 $TARGET：先跑 /opt/aegispanel/deploy/render-nginx.sh"
  # 只收单个地址，不收网段：可信来源越宽，能伪造来源 IP 的人越多
  python3 - "$@" <<'PY' || die "压测机地址必须是单个 IPv4 / IPv6 地址，且不能是 0.0.0.0、:: 或回环"
import ipaddress, sys
for raw in sys.argv[1:]:
    ip = ipaddress.ip_address(raw)
    if ip.is_unspecified or ip.is_loopback or str(ip) != raw:
        raise SystemExit(1)
PY
  local tmp ip
  install -d -m 0755 -- "$(dirname -- "$TARGET")"
  tmp="$(mktemp "$TARGET.tmp.XXXXXX")"
  trap 'rm -f -- "$tmp"' EXIT
  {
    grep -v '^set_real_ip_from __LOADGEN_IP__;$' "$TEMPLATE"
    for ip in "$@"; do printf 'set_real_ip_from %s;\n' "$ip"; done
  } > "$tmp"
  chmod 0644 "$tmp"

  cp -p -- "$TARGET" "$BACKUP"
  mv -f -- "$tmp" "$TARGET"
  trap - EXIT
  if ! reload_nginx; then
    say "nginx -t 未通过，回滚"
    restore
    nginx -t || true
    die "已回滚，nginx 未 reload"
  fi
  say "已启用：只对 $* 采信 X-Real-IP；压测结束后跑 nginx-realip.sh disable"
}

restore() {
  [[ -e "$BACKUP" ]] && mv -f -- "$BACKUP" "$TARGET"
  return 0
}

cmd_disable() {
  [[ -e "$BACKUP" ]] || die "不在压测模式（没有 $BACKUP），无需撤销"
  restore
  reload_nginx || die "还原后 nginx -t 未通过，请检查 $TARGET（删掉它再跑 deploy/render-nginx.sh 会写回不信任任何代理的默认文件；站点在 Cloudflare 后面则跑 deploy/update-cloudflare-realip.sh）"
  say "已撤销：$TARGET 还原为压测前的内容并已 reload"
}

cmd_status() {
  if [[ -e "$BACKUP" ]]; then
    printf '压测模式：开启\n'
    grep '^set_real_ip_from\|^real_ip_header' "$TARGET" || true
  else
    printf '压测模式：关闭\n'
  fi
}

[[ "$(id -u)" -eq 0 || -n "${LT_REALIP_TARGET:-}" ]] || die "需要 root"
case "${1:-}" in
  enable) shift; cmd_enable "$@" ;;
  disable) cmd_disable ;;
  status) cmd_status ;;
  *) sed -n '7,10p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' >&2; exit 2 ;;
esac

#!/usr/bin/env bash
# [INPUT]: 依赖 curl 取 Cloudflare 官方的 ips-v4 / ips-v6 列表，依赖 python3 校验网段
# [OUTPUT]: 原子写出 /etc/aegispanel/cloudflare-realip.conf：Cloudflare 全部网段的 set_real_ip_from + real_ip_header CF-Connecting-IP；列表为空、畸形、重复或过大时失败且不动旧文件
# [POS]: deploy 安装链里「站点在 Cloudflare 后面」的显式启用步骤，随发布包装到 /opt/aegispanel/deploy；render-nginx.sh 只在该文件缺失时写不信任任何代理的默认版，两者分工：默认安全、启用显式
# Refresh the trusted Cloudflare proxy networks used by the Pandora edge.
#
# Run it only when the site is behind Cloudflare (orange cloud). On a site that
# is not, trusting CF-Connecting-IP lets any client pick its own source address.
# Re-run it to pick up Cloudflare range changes; then nginx -t && systemctl reload nginx.
#
#   update-cloudflare-realip.sh [target file]   (the argument exists for tests)
set -euo pipefail
umask 077

TARGET_FILE="${1:-/etc/aegispanel/cloudflare-realip.conf}"
[[ "$TARGET_FILE" = /* ]] || { printf 'update-cloudflare-realip: target path must be absolute\n' >&2; exit 1; }
TARGET_DIR="$(dirname -- "$TARGET_FILE")"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf -- "$WORK_DIR"' EXIT

curl -fsS --proto '=https' --tlsv1.2 https://www.cloudflare.com/ips-v4 \
  -o "$WORK_DIR/ips-v4"
curl -fsS --proto '=https' --tlsv1.2 https://www.cloudflare.com/ips-v6 \
  -o "$WORK_DIR/ips-v6"

# Fail closed on empty, malformed, duplicate, or unexpectedly large lists.
python3 - "$WORK_DIR/ips-v4" "$WORK_DIR/ips-v6" <<'PY'
import ipaddress
import pathlib
import sys

seen = set()
for path_text, version in zip(sys.argv[1:], (4, 6)):
    lines = [line.strip() for line in pathlib.Path(path_text).read_text().splitlines() if line.strip()]
    if not 1 <= len(lines) <= 128:
        raise SystemExit("unexpected Cloudflare network count")
    for line in lines:
        network = ipaddress.ip_network(line, strict=True)
        if network.version != version or str(network) != line or line in seen:
            raise SystemExit("invalid Cloudflare network list")
        seen.add(line)
PY

install -d -m 0755 "$TARGET_DIR"
tmp_file="$(mktemp "$TARGET_FILE.tmp.XXXXXX")"
trap 'rm -rf -- "$WORK_DIR"; rm -f -- "$tmp_file"' EXIT
{
  printf '# Generated from https://www.cloudflare.com/ips-v4 and ips-v6\n'
  while IFS= read -r network; do
    [[ -n "$network" ]] && printf 'set_real_ip_from %s;\n' "$network"
  done < "$WORK_DIR/ips-v4"
  while IFS= read -r network; do
    [[ -n "$network" ]] && printf 'set_real_ip_from %s;\n' "$network"
  done < "$WORK_DIR/ips-v6"
  printf 'real_ip_header CF-Connecting-IP;\n'
  printf 'real_ip_recursive on;\n'
} > "$tmp_file"
chmod 0644 "$tmp_file"
mv -f -- "$tmp_file" "$TARGET_FILE"
trap 'rm -rf -- "$WORK_DIR"' EXIT

printf 'Cloudflare real-IP networks updated\n'

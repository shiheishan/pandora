#!/usr/bin/env bash
# Refresh the trusted Cloudflare proxy networks used by the Pandora edge.
set -euo pipefail
umask 077

TARGET_DIR=/etc/aegispanel
TARGET_FILE="$TARGET_DIR/cloudflare-realip.conf"
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

#!/usr/bin/env bash
# [INPUT]: 依赖 .env 的 AEGIS_ADMIN_PATH 与 AEGIS_PUBLIC_BASE_URL（只读解析，不 source），依赖同目录 nginx-aegis.conf 模板
# [OUTPUT]: 原子写出 /etc/nginx/conf.d/aegis.conf：填入后台隐藏前缀与站点域名
# [POS]: deploy 安装链的边缘入口渲染器，被 install.sh 提示、migrate-to-new-host.sh 调用；模板里不含任何具体部署的值
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
# Render the unified edge config without sourcing the secret environment file.
set -euo pipefail
umask 077

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${1:-$SCRIPT_DIR/.env}"
OUTPUT_FILE="${2:-/etc/nginx/conf.d/aegis.conf}"
TEMPLATE_FILE="$SCRIPT_DIR/nginx-aegis.conf"

die() { printf 'render-nginx: %s\n' "$*" >&2; exit 1; }

[[ -f "$ENV_FILE" ]] || die "environment file not found"
[[ -f "$TEMPLATE_FILE" ]] || die "nginx template not found"
[[ "$OUTPUT_FILE" = /* ]] || die "output path must be absolute"

# Read KEY=value lines with awk instead of sourcing: the file holds secrets and
# must never be executed. Each key must appear exactly once.
env_value() {
  local key="$1" values
  mapfile -t values < <(awk -F= -v key="$key" '
    $1 == key {
      sub(/^[^=]*=/, "")
      sub(/\r$/, "")
      print
    }
  ' "$ENV_FILE")
  [[ ${#values[@]} -eq 1 ]] || die "$key must occur exactly once"
  printf '%s' "${values[0]}"
}

admin_path="$(env_value AEGIS_ADMIN_PATH)"

# One high-entropy URL segment keeps the management surface out of commodity
# scans. It is a discovery-control layer, never a replacement for admin auth.
[[ ${#admin_path} -ge 20 && ${#admin_path} -le 64 ]] || \
  die "AEGIS_ADMIN_PATH must be 20-64 characters"
[[ "$admin_path" =~ ^[A-Za-z0-9][A-Za-z0-9_-]*$ ]] || \
  die "AEGIS_ADMIN_PATH must be one URL-safe path segment"
[[ "$admin_path" != "__AEGIS_ADMIN_PATH__" ]] || die "placeholder value is forbidden"
[[ "$admin_path" != CHANGE_ME* ]] || die "example/default value is forbidden"

# The edge serves the same origin the panel builds install commands, payment
# callbacks and subscription links from, so the domain is taken from that URL
# rather than configured twice. HTTPS on the default port only: the template
# listens on 443 and looks up the certificate by this name.
base_url="$(env_value AEGIS_PUBLIC_BASE_URL)"
[[ "$base_url" != *CHANGE_ME* ]] || die "AEGIS_PUBLIC_BASE_URL still holds the example value"
[[ "$base_url" =~ ^https://([^/:]+)/?$ ]] || \
  die "AEGIS_PUBLIC_BASE_URL must be https://<domain> with no port or path"
domain="${BASH_REMATCH[1],,}"
[[ ${#domain} -le 253 ]] || die "domain is too long"
[[ "$domain" =~ ^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]([a-z0-9-]*[a-z0-9])?$ ]] || \
  die "AEGIS_PUBLIC_BASE_URL must name a DNS domain, not an IP address"

for placeholder in __AEGIS_ADMIN_PATH__ __AEGIS_DOMAIN__; do
  grep -q "$placeholder" "$TEMPLATE_FILE" || die "template placeholder $placeholder is missing"
done

output_dir="$(dirname -- "$OUTPUT_FILE")"
[[ -d "$output_dir" ]] || die "output directory does not exist"
tmp_file="$(mktemp "${OUTPUT_FILE}.tmp.XXXXXX")"
trap 'rm -f -- "$tmp_file"' EXIT

sed -e "s/__AEGIS_ADMIN_PATH__/${admin_path}/g" -e "s/__AEGIS_DOMAIN__/${domain}/g" \
  "$TEMPLATE_FILE" >"$tmp_file"
chmod 0644 "$tmp_file"
mv -f -- "$tmp_file" "$OUTPUT_FILE"
trap - EXIT

printf 'render-nginx: unified edge config rendered for %s (admin path redacted)\n' "$domain"

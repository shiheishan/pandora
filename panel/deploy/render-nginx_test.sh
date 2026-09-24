#!/usr/bin/env bash
# [INPUT]: 依赖同目录 render-nginx.sh 与 nginx-aegis.conf
# [OUTPUT]: 渲染器契约测试：后台前缀与站点域名都来自 .env，模板不残留占位符或任何具体部署的域名，非法输入拒绝渲染
# [POS]: deploy 安装链的边缘入口测试，只用虚构域名 panel.example.test，不碰真实 nginx
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf -- "$TEST_DIR"' EXIT

# 「不应出现」的断言不能写成取反的 grep：set -e 对取反的命令不生效，失败也不会退出。
refute() {
  if grep "$@"; then
    printf 'unexpected match: %s\n' "$*" >&2
    exit 1
  fi
}

path='ops_0123456789abcdef0123456789abcdef'
domain='panel.example.test'
base="AEGIS_PUBLIC_BASE_URL=https://$domain"
printf 'AEGIS_ADMIN_PATH=%s\n%s\n' "$path" "$base" >"$TEST_DIR/valid.env"
"$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/valid.env" "$TEST_DIR/aegis.conf" >/dev/null

grep -Fq "location = /$path" "$TEST_DIR/aegis.conf"
# 不带尾斜杠只能重定向：入口页的相对路径要以 /$path/ 为基准，直接下发会把请求打到公开网关
grep -Fq "return 301 /$path/;" "$TEST_DIR/aegis.conf"
grep -Fq "location ^~ /$path/" "$TEST_DIR/aegis.conf"
grep -Fq "rewrite ^/$path(/.*)\$ \$1 break;" "$TEST_DIR/aegis.conf"
refute -Fq '__AEGIS_ADMIN_PATH__' "$TEST_DIR/aegis.conf"
refute -Fq 'listen 127.0.0.1:9081' "$TEST_DIR/aegis.conf"
grep -Fq 'listen 0.0.0.0:7001' "$TEST_DIR/aegis.conf"
grep -Fq 'listen 0.0.0.0:80 default_server' "$TEST_DIR/aegis.conf"
grep -Fq "server_name $domain;" "$TEST_DIR/aegis.conf"
grep -Fq 'listen 0.0.0.0:443 ssl' "$TEST_DIR/aegis.conf"
grep -Fq "/etc/letsencrypt/live/$domain/fullchain.pem" "$TEST_DIR/aegis.conf"
grep -Fq "/etc/letsencrypt/live/$domain/privkey.pem" "$TEST_DIR/aegis.conf"
refute -Fq '__AEGIS_DOMAIN__' "$TEST_DIR/aegis.conf"
# 模板属于产品，不能带任何一套具体部署的域名：server_name 只能是兜底的 _ 或 .env 给的域名
stray="$(grep -vE '^[[:space:]]*#' "$TEST_DIR/aegis.conf" | grep -oE 'server_name[[:space:]]+[^;]*;' \
  | grep -vxE "server_name[[:space:]]+(_|${domain//./\\.});" || true)"
[[ -z "$stray" ]] || { printf 'unexpected server_name: %s\n' "$stray" >&2; exit 1; }
grep -Fq 'include /etc/aegispanel/cloudflare-realip.conf' "$TEST_DIR/aegis.conf"

for invalid in short '../escape-path-0123456789' 'slash/path-0123456789abcdef' 'CHANGE_ME_TO_A_RANDOM_48_CHAR_PATH'; do
  printf 'AEGIS_ADMIN_PATH=%s\n%s\n' "$invalid" "$base" >"$TEST_DIR/invalid.env"
  if "$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/invalid.env" "$TEST_DIR/rejected.conf" >/dev/null 2>&1; then
    printf 'expected invalid path to be rejected\n' >&2
    exit 1
  fi
done

# 域名只接受 https://<DNS 域名>：缺失、重复、示例值、http、带端口或路径、IP 都拒绝
reject_base() {
  printf 'AEGIS_ADMIN_PATH=%s\n' "$path" >"$TEST_DIR/invalid.env"
  printf '%s' "$1" >>"$TEST_DIR/invalid.env"
  if "$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/invalid.env" "$TEST_DIR/rejected.conf" >/dev/null 2>&1; then
    printf 'expected base URL case to be rejected: %q\n' "$1" >&2
    exit 1
  fi
}
reject_base ''
reject_base $'AEGIS_PUBLIC_BASE_URL=https://a.example.test\nAEGIS_PUBLIC_BASE_URL=https://b.example.test\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=https://CHANGE_ME_TO_YOUR_PANEL_DOMAIN\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=http://panel.example.test\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=https://panel.example.test:8443\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=https://panel.example.test/sub\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=https://203.0.113.9\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=https://localhost\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=https://evil.test;include/x\n'

# 大写与结尾斜杠按同一个域名处理
printf 'AEGIS_ADMIN_PATH=%s\nAEGIS_PUBLIC_BASE_URL=https://Panel.Example.TEST/\n' "$path" >"$TEST_DIR/upper.env"
"$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/upper.env" "$TEST_DIR/upper.conf" >/dev/null
grep -Fq "server_name $domain;" "$TEST_DIR/upper.conf"

printf 'render-nginx tests passed\n'

#!/usr/bin/env bash
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
# 渲染结果随本机 nginx 版本变（HTTP/2 写法），测试一律显式指定版本，不受 runner 上装没装 nginx 影响
export PANDORA_NGINX_VERSION=1.26.3
"$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/valid.env" "$TEST_DIR/aegis.conf" "$TEST_DIR/realip.conf" >/dev/null

grep -Fq "location = /$path" "$TEST_DIR/aegis.conf"
# 不带尾斜杠只能重定向：入口页的相对路径要以 /$path/ 为基准，直接下发会把请求打到公开网关
grep -Fq "return 301 /$path/;" "$TEST_DIR/aegis.conf"
grep -Fq "location ^~ /$path/" "$TEST_DIR/aegis.conf"
grep -Fq "rewrite ^/$path(/.*)\$ \$1 break;" "$TEST_DIR/aegis.conf"
refute -Fq '__AEGIS_ADMIN_PATH__' "$TEST_DIR/aegis.conf"
refute -Fq 'listen 127.0.0.1:9081' "$TEST_DIR/aegis.conf"
# 公网明文「测试入口」已删除：除 80/443 与回环 9080 外不得再有别的 listen
grep -Fq 'listen 127.0.0.1:9080;' "$TEST_DIR/aegis.conf"
refute -Fq '7001' "$TEST_DIR/aegis.conf"
stray_listen="$(grep -vE '^[[:space:]]*#' "$TEST_DIR/aegis.conf" | grep -oE 'listen[[:space:]]+[^;]*;' \
  | grep -vxE 'listen[[:space:]]+(127\.0\.0\.1:9080|0\.0\.0\.0:80( default_server)?|\[::\]:80( default_server)?|0\.0\.0\.0:443 ssl( http2)?|\[::\]:443 ssl( http2)?);' || true)"
[[ -z "$stray_listen" ]] || { printf 'unexpected listen: %s\n' "$stray_listen" >&2; exit 1; }
grep -Fq 'listen 0.0.0.0:80 default_server' "$TEST_DIR/aegis.conf"
grep -Fq "server_name $domain;" "$TEST_DIR/aegis.conf"
grep -Fq 'listen 0.0.0.0:443 ssl' "$TEST_DIR/aegis.conf"
# 证书一律经 edge-tls.sh 维护的稳定链接（Let's Encrypt 域名 / IP 证书或自签兜底），不再按域名拼 certbot 路径
grep -Fq 'ssl_certificate /etc/aegispanel/tls/live/fullchain.pem;' "$TEST_DIR/aegis.conf"
grep -Fq 'ssl_certificate_key /etc/aegispanel/tls/live/privkey.pem;' "$TEST_DIR/aegis.conf"
refute -Fq '/etc/letsencrypt' "$TEST_DIR/aegis.conf"
# 面板主机的 80：只放 ACME 校验目录，其余 308 到 https；不再在 443 的 server 里用 if 判端口
server_block() {
  # 打印第 $1 个顶层 server 块
  awk -v want="$1" '/^server \{/ { n++ } n == want { print } n == want && /^\}/ { exit }' "$2"
}
[[ "$(grep -c '^server {' "$TEST_DIR/aegis.conf")" -eq 3 ]] || { printf 'want 3 server blocks\n' >&2; exit 1; }
port80="$(server_block 2 "$TEST_DIR/aegis.conf")"
grep -Fq "server_name $domain;" <<<"$port80"
grep -Fq 'location ^~ /.well-known/acme-challenge/ {' <<<"$port80"
grep -Fq 'root /var/www/aegis-acme;' <<<"$port80"
grep -Fq 'return 308 https://$host$request_uri;' <<<"$port80"
if grep -Eq 'listen[^;]*443|proxy_pass' <<<"$port80"; then printf 'port-80 server must not proxy or serve TLS\n' >&2; exit 1; fi
main="$(server_block 3 "$TEST_DIR/aegis.conf")"
if grep -Eq 'listen[^;]*:80[; ]' <<<"$main"; then printf 'the TLS server still listens on 80\n' >&2; exit 1; fi
refute -Fq 'if ($server_port' "$TEST_DIR/aegis.conf"
refute -Fq '__AEGIS_DOMAIN__' "$TEST_DIR/aegis.conf"
# 模板属于产品，不能带任何一套具体部署的域名：server_name 只能是兜底的 _ 或 .env 给的域名
stray="$(grep -vE '^[[:space:]]*#' "$TEST_DIR/aegis.conf" | grep -oE 'server_name[[:space:]]+[^;]*;' \
  | grep -vxE "server_name[[:space:]]+(_|${domain//./\\.});" || true)"
[[ -z "$stray" ]] || { printf 'unexpected server_name: %s\n' "$stray" >&2; exit 1; }
grep -Fq 'include /etc/aegispanel/cloudflare-realip.conf' "$TEST_DIR/aegis.conf"

# 压缩：网关给 JS 发 text/javascript，必须在 gzip_types 里；级别 5
gzip_types="$(grep -E '^[[:space:]]*gzip_types ' "$TEST_DIR/aegis.conf")"
for type in text/css text/javascript application/javascript application/json image/svg+xml; do
  grep -Fqw "$type" <<<"$gzip_types" || { printf 'gzip_types misses %s\n' "$type" >&2; exit 1; }
done
grep -Eq '^[[:space:]]*gzip_comp_level 5;' "$TEST_DIR/aegis.conf"
# HTTP/2 下每个并发请求都计入 limit_conn，24 会把冷加载的资源 503 掉
grep -Eq '^[[:space:]]*limit_conn aegis_conn 64;' "$TEST_DIR/aegis.conf"

# 节点网关单独限速：按「来源 IP + 节点标识」分桶，外加每 IP 总上限；limit_conn 单独放宽。
# 一台机器挂 60 个节点约 810 次/分、60 条事件流，按每 IP 的 aegis_api 与 limit_conn 64 会被 503
grep -Eq '^limit_req_zone "\$binary_remote_addr\$aegis_signed_node\$aegis_compat_node" zone=aegis_node:[0-9]+m rate=[0-9]+r/m;' "$TEST_DIR/aegis.conf"
grep -Eq '^limit_req_zone \$binary_remote_addr zone=aegis_node_ip:[0-9]+m rate=[0-9]+r/m;' "$TEST_DIR/aegis.conf"
grep -Eq '^limit_conn_zone \$binary_remote_addr zone=aegis_node_conn:[0-9]+m;' "$TEST_DIR/aegis.conf"
grep -Fq 'map $http_x_node_id $aegis_signed_node {' "$TEST_DIR/aegis.conf"
grep -Fq 'map $arg_node_id $aegis_compat_node {' "$TEST_DIR/aegis.conf"
node_location() {
  # 打印某个 location 块（从 location 行到下一个只有 } 的行）
  awk -v want="$1" 'index($0, want) { on = 1 } on { print } on && /^    }$/ { exit }' "$TEST_DIR/aegis.conf"
}
for loc in 'location ^~ /api/v1/server/UniProxy/ {' 'location ^~ /v1/nodes/ {'; do
  block="$(node_location "$loc")"
  [[ -n "$block" ]] || { printf 'missing %s\n' "$loc" >&2; exit 1; }
  grep -Eq 'limit_req zone=aegis_node burst=[0-9]+ nodelay;' <<<"$block" || { printf '%s: no per-node limit\n' "$loc" >&2; exit 1; }
  grep -Eq 'limit_req zone=aegis_node_ip burst=[0-9]+ nodelay;' <<<"$block" || { printf '%s: no per-IP cap\n' "$loc" >&2; exit 1; }
  grep -Eq 'limit_conn aegis_node_conn [0-9]+;' <<<"$block" || { printf '%s: no node limit_conn\n' "$loc" >&2; exit 1; }
  if grep -Fq 'zone=aegis_api' <<<"$block"; then printf '%s still uses the per-IP aegis_api zone\n' "$loc" >&2; exit 1; fi
  conns="$(grep -Eo 'limit_conn aegis_node_conn [0-9]+;' <<<"$block" | grep -Eo '[0-9]+')"
  (( conns >= 128 )) || { printf '%s: limit_conn %s too low for 60 node event streams\n' "$loc" "$conns" >&2; exit 1; }
done

# HTTP/2 按 nginx 版本渲染：1.25.1 起 http2 on;，更老或探不到版本用 listen ... ssl http2
render_http2() {
  PANDORA_NGINX_VERSION="$1" "$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/valid.env" "$TEST_DIR/h2.raw" "$TEST_DIR/realip.conf" >/dev/null
  # 只看指令，不看注释
  grep -vE '^[[:space:]]*#' "$TEST_DIR/h2.raw" >"$TEST_DIR/h2.conf" || true
}
expect_modern() {
  [[ "$(grep -cE '^[[:space:]]*http2 on;' "$TEST_DIR/h2.conf")" -eq 1 ]] || { printf 'nginx %s: want one http2 on;\n' "$1" >&2; exit 1; }
  refute -Eq 'listen[^;]*http2' "$TEST_DIR/h2.conf"
}
expect_legacy() {
  refute -Eq '^[[:space:]]*http2 on;' "$TEST_DIR/h2.conf"
  grep -Fq 'listen 0.0.0.0:443 ssl http2;' "$TEST_DIR/h2.conf"
  grep -Fq 'listen [::]:443 ssl http2;' "$TEST_DIR/h2.conf"
  # 只有 443 走 HTTP/2：80 与回环 9080 不带
  [[ "$(grep -cE 'listen[^;]*http2' "$TEST_DIR/h2.conf")" -eq 2 ]] || { printf 'nginx %s: http2 leaked onto a non-443 listen\n' "$1" >&2; exit 1; }
}
for v in 1.25.1 1.26.3 1.27.4 'nginx version: nginx/1.29.0 (Debian)' 2.0.0; do render_http2 "$v"; expect_modern "$v"; done
for v in 1.25.0 1.24.0 1.22.1 1.18.0 'nginx version: openresty/1.21.4.1' garbage; do render_http2 "$v"; expect_legacy "$v"; done

for invalid in short '../escape-path-0123456789' 'slash/path-0123456789abcdef' 'CHANGE_ME_TO_A_RANDOM_48_CHAR_PATH'; do
  printf 'AEGIS_ADMIN_PATH=%s\n%s\n' "$invalid" "$base" >"$TEST_DIR/invalid.env"
  if "$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/invalid.env" "$TEST_DIR/rejected.conf" "$TEST_DIR/realip-rejected.conf" >/dev/null 2>&1; then
    printf 'expected invalid path to be rejected\n' >&2
    exit 1
  fi
done

# 对外地址只接受 https://<DNS 域名或公网 IPv4>：规则与 public-base-url.sh、platform/config 共用用例表；
# 另有缺失、重复、注入
reject_base() {
  printf 'AEGIS_ADMIN_PATH=%s\n' "$path" >"$TEST_DIR/invalid.env"
  printf '%s' "$1" >>"$TEST_DIR/invalid.env"
  if "$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/invalid.env" "$TEST_DIR/rejected.conf" "$TEST_DIR/realip-rejected.conf" >/dev/null 2>&1; then
    printf 'expected base URL case to be rejected: %q\n' "$1" >&2
    exit 1
  fi
}
reject_base ''
reject_base $'AEGIS_PUBLIC_BASE_URL=https://a.example.test\nAEGIS_PUBLIC_BASE_URL=https://b.example.test\n'
reject_base $'AEGIS_PUBLIC_BASE_URL=https://evil.test;include/x\n'
cases=0
while read -r verdict url; do
  case "$verdict" in ''|'#'*) continue ;; esac
  cases=$((cases + 1))
  if [[ "$verdict" = reject ]]; then
    reject_base "AEGIS_PUBLIC_BASE_URL=$url"$'\n'
  else
    printf 'AEGIS_ADMIN_PATH=%s\nAEGIS_PUBLIC_BASE_URL=%s\n' "$path" "$url" >"$TEST_DIR/case.env"
    "$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/case.env" "$TEST_DIR/case.conf" "$TEST_DIR/realip.conf" >/dev/null \
      || { printf 'rejected valid base URL %s\n' "$url" >&2; exit 1; }
    host="${url#https://}"; host="${host%/}"; host="${host,,}"
    grep -Fq "server_name $host;" "$TEST_DIR/case.conf" || { printf '%s: server_name not %s\n' "$url" "$host" >&2; exit 1; }
  fi
done <"$SCRIPT_DIR/fixtures/public-base-url-cases.txt"
(( cases >= 30 )) || { printf 'shared case table looks truncated\n' >&2; exit 1; }

# 只有公网 IPv4：server_name 就是这个 IP（Let's Encrypt 的 HTTP-01 按 Host: <IP> 来校验），证书仍走 live
printf 'AEGIS_ADMIN_PATH=%s\nAEGIS_PUBLIC_BASE_URL=https://203.0.113.10\n' "$path" >"$TEST_DIR/ip.env"
"$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/ip.env" "$TEST_DIR/ip.conf" "$TEST_DIR/realip.conf" >/dev/null
[[ "$(grep -c 'server_name 203.0.113.10;' "$TEST_DIR/ip.conf")" -eq 2 ]] || { printf 'IP render: want server_name on 80 and 443\n' >&2; exit 1; }
grep -Fq 'ssl_certificate /etc/aegispanel/tls/live/fullchain.pem;' "$TEST_DIR/ip.conf"
grep -Fq 'location ^~ /.well-known/acme-challenge/ {' "$TEST_DIR/ip.conf"

# 大写与结尾斜杠按同一个域名处理
printf 'AEGIS_ADMIN_PATH=%s\nAEGIS_PUBLIC_BASE_URL=https://Panel.Example.TEST/\n' "$path" >"$TEST_DIR/upper.env"
"$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/upper.env" "$TEST_DIR/upper.conf" "$TEST_DIR/realip.conf" >/dev/null
grep -Fq "server_name $domain;" "$TEST_DIR/upper.conf"

# 拒绝渲染时不留下任何东西（信任表也不建）
[[ ! -e "$TEST_DIR/realip-rejected.conf" ]] || { printf 'rejected render created the real-IP file\n' >&2; exit 1; }

# error_log：不能是 /dev/null（连接数顶满时「worker_connections are not enough」只在这里），留 crit 写文件
refute -Eq '^[[:space:]]*error_log[[:space:]]+/dev/null' "$TEST_DIR/aegis.conf"
[[ "$(grep -cE '^[[:space:]]*error_log /var/log/nginx/aegis-error\.log crit;' "$TEST_DIR/aegis.conf")" -eq 3 ]] \
  || { printf 'all three servers must log crit to /var/log/nginx/aegis-error.log\n' >&2; exit 1; }

# 主配置的连接上限（5k-r4：Debian 缺省 768 × 2 个 worker，约一千条 SSE 就把节点请求挤成 500）。
# 输出在 <dir>/conf.d/ 下时改 <dir>/nginx.conf：worker_connections 至少 8192、worker_rlimit_nofile 至少 65536
NGX="$TEST_DIR/etc-nginx"
mkdir -p "$NGX/conf.d"
debian_main() {
  printf 'user www-data;\nworker_processes auto;\npid /run/nginx.pid;\ninclude /etc/nginx/modules-enabled/*.conf;\n\nevents {\n\tworker_connections 768;\n\t# multi_accept on;\n}\n\nhttp {\n\tinclude /etc/nginx/conf.d/*.conf;\n}\n' >"$NGX/nginx.conf"
}
render_into_confd() { "$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/valid.env" "$NGX/conf.d/aegis.conf" "$TEST_DIR/realip.conf" >/dev/null; }
debian_main
render_into_confd
grep -Eq '^[[:space:]]*worker_connections 8192;' "$NGX/nginx.conf"
[[ "$(grep -c 'worker_rlimit_nofile 65536;' "$NGX/nginx.conf")" -eq 1 ]]
# worker_rlimit_nofile 在主段（events 之前），worker_connections 在 events 里
awk '/worker_rlimit_nofile/ { r = NR } /^events/ { e = NR } /worker_connections/ { w = NR } /^}/ && e && !c { c = NR }
  END { exit !(r && e && w && r < e && e < w && w < c) }' "$NGX/nginx.conf" \
  || { printf 'tuned directives landed in the wrong blocks:\n' >&2; cat "$NGX/nginx.conf" >&2; exit 1; }
grep -Fq '# multi_accept on;' "$NGX/nginx.conf"
grep -Fq 'include /etc/nginx/conf.d/*.conf;' "$NGX/nginx.conf"
# 幂等
cp "$NGX/nginx.conf" "$TEST_DIR/main.once"
render_into_confd
cmp -s "$TEST_DIR/main.once" "$NGX/nginx.conf" || { printf 'second render changed nginx.conf again\n' >&2; exit 1; }
# 已经更高的不降；更低的 rlimit 抬上去；events 里没写 worker_connections 就补一行
printf 'worker_processes 4;\nworker_rlimit_nofile 100000;\nevents {\n    worker_connections 20000;\n}\nhttp {\n}\n' >"$NGX/nginx.conf"
render_into_confd
grep -Fq 'worker_rlimit_nofile 100000;' "$NGX/nginx.conf" && grep -Fq 'worker_connections 20000;' "$NGX/nginx.conf" \
  || { printf 'higher limits were lowered\n' >&2; exit 1; }
printf 'worker_processes 2;\nworker_rlimit_nofile 1024;\nevents {\n    use epoll;\n}\nhttp {\n}\n' >"$NGX/nginx.conf"
render_into_confd
[[ "$(grep -c 'worker_rlimit_nofile' "$NGX/nginx.conf")" -eq 1 ]] && grep -Fq 'worker_rlimit_nofile 65536;' "$NGX/nginx.conf" \
  && grep -Eq '^[[:space:]]*worker_connections 8192;' "$NGX/nginx.conf" \
  || { printf 'missing worker_connections / low rlimit not fixed:\n' >&2; cat "$NGX/nginx.conf" >&2; exit 1; }
# 认不出的结构（events 写在一行里）不改，渲染照常完成
printf 'events { worker_connections 512; }\nhttp {\n}\n' >"$NGX/nginx.conf"
cp "$NGX/nginx.conf" "$TEST_DIR/main.oneline"
"$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/valid.env" "$NGX/conf.d/aegis.conf" "$TEST_DIR/realip.conf" >/dev/null 2>&1
cmp -s "$TEST_DIR/main.oneline" "$NGX/nginx.conf" || { printf 'unrecognised layout was rewritten\n' >&2; exit 1; }
# 显式 none：不碰；渲染被拒：不碰
debian_main
cp "$NGX/nginx.conf" "$TEST_DIR/main.debian"
PANDORA_NGINX_MAIN_CONF=none render_into_confd
cmp -s "$TEST_DIR/main.debian" "$NGX/nginx.conf" || { printf 'PANDORA_NGINX_MAIN_CONF=none still tuned nginx.conf\n' >&2; exit 1; }
printf 'AEGIS_ADMIN_PATH=short\n%s\n' "$base" >"$TEST_DIR/invalid.env"
if "$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/invalid.env" "$NGX/conf.d/aegis.conf" "$TEST_DIR/realip.conf" >/dev/null 2>&1; then
  printf 'expected invalid path to be rejected\n' >&2; exit 1
fi
cmp -s "$TEST_DIR/main.debian" "$NGX/nginx.conf" || { printf 'rejected render tuned nginx.conf\n' >&2; exit 1; }
# 输出不在 conf.d 下（其余用例都是）：不去找主配置
[[ ! -e "$TEST_DIR/nginx.conf" ]]

printf 'render-nginx tests passed\n'

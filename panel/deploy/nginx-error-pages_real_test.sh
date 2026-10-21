#!/usr/bin/env bash
# 真 nginx 上验 JSON 错误页（http 级 map $uri $pandora_api + error_page 502 + 命名 location @pandora_502）：
# 用 render-nginx.sh 渲染正式模板、装进系统 nginx（Debian / Ubuntu 的 nginx 包），面板不起，
# 9000 / 9001 上没人听，nginx 连上游被拒 → 502：
#   - 门户 API（/v1/…）与后台 API（/<后台前缀>/v1/…，rewrite 去前缀之后）回与面板同形的 JSON 信封，
#     带 Cache-Control: no-store 与 nosniff，request_id 是 nginx 的 $request_id；
#   - 回环运维入口 127.0.0.1:9080 与主 443 server 同一个 server 块，两边都验；
#   - 非 API 路径（门户首页、后台首页）仍回 nginx 默认的 502 HTML 页。
# render-nginx_test.sh 只核渲染出的文本，本测试核真 nginx 的行为（map 与命名 location 的求值、
# types { } 后的 Content-Type）。
# 要 root、会写 /etc/pandora 与 /etc/nginx：只在一次性的 CI runner 上跑
# （.github/workflows/panel-deploy.yml 的 deploy-nginx-error-pages），/etc/pandora 已存在就拒绝。
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

die() { printf 'nginx-error-pages: %s\n' "$*" >&2; exit 1; }

[[ "$(id -u)" -eq 0 ]] || die "需要 root（写 /etc/pandora 与 /etc/nginx/conf.d）"
for c in nginx curl python3 openssl systemctl; do
  command -v "$c" >/dev/null 2>&1 || die "缺少命令 $c"
done
[[ ! -e /etc/pandora ]] || die "/etc/pandora 已存在：本测试只在一次性的 CI runner 上跑，不碰已有安装"
[[ ! -e /etc/nginx/conf.d/pandora.conf ]] || die "/etc/nginx/conf.d/pandora.conf 已存在"
# 上游端口上有人在听就测不到 502
for port in 9000 9001; do
  if (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null; then
    die "127.0.0.1:$port 上有进程在听，测不到上游不可用时的 502"
  fi
done

TEST_DIR="$(mktemp -d)"
trap 'rm -rf -- "$TEST_DIR"' EXIT

domain='panel.example.test'
admin_path="adm_$(openssl rand -hex 16)"
printf 'public_base_url: https://%s\n' "$domain" >"$TEST_DIR/config.yaml"
printf 'PANDORA_ADMIN_PATH=%s\n' "$admin_path" >"$TEST_DIR/deploy.env"

# 模板写死的证书位置（生产上由 edge-tls.sh 维护）：这里放一张一天有效的自签证书
install -d -m 0755 /etc/pandora /etc/pandora/tls /etc/pandora/tls/live
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=$domain" \
  -keyout /etc/pandora/tls/live/privkey.pem -out /etc/pandora/tls/live/fullchain.pem 2>/dev/null
chmod 0600 /etc/pandora/tls/live/privkey.pem

# nginx 包自带的默认站点也 listen 80 default_server，与 pandora.conf 冲突（edge-tls.sh 装机时同样停用它）
rm -f -- /etc/nginx/sites-enabled/default
# 真实 IP 信任表由渲染器按缺省路径建「不信任任何代理」的默认文件；主配置的 worker 调参照装机时一样做
bash "$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/config.yaml" "$TEST_DIR/deploy.env" /etc/nginx/conf.d/pandora.conf
nginx -t
systemctl restart nginx

# 请求一次，校验状态码与 Content-Type；正文写到 $TEST_DIR/body，响应头写到 $TEST_DIR/headers
fetch() {
  local want_status="$1" want_type="$2"
  shift 2
  local got
  got="$(curl -sS -o "$TEST_DIR/body" -D "$TEST_DIR/headers" -w '%{http_code} %{content_type}' --max-time 10 "$@")" \
    || die "请求失败：$*"
  case "$got" in
    "$want_status $want_type"*) ;;
    *) cat "$TEST_DIR/body" >&2; die "期望 $want_status $want_type，实际 $got（${*: -1}）" ;;
  esac
}

header_is() {
  local name="$1" value="$2"
  tr -d '\r' <"$TEST_DIR/headers" | grep -qiE "^${name}: ${value}\$" || {
    cat "$TEST_DIR/headers" >&2
    die "响应头缺 $name: $value"
  }
}

assert_envelope() {
  local what="$1"
  header_is 'Cache-Control' 'no-store'
  header_is 'X-Content-Type-Options' 'nosniff'
  python3 - "$TEST_DIR/body" "$what" <<'PY'
import json, re, sys
body, what = sys.argv[1], sys.argv[2]
doc = json.load(open(body, encoding="utf-8"))
assert set(doc) == {"error"}, (what, doc)
e = doc["error"]
assert set(e) == {"code", "message", "request_id"}, (what, e)
assert e["code"] == "service_unavailable", (what, e)
assert e["message"] == "服务暂时不可用，请稍后重试", (what, e)
assert re.fullmatch(r"[0-9a-f]{32}", e["request_id"]), (what, e)
PY
  printf 'ok  %s\n' "$what"
}

loop='http://127.0.0.1:9080'
tls=(-k --resolve "$domain:443:127.0.0.1")

fetch 502 application/json "$loop/v1/site-config"
assert_envelope '回环入口 门户 API /v1/site-config → 502 信封'

fetch 502 application/json "$loop/$admin_path/v1/dashboard"
assert_envelope '回环入口 后台 API /<前缀>/v1/dashboard → 502 信封'

fetch 502 application/json "${tls[@]}" "https://$domain/v1/site-config"
assert_envelope '443 门户 API /v1/site-config → 502 信封'

fetch 502 application/json "${tls[@]}" "https://$domain/$admin_path/v1/dashboard"
assert_envelope '443 后台 API /<前缀>/v1/dashboard → 502 信封'

fetch 502 text/html "$loop/"
printf 'ok  %s\n' '回环入口 门户首页 / → nginx 默认 502 页'

fetch 502 text/html "$loop/$admin_path/"
printf 'ok  %s\n' '回环入口 后台首页 /<前缀>/ → nginx 默认 502 页'

echo "nginx error pages: all checks passed"

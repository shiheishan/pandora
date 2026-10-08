#!/usr/bin/env bash
# edge-tls.sh 的桩测试：不需要 root、nginx、certbot、lego 或网络。
#
# nginx / systemctl / ufw / certbot / lego / apt-get / curl 换成记账的桩（certbot 与 lego 的桩
# 用 openssl 现签一张覆盖目标主机的证书），路径都指到临时目录，逐个验证：
#   ① 只有公网 IP：lego webroot + shortlived 申请 IP 证书，live 指向它，渲染 nginx、启用续期 timer；
#   ② 申请失败：自签兜底（覆盖该 IP），退出 3 并给出补救命令；renew 重试成功即无缝换上并告警一次；
#   ③ lego 不够新：Debian 上启用官方 backports 装 lego；非 Debian、续期 timer 不装包；
#   ④ 域名：certbot webroot；升级时接管已有的 Let's Encrypt 证书，不再申请；
#   ⑤ PANDORA_ACME=0：不联系 CA，只用自签；设置记进 acme.env，renew 沿用；
#   ⑥ 渲染 → nginx -t → reload，失败把站点文件与 nginx.conf 一起换回；渲染器拒绝时不动 nginx；
#   ⑦ renew：证书变了才 reload；证书过期、不覆盖面板地址都报错并告警；运维自己的证书不替换。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'edge-tls: %s\n' "$*" >&2; exit 1; }
refute() { if grep "$@"; then fail "unexpected match: $*"; fi; }
EDGE="$DEPLOY/edge-tls.sh"

# --- 桩：每次调用记一行到 calls.log ---------------------------------------------
mkdir -p "$T/bin"
stub() {
  local name="$1" body="${2:-}"
  printf '#!/usr/bin/env bash\nprintf "%%s %%s\\n" "%s" "$*" >>"%s/calls.log"\nT="%s"\n%s\n' "$name" "$T" "$T" "$body" >"$T/bin/$name"
  chmod +x "$T/bin/$name"
}
# 用 openssl 现签一张证书：<cert> <key> <CN> <SAN> <天数>，或 <cert> <key> <CN> <SAN> window <起> <止>
#（YYYYMMDDHHMMSSZ；req 的 -not_before 要 OpenSSL 3.4，CI 上是 3.0，所以走 openssl ca -selfsign）
cp "$DEPLOY/fixtures/mkcert-test.sh" "$T/bin/mkcert"
chmod +x "$T/bin/mkcert"
stub systemctl 'case "$1" in cat) [ -f "$T/no-timer" ] && exit 1; exit 0 ;; esac; exit 0'
stub ufw 'if [ "${1:-}" = status ]; then echo "Status: active"; fi; exit 0'
stub nginx '[ "${1:-}" = -t ] && [ -f "$T/nginx.fail" ] && { echo "nginx: [emerg] stub failure" >&2; exit 1; }; exit 0'
stub curl 'exit 0'
# lego 桩：版本取 lego.version（没有就当没装）；run 现签一张约 6 天的 IP 证书；renew 只在 lego.renew 存在时换新
stub lego '
[ -f "$T/lego.version" ] || exit 127
[ "${1:-}" = --version ] && { echo "lego version $(cat "$T/lego.version") linux/amd64"; exit 0; }
path="" dom="" sub=""
while [ $# -gt 0 ]; do
  case "$1" in --path) path="$2"; shift ;; --domains) dom="$2"; shift ;; run|renew) sub="$1" ;; esac
  shift
done
[ -f "$T/lego.fail" ] && { echo "acme: error: 400 :: urn:ietf:params:acme:error:connection" >&2; exit 1; }
if [ "$sub" = run ] || { [ "$sub" = renew ] && [ -f "$T/lego.renew" ]; }; then
  mkcert "$path/certificates/$dom.crt" "$path/certificates/$dom.key" "$dom" "IP:$dom" 6
fi'
# certbot 桩：按 -d 在 live 目录下现签一张 90 天的域名证书
stub certbot '
[ -f "$T/certbot.fail" ] && exit 1
d=""; while [ $# -gt 0 ]; do [ "$1" = -d ] && d="$2"; shift; done
[ -n "$d" ] && mkcert "$PANDORA_LE_LIVE_DIR/$d/fullchain.pem" "$PANDORA_LE_LIVE_DIR/$d/privkey.pem" "$d" "DNS:$d" 90
exit 0'
# apt-get 桩：install lego 就把 lego 「升」到 4.35.2
stub apt-get 'case "$*" in *install*lego*) [ -f "$T/apt.fail" ] && exit 100; echo 4.35.2 >"$T/lego.version" ;; esac; exit 0'
export PATH="$T/bin:$PATH"
calls() { cat "$T/calls.log" 2>/dev/null || true; }
# 现在往后（负数往前）N 天的 UTC 时间，openssl ca 的日期格式；GNU 与 BSD date 都行
utc_days() {
  local n="$1"; [[ "$n" == -* ]] || n="+$n"
  date -u -d "$n days" +%Y%m%d%H%M%SZ 2>/dev/null || date -u -v"${n}d" +%Y%m%d%H%M%SZ
}
reset_calls() { : >"$T/calls.log"; }

export PANDORA_TLS_DIR="$T/tls" PANDORA_TLS_STATE_DIR="$T/state" PANDORA_LE_LIVE_DIR="$T/le"
export PANDORA_ACME_WEBROOT="$T/webroot" PANDORA_NGINX_DIR="$T/nginx" PANDORA_REALIP_FILE="$T/realip.conf"
export PANDORA_BACKUP_DIR="$T/backups" PANDORA_NGINX_VERSION=1.26.3
export PANDORA_OS_RELEASE="$T/os-release" PANDORA_APT_SOURCES_DIR="$T/apt"
unset PANDORA_ACME PANDORA_CERTBOT PANDORA_ACME_EMAIL PANDORA_CERTBOT_EMAIL PANDORA_ACME_SERVER

ip=203.0.113.10
domain=panel.example.test
admin_path=ops_0123456789abcdef0123456789abcdef
printf 'AEGIS_ADMIN_PATH=%s\nAEGIS_PUBLIC_BASE_URL=https://%s\nAEGIS_ALERT_TG_TOKEN=tok\nAEGIS_ALERT_TG_CHAT=42\n' "$admin_path" "$ip" >"$T/ip.env"
printf 'AEGIS_ADMIN_PATH=%s\nAEGIS_PUBLIC_BASE_URL=https://%s/\n' "$admin_path" "$domain" >"$T/domain.env"

fresh() {
  rm -rf "$T/tls" "$T/state" "$T/le" "$T/webroot" "$T/nginx" "$T/backups" "$T/apt" \
    "$T/lego.version" "$T/lego.fail" "$T/lego.renew" "$T/certbot.fail" "$T/nginx.fail" "$T/apt.fail" "$T/no-timer"
  mkdir -p "$T/nginx/conf.d" "$T/nginx/sites-available" "$T/nginx/sites-enabled" "$T/apt/sources.list.d"
  printf 'ID=debian\nVERSION_CODENAME=trixie\n' >"$T/os-release"
  printf 'Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie trixie-updates\nComponents: main\n' \
    >"$T/apt/sources.list.d/debian.sources"
  reset_calls
}
edge() { local rc=0; bash "$EDGE" "$@" >"$T/out" 2>&1 || rc=$?; return "$rc"; }
rc_of() { local rc=0; edge "$@" || rc=$?; echo "$rc"; }
live_target() { readlink "$T/tls/live"; }
covers_ip() { openssl x509 -in "$T/tls/live/fullchain.pem" -noout -checkip "$1" | grep -q 'does match'; }
status_of() { awk -F= -v k="$1" '$1 == k { sub(/^[^=]*=/, ""); print }' "$T/state/status"; }

# --- ① 只有公网 IP：lego 申请 IP 证书 ----------------------------------------------
fresh
echo 4.35.2 >"$T/lego.version"
rc="$(rc_of setup "$T/ip.env")"
[ "$rc" = 0 ] || fail "IP setup returned $rc: $(cat "$T/out")"
[ "$(live_target)" = "$T/tls/lego" ] || fail "live points at $(live_target)"
covers_ip "$ip" || fail 'live certificate does not cover the IP'
calls | grep -q "^lego --path $T/tls/acme --accept-tos --key-type ec256 --disable-cn --http --http.webroot $T/webroot --domains $ip run --profile shortlived$" \
  || fail "lego run arguments: $(calls | grep '^lego')"
calls | grep -qx 'ufw allow 80/tcp' && calls | grep -qx 'ufw allow 443/tcp' || fail 'ufw not opened'
calls | grep -qx 'systemctl enable --now aegis-tls-renew.timer' || fail 'renew timer not enabled'
calls | grep -qx 'nginx -t' && calls | grep -qx 'systemctl reload nginx' || fail 'nginx not tested and reloaded'
refute -q '^certbot' "$T/calls.log"
refute -q '^apt-get' "$T/calls.log"
conf="$T/nginx/conf.d/aegis.conf"
grep -Fq "server_name $ip;" "$conf" || fail 'aegis.conf not rendered for the IP'
grep -Fq 'ssl_certificate /etc/aegispanel/tls/live/fullchain.pem;' "$conf" || fail 'certificate path is not the stable live link'
grep -q 'Let.s Encrypt' "$T/out" && grep -q '订户协议' "$T/out" || fail 'issuance does not state the subscriber agreement'
[ "$(status_of RESULT)" = ok ] && [ "$(status_of MODE)" = lego ] || fail "status: $(cat "$T/state/status")"
grep -qx 'ACME_ENABLED=1' "$T/tls/acme.env" || fail 'acme.env not written'
# 再跑一次 setup（升级重跑）：在用的 IP 证书沿用，不再联系 CA
reset_calls
[ "$(rc_of setup "$T/ip.env")" = 0 ] || fail 'second setup failed'
refute -q '^lego --path' "$T/calls.log"

# --- ② 申请失败：自签兜底；renew 重试成功即换上 --------------------------------------
fresh
echo 4.35.2 >"$T/lego.version"; touch "$T/lego.fail"
rc="$(rc_of setup "$T/ip.env")"
[ "$rc" = 3 ] || fail "failed issuance should exit 3, got $rc: $(cat "$T/out")"
[ "$(live_target)" = "$T/tls/selfsigned" ] || fail "fallback live points at $(live_target)"
covers_ip "$ip" || fail 'self-signed certificate does not cover the IP'
[ "$(stat -c %a "$T/tls/selfsigned/privkey.pem" 2>/dev/null || stat -f %Lp "$T/tls/selfsigned/privkey.pem")" = 600 ] || fail 'self-signed key is not 0600'
grep -Fq "$DEPLOY/edge-tls.sh issue" "$T/out" || fail "fallback does not give the remediation command: $(cat "$T/out")"
grep -q '80/tcp' "$T/out" && grep -q '自动重试' "$T/out" || fail 'fallback does not explain the cause and the retry'
grep -Fq "server_name $ip;" "$T/nginx/conf.d/aegis.conf" || fail 'nginx not rendered on the self-signed fallback'
[ "$(status_of RESULT)" = warn ] || fail "fallback status: $(cat "$T/state/status")"
# renew 仍失败：退出 1，结论写进 status；告警只在结论变化时推
reset_calls
[ "$(rc_of renew "$T/ip.env")" = 1 ] || fail 'renew on a failing fallback should exit 1'
grep -q '仍在用自签证书' "$T/state/status" || fail "renew status: $(cat "$T/state/status")"
[ "$(calls | grep -c '^curl')" = 1 ] || fail "status change did not alert exactly once: $(calls | grep '^curl')"
reset_calls
edge renew "$T/ip.env" || true
refute -q '^curl' "$T/calls.log"
# 80 修好了：renew 申请成功，无缝换上并 reload，推一条恢复
rm -f "$T/lego.fail"; reset_calls
[ "$(rc_of renew "$T/ip.env")" = 0 ] || fail "renew after the fix failed: $(cat "$T/out")"
[ "$(live_target)" = "$T/tls/lego" ] || fail 'renew did not switch to the Let'\''s Encrypt certificate'
calls | grep -qx 'systemctl reload nginx' || fail 'switch was not followed by a reload'
calls | grep -q '^curl' || fail 'recovery was not reported'
[ "$(status_of RESULT)" = ok ] || fail "status after recovery: $(cat "$T/state/status")"
refute -q '^apt-get' "$T/calls.log"

# --- ③ lego 不够新：Debian 上从官方 backports 装；别处与续期 timer 不装 ---------------
fresh
echo 4.9.1 >"$T/lego.version"
[ "$(rc_of setup "$T/ip.env")" = 0 ] || fail "backports install path failed: $(cat "$T/out")"
src="$T/apt/sources.list.d/pandora-trixie-backports.sources"
[ -f "$src" ] && grep -qx 'Suites: trixie-backports' "$src" && grep -qx 'URIs: http://deb.debian.org/debian' "$src" \
  && grep -qx 'Signed-By: /usr/share/keyrings/debian-archive-keyring.gpg' "$src" || fail "backports source: $(cat "$src" 2>/dev/null)"
calls | grep -q '^apt-get install -y -qq -t trixie-backports lego$' || fail "apt-get: $(calls | grep apt-get)"
# 已经启用了 backports：不再写源
fresh
echo 4.9.1 >"$T/lego.version"
printf 'deb http://deb.debian.org/debian trixie-backports main\n' >"$T/apt/sources.list"
[ "$(rc_of setup "$T/ip.env")" = 0 ] || fail 'setup with backports already enabled failed'
[ ! -e "$T/apt/sources.list.d/pandora-trixie-backports.sources" ] || fail 'backports source written twice'
# 不是 Debian：不碰 apt，自签兜底并说明
fresh
printf 'ID=ubuntu\nVERSION_CODENAME=noble\n' >"$T/os-release"
[ "$(rc_of setup "$T/ip.env")" = 3 ] || fail 'non-Debian without lego should fall back'
refute -q '^apt-get' "$T/calls.log"
grep -q '4.22.0' "$T/out" || fail 'non-Debian fallback does not name the lego version it needs'
# 续期 timer 不装包
reset_calls
edge renew "$T/ip.env" || true
refute -q '^apt-get' "$T/calls.log"

# --- ④ 域名：certbot webroot；升级接管已有证书 -------------------------------------------
fresh
[ "$(rc_of setup "$T/domain.env")" = 0 ] || fail "domain setup failed: $(cat "$T/out")"
calls | grep -q "^certbot certonly --webroot -w $T/webroot -d $domain --non-interactive --agree-tos --register-unsafely-without-email$" \
  || fail "certbot arguments: $(calls | grep '^certbot')"
[ "$(live_target)" = "$T/le/$domain" ] || fail "domain live points at $(live_target)"
refute -q '^lego --path' "$T/calls.log"
fresh
PANDORA_CERTBOT_EMAIL=ops@example.test PANDORA_ACME_SERVER=https://acme-staging-v02.api.letsencrypt.org/directory \
  edge setup "$T/domain.env" || fail "domain setup with email failed: $(cat "$T/out")"
calls | grep -q -- '--email ops@example.test' && calls | grep -q -- '--server https://acme-staging-v02.api.letsencrypt.org/directory' \
  || fail "email / server not passed: $(calls | grep '^certbot')"
grep -qx 'ACME_EMAIL=ops@example.test' "$T/tls/acme.env" || fail 'email not remembered for renewals'
# 升级：以前申请过的 /etc/letsencrypt/live/<域名> 直接接管，不再申请
fresh
mkcert "$T/le/$domain/fullchain.pem" "$T/le/$domain/privkey.pem" "$domain" "DNS:$domain" 90
[ "$(rc_of setup "$T/domain.env")" = 0 ] || fail 'adopting an existing certificate failed'
refute -q '^certbot' "$T/calls.log"
[ "$(live_target)" = "$T/le/$domain" ] || fail 'existing Let'\''s Encrypt certificate not adopted'
grep -q '接管已有的' "$T/out" || fail 'adoption not reported'
# certbot 失败：自签兜底，证书覆盖域名
fresh
touch "$T/certbot.fail"
[ "$(rc_of setup "$T/domain.env")" = 3 ] || fail 'certbot failure should fall back'
openssl x509 -in "$T/tls/live/fullchain.pem" -noout -checkhost "$domain" | grep -q 'does match' || fail 'self-signed does not cover the domain'

# --- ⑤ PANDORA_ACME=0：只用自签，不联系 CA ------------------------------------------------
fresh
echo 4.35.2 >"$T/lego.version"
rc=0; PANDORA_ACME=0 bash "$EDGE" setup "$T/ip.env" >"$T/out" 2>&1 || rc=$?
[ "$rc" = 3 ] || fail "PANDORA_ACME=0 should exit 3, got $rc"
refute -q -e '^lego --path' -e '^certbot' "$T/calls.log"
grep -qx 'ACME_ENABLED=0' "$T/tls/acme.env" || fail 'opt-out not remembered'
reset_calls
[ "$(rc_of renew "$T/ip.env")" = 0 ] || fail 'renew with ACME disabled should succeed quietly'
refute -q -e '^lego --path' -e '^certbot' "$T/calls.log"
[ "$(status_of RESULT)" = warn ] || fail 'opt-out status should be warn'
# 旧名 PANDORA_CERTBOT=0 同义
fresh
rc=0; PANDORA_CERTBOT=0 bash "$EDGE" setup "$T/domain.env" >"$T/out" 2>&1 || rc=$?
[ "$rc" = 3 ] && ! calls | grep -q '^certbot' || fail 'PANDORA_CERTBOT=0 still contacted the CA'

# --- ⑥ 渲染 → nginx -t → reload；失败换回 ---------------------------------------------------
fresh
echo 4.35.2 >"$T/lego.version"
stock_main='user www-data;
worker_processes auto;

events {
	worker_connections 768;
}

http {
	include /etc/nginx/conf.d/*.conf;
}'
printf '%s\n' "$stock_main" >"$T/nginx/nginx.conf"
# 发行版默认站点：只停用原样的链接
printf 'server {\n    listen 80 default_server;\n}\n' >"$T/nginx/sites-available/default"
ln -s "$T/nginx/sites-available/default" "$T/nginx/sites-enabled/default"
bash "$EDGE" ensure "$T/ip.env" >/dev/null
edge apply "$T/ip.env" || fail "apply failed: $(cat "$T/out")"
[ ! -e "$T/nginx/sites-enabled/default" ] && [ -f "$T/nginx/sites-available/default" ] || fail 'stock default site handling'
grep -Eq '^[[:space:]]*worker_connections 8192;' "$T/nginx/nginx.conf" || fail 'nginx.conf not tuned'
printf 'server { listen 80 default_server; }\n' >"$T/nginx/sites-available/mine"
ln -s "$T/nginx/sites-available/mine" "$T/nginx/sites-enabled/default"
echo '# previous good config' >"$T/nginx/conf.d/aegis.conf"
printf '%s\n' "$stock_main" >"$T/nginx/nginx.conf"
touch "$T/nginx.fail"; reset_calls
[ "$(rc_of apply "$T/ip.env")" = 1 ] || fail 'nginx -t failure not reported'
[ -L "$T/nginx/sites-enabled/default" ] || fail 'a non-stock link named default was removed'
[ "$(cat "$T/nginx/conf.d/aegis.conf")" = '# previous good config' ] || fail 'previous config not restored'
[ "$(cat "$T/nginx/nginx.conf")" = "$stock_main" ] || fail 'nginx.conf not restored'
refute -q 'reload' "$T/calls.log"
ls "$T/backups"/nginx-aegis.conf.* "$T/backups"/nginx-main.conf.* >/dev/null 2>&1 || fail 'configs not backed up'
rm -f "$T/nginx/conf.d/aegis.conf"
[ "$(rc_of apply "$T/ip.env")" = 1 ] && [ ! -e "$T/nginx/conf.d/aegis.conf" ] || fail 'failed first render left a new aegis.conf'
# setup 在 nginx -t 失败时退出 1，不申请证书
reset_calls
[ "$(rc_of setup "$T/ip.env")" = 1 ] || fail 'setup ignored the nginx -t failure'
refute -q '^lego --path' "$T/calls.log"
rm -f "$T/nginx.fail"
# 渲染器拒绝（缺后台前缀）：不动 nginx
echo '# keep me' >"$T/nginx/conf.d/aegis.conf"
printf 'AEGIS_PUBLIC_BASE_URL=https://%s\n' "$ip" >"$T/bad.env"
reset_calls
[ "$(rc_of apply "$T/bad.env")" = 1 ] || fail 'render refusal not reported'
[ "$(cat "$T/nginx/conf.d/aegis.conf")" = '# keep me' ] && ! calls | grep -q '^nginx' || fail 'nginx touched after render refusal'
# 不合规的对外地址：一步都不做
printf 'AEGIS_ADMIN_PATH=%s\nAEGIS_PUBLIC_BASE_URL=https://10.0.0.5\n' "$admin_path" >"$T/private.env"
[ "$(rc_of setup "$T/private.env")" = 1 ] || fail 'private IP accepted'
grep -q '公网' "$T/out" || fail "private IP refusal: $(cat "$T/out")"

# --- ⑦ renew：变了才 reload；过期、主机不符报错；运维的证书不替换 -----------------------
fresh
echo 4.35.2 >"$T/lego.version"
edge setup "$T/ip.env" || fail 'setup for renew tests failed'
reset_calls
[ "$(rc_of renew "$T/ip.env")" = 0 ] || fail "steady renew failed: $(cat "$T/out")"
calls | grep -q "^lego .* --domains $ip renew --dynamic --profile shortlived --no-random-sleep$" || fail "lego renew arguments: $(calls | grep '^lego')"
refute -q 'reload' "$T/calls.log"
refute -q '^curl' "$T/calls.log"
touch "$T/lego.renew"; reset_calls
[ "$(rc_of renew "$T/ip.env")" = 0 ] || fail 'renew with a new certificate failed'
[ "$(calls | grep -c 'systemctl reload nginx')" = 1 ] || fail 'renewed certificate not reloaded exactly once'
rm -f "$T/lego.renew"; reset_calls
edge renew "$T/ip.env" || true
refute -q 'reload' "$T/calls.log"
# 续期失败本身：退出 1，告警
touch "$T/lego.fail"; reset_calls
[ "$(rc_of renew "$T/ip.env")" = 1 ] || fail 'failed lego renew should exit 1'
grep -q 'lego 续期失败' "$T/state/status" && calls | grep -q '^curl' || fail 'failed renew not recorded / alerted'
rm -f "$T/lego.fail"
# 快到期（6 天证书只剩约 1 天，低于寿命的 1/3）而续期没成功：报错并告警，每次都推
mkcert "$T/tls/acme/certificates/$ip.crt" "$T/tls/acme/certificates/$ip.key" "$ip" "IP:$ip" window "$(utc_days -5)" "$(utc_days 1)"
reset_calls
[ "$(rc_of renew "$T/ip.env")" = 1 ] || fail "expiring certificate not reported: $(cat "$T/out")"
grep -q '小时到期' "$T/state/status" && calls | grep -q '^curl' || fail "expiring: $(cat "$T/state/status")"
reset_calls
edge renew "$T/ip.env" || true
calls | grep -q '^curl' || fail 'an expiring certificate must alert on every run'
# 已过期：同样
mkcert "$T/tls/acme/certificates/$ip.crt" "$T/tls/acme/certificates/$ip.key" "$ip" "IP:$ip" window "$(utc_days -7)" "$(utc_days -1)"
reset_calls
[ "$(rc_of renew "$T/ip.env")" = 1 ] || fail 'expired certificate not reported'
grep -q '已过期' "$T/state/status" && calls | grep -q '^curl' || fail "expired: $(cat "$T/state/status")"
# 对外地址改了、证书不覆盖：报错，不自己联系 CA
mkcert "$T/tls/acme/certificates/$ip.crt" "$T/tls/acme/certificates/$ip.key" "$ip" "IP:$ip" 6
printf 'AEGIS_ADMIN_PATH=%s\nAEGIS_PUBLIC_BASE_URL=https://198.51.100.7\n' "$admin_path" >"$T/moved.env"
reset_calls
[ "$(rc_of renew "$T/moved.env")" = 1 ] || fail 'host mismatch not reported'
grep -q '不覆盖面板地址' "$T/state/status" || fail "mismatch: $(cat "$T/state/status")"
# 运维自己指定的证书：issue / setup 都不替换
fresh
mkcert "$T/own/fullchain.pem" "$T/own/privkey.pem" "$ip" "IP:$ip" 30
mkdir -p "$T/tls"; ln -s "$T/own" "$T/tls/live"
echo 4.35.2 >"$T/lego.version"
[ "$(rc_of setup "$T/ip.env")" = 0 ] || fail "custom certificate setup: $(cat "$T/out")"
[ "$(live_target)" = "$T/own" ] || fail 'custom certificate was replaced'
refute -q '^lego --path' "$T/calls.log"
# 没配过边缘：renew 什么都不做
fresh
[ "$(rc_of renew "$T/ip.env")" = 0 ] || fail 'renew without an edge should be a no-op'
# status 能读
edge setup "$T/domain.env" || true
edge status "$T/domain.env" || fail 'status failed'
grep -q "$domain" "$T/out" && grep -q '到期时间' "$T/out" || fail "status output: $(cat "$T/out")"

# --- 静态：模板、续期单元与本脚本的路径一致 ------------------------------------------------
grep -Fq 'root /var/www/aegis-acme;' "$DEPLOY/nginx-aegis.conf" && grep -Fq 'ACME_WEBROOT="${PANDORA_ACME_WEBROOT:-/var/www/aegis-acme}"' "$EDGE" \
  || fail 'ACME webroot differs between the template and edge-tls.sh'
grep -Fq 'ssl_certificate /etc/aegispanel/tls/live/fullchain.pem;' "$DEPLOY/nginx-aegis.conf" && grep -Fq 'TLS_DIR="${PANDORA_TLS_DIR:-/etc/aegispanel/tls}"' "$EDGE" \
  || fail 'live certificate path differs between the template and edge-tls.sh'
grep -Fq 'RENEW_TIMER=aegis-tls-renew.timer' "$EDGE" || fail 'edge-tls.sh enables a different timer'

printf 'edge-tls mock: PASS\n'

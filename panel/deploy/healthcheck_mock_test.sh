#!/usr/bin/env bash
# healthcheck.sh 证书一节（check_tls）的桩测试：不需要 root、nginx 或网络。
# 以 HEALTHCHECK_LIB=1 source 它只取函数，把「nginx 在回环 443 上下发的证书」换成临时签的证书，验证：
#   主机取对外地址（域名与公网 IPv4 都查）；阈值按寿命缩放（剩余 < 寿命 1/3，最多 14 天）：
#   6 天的 IP 证书刚签、过半都不报，只剩 1 天才报；90 天证书剩 20 天不报、剩 10 天报；过期报；
#   续期 timer 的结论 RESULT=error 报、超过 36 小时没跑报；没走 HTTPS 边缘不查。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
T="$(mktemp -d)"
trap 'rm -rf -- "$T"' EXIT
fail() { printf 'healthcheck: %s\n' "$*" >&2; exit 1; }
MK="$DEPLOY/fixtures/mkcert-test.sh"
utc_days() {
  local n="$1"; [[ "$n" == -* ]] || n="+$n"
  date -u -d "$n days" +%Y%m%d%H%M%SZ 2>/dev/null || date -u -v"${n}d" +%Y%m%d%H%M%SZ
}

export EDGE_CONF="$T/aegis.conf" TLS_STATUS_FILE="$T/status"
HEALTHCHECK_LIB=1 . "$DEPLOY/healthcheck.sh"
set -euo pipefail
declare -F check_tls served_cert >/dev/null || fail 'library mode did not define check_tls'
# 换掉探测：按 SERVED 指向的文件下发证书，并记下用的主机
served_cert() { printf '%s\n' "$1" >"$T/probed"; cat "$SERVED" 2>/dev/null || true; }

ip=203.0.113.10
domain=panel.example.test
touch "$EDGE_CONF"
run() {
  PROBLEMS=(); check_tls; : >"$T/problems"
  [ "${#PROBLEMS[@]}" -eq 0 ] || printf '%s\n' "${PROBLEMS[@]}" >"$T/problems"
}
none() { run; [ ! -s "$T/problems" ] || fail "$1: unexpected problems: $(cat "$T/problems")"; }
some() { run; grep -q "$2" "$T/problems" || fail "$1: want '$2', got: $(cat "$T/problems")"; }
cert() { bash "$MK" "$T/c.pem" "$T/k.pem" "$1" "$2" window "$(utc_days "$3")" "$(utc_days "$4")"; SERVED="$T/c.pem"; }

# IP 部署：主机取自对外地址（以前从 server_name 用域名正则取，IP 时根本不查）
AEGIS_PUBLIC_BASE_URL="https://$ip/"
cert "$ip" "IP:$ip" 0 6;   none 'fresh 6-day IP certificate'
[ "$(cat "$T/probed")" = "$ip" ] || fail "probed host: $(cat "$T/probed")"
cert "$ip" "IP:$ip" -3 3;  none 'IP certificate at half-life (renewal window, not an alarm)'
cert "$ip" "IP:$ip" -5 1;  some 'IP certificate with 1 day left' '小时到期'
cert "$ip" "IP:$ip" -7 -1; some 'expired IP certificate' '已过期'
# 域名：90 天证书按 14 天线
AEGIS_PUBLIC_BASE_URL="https://$domain"
cert "$domain" "DNS:$domain" -70 20; none '90-day certificate with 20 days left'
cert "$domain" "DNS:$domain" -80 10; some '90-day certificate with 10 days left' '小时到期'
[ "$(cat "$T/probed")" = "$domain" ] || fail 'domain host not probed'
# 取不到证书
SERVED="$T/missing.pem"; some 'nothing served on 443' '取不到'

# 续期 timer 的结论
cert "$domain" "DNS:$domain" -10 80
now_iso="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf 'RESULT=ok\nMESSAGE=证书正常\nCHECKED_AT=%s\n' "$now_iso" >"$TLS_STATUS_FILE"; none 'renewal ok'
printf 'RESULT=warn\nMESSAGE=按设置只用自签证书（PANDORA_ACME=0）\nCHECKED_AT=%s\n' "$now_iso" >"$TLS_STATUS_FILE"; none 'opt-out warn is not an alarm'
printf 'RESULT=error\nMESSAGE=lego 续期失败\nCHECKED_AT=%s\n' "$now_iso" >"$TLS_STATUS_FILE"; some 'renewal error' 'lego 续期失败'
old_iso="$(date -u -d '-2 days' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v-2d +%Y-%m-%dT%H:%M:%SZ)"
printf 'RESULT=ok\nMESSAGE=证书正常\nCHECKED_AT=%s\n' "$old_iso" >"$TLS_STATUS_FILE"; some 'stale renewal timer' '36 小时没跑'
rm -f "$TLS_STATUS_FILE"

# 没走 HTTPS 边缘：不查
rm -f "$EDGE_CONF"; SERVED="$T/missing.pem"; none 'no edge config'
touch "$EDGE_CONF"; AEGIS_PUBLIC_BASE_URL=http://127.0.0.1:9000; none 'non-https base URL'

# 主流程调用它，旧的 server_name 域名正则已经删掉
grep -qx 'check_tls' "$DEPLOY/healthcheck.sh" || fail 'main flow does not call check_tls'
if grep -q 'server_name \\K' "$DEPLOY/healthcheck.sh"; then fail 'old server_name regex still present'; fi

printf 'healthcheck mock: PASS\n'

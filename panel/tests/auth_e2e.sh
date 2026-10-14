#!/usr/bin/env bash
# 鉴权端到端（docs/architecture.md 第 8、17 节）：
#   1. 注册、登录：响应体只有 access，refresh 在 HttpOnly + SameSite=Strict 的 cookie 里；
#   2. refresh 轮换；随便拼的值只回 401 不吊销；上一枚超出 30 秒宽限再用 = 重放，整条会话吊销；
#   3. 跨域令牌：门户令牌进不了后台，后台令牌进不了门户；
#   4. IAM-006：口令错、邮箱不存在、非管理员账号登后台，对外完全一样；
#   5. 重认证：口令错 401，口令对发新令牌；
#   6. 停用即吊销：停用后令牌与 refresh 立即失效，口令对也登不进（与口令错一样）；
#   7. 改密：门户保留当前会话、吊销其余；旧口令失效；
#   8. 同一账号 15 分钟失败 10 次暂停，第 11 次口令对也 429，门户与后台共用；
#   9. 登录失败审计记 anonymous，被撞账号只作资源。
#
# 会建用户、留审计，只许在一次性库上跑：AUTH_E2E_DISPOSABLE=YES_DELETE_FIXTURES，库名带独立的 test / e2e 段。
# 要求非生产模式（注册第 1 步回带 dev_code）。来源 IP 用 X-Real-IP 扮演（引擎只信它）。
set -uo pipefail

PUB=${PUB:-http://127.0.0.1:9000}
ADM=${ADM:-http://127.0.0.1:9001}
PSQL=${PSQL:-/opt/pandora/deploy/psql.sh}
ADMIN_EMAIL=${ADMIN_EMAIL:-}
ADMIN_PASS=${ADMIN_PASS:-}

pass=0; fail=0
ok()  { echo "  [ OK ] $1"; pass=$((pass+1)); }
bad() { echo "  [FAIL] $1"; echo "         $2"; fail=$((fail+1)); }
sec() { echo; echo "=== $1 ==="; }
jqr() { python3 -c "import sys,json;d=json.load(sys.stdin);print(d$1)" 2>/dev/null; }
code(){ curl -s -o /dev/null -w '%{http_code}' "$@"; }
json(){ curl -s -H 'Content-Type: application/json' "$@"; }
# 错误信封里对外可见的部分（码与文案），去掉每次不同的 request_id
errsig(){ python3 -c "import sys,json;e=json.load(sys.stdin)['error'];print(e['code']+'|'+e['message'])" 2>/dev/null; }

[ "${AUTH_E2E_DISPOSABLE:-}" = YES_DELETE_FIXTURES ] \
  || { echo '[FATAL] AUTH_E2E_DISPOSABLE=YES_DELETE_FIXTURES is required' >&2; exit 1; }
[ -n "$ADMIN_EMAIL" ] && [ -n "$ADMIN_PASS" ] \
  || { echo '[FATAL] ADMIN_EMAIL / ADMIN_PASS must be explicit' >&2; exit 1; }
DB_NAME=$("$PSQL" -X -qAtc 'SELECT current_database()' | tr -d '[:space:]')
[[ "$DB_NAME" =~ (^|[-_])(test|e2e)([-_]|$) ]] \
  || { echo '[FATAL] database name must contain a standalone test/e2e segment' >&2; exit 1; }
# 注册 IP 限额是后台设置（缺省每 IP 每分钟 10 次），连着注册会撞 429：一次性库上先关掉（见 lib/signup.sh）
. "$(dirname "$0")/lib/signup.sh"
relax_signup_ip_limit

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
RUN="$(date +%s)_$RANDOM"

# register <email> <password>：走完两步注册，打印 user_id
register(){
  local r rt cd
  r=$(json -X POST "$PUB/v1/auth/register/start" -d "{\"email\":\"$1\"}")
  rt=$(echo "$r" | jqr "['registration_token']"); cd=$(echo "$r" | jqr "['dev_code']")
  json -X POST "$PUB/v1/auth/register/complete" \
    -d "{\"registration_token\":\"$rt\",\"code\":\"$cd\",\"password\":\"$2\"}" | jqr "['user_id']"
}
# login <base> <email> <password> <cookie jar>：打印 access_token
login(){
  json -X POST "$1/v1/auth/login" -c "$4" -d "{\"email\":\"$2\",\"password\":\"$3\"}" | jqr "['access_token']"
}
# rt_of <cookie jar> <name>：取 jar 里的 refresh 值
rt_of(){ awk -v n="$2" '$6==n{print $7}' "$1" | tail -1; }

#-------------------------------------------------------------------------------
sec "1. 注册与登录"

UE="authe2e_${RUN}@example.com"; UP="Auth!E2e2026x"
UID_=$(register "$UE" "$UP")
[ -n "$UID_" ] && ok "门户用户已注册" || { bad "注册失败" ""; exit 1; }
H="$TMP/login.h"
BODY=$(curl -s -D "$H" -c "$TMP/u1" -H 'Content-Type: application/json' -X POST "$PUB/v1/auth/login" \
  -d "{\"email\":\"$UE\",\"password\":\"$UP\"}")
UTOK=$(echo "$BODY" | jqr "['access_token']")
[ -n "$UTOK" ] && ok "门户登录拿到 access" || { bad "门户登录失败" "$BODY"; exit 1; }
echo "$BODY" | grep -q refresh && bad "响应体里出现了 refresh" "$BODY" || ok "响应体不含 refresh"
SC=$(grep -i '^set-cookie: pandora_rt=' "$H")
echo "$SC" | grep -qi httponly && echo "$SC" | grep -qi 'samesite=strict' && echo "$SC" | grep -qi 'path=/v1/auth' \
  && ok "refresh cookie：HttpOnly、SameSite=Strict、Path=/v1/auth" || bad "refresh cookie 属性不对" "$SC"
C=$(code "$PUB/v1/me" -H "Authorization: Bearer $UTOK")
[ "$C" = 200 ] && ok "GET /v1/me 200" || bad "access 不可用" "HTTP $C"

#-------------------------------------------------------------------------------
sec "2. refresh 轮换、随便拼的值、重放"

RT1=$(rt_of "$TMP/u1" pandora_rt)
[ -n "$RT1" ] || { bad "登录后 cookie jar 里没有 refresh" ""; exit 1; }
R=$(curl -s -b "$TMP/u1" -c "$TMP/u1" -X POST "$PUB/v1/auth/refresh")
NEW=$(echo "$R" | jqr "['access_token']")
RT2=$(rt_of "$TMP/u1" pandora_rt)
[ -n "$NEW" ] && [ -n "$RT2" ] && [ "$RT1" != "$RT2" ] && ok "refresh 发新 access 并轮换 cookie" || bad "refresh 没有轮换" "$R"
SID=${RT1%%.*}
FAKE="$SID.$(head -c 32 /dev/urandom | base64 | tr '+/' '-_' | tr -d '=')"
C=$(code -X POST "$PUB/v1/auth/refresh" -b "pandora_rt=$FAKE")
[ "$C" = 401 ] && ok "随便拼的 refresh 回 401" || bad "伪造的 refresh 没被拒" "HTTP $C"
C=$(code "$PUB/v1/me" -H "Authorization: Bearer $NEW")
[ "$C" = 200 ] && ok "伪造的 refresh 没有吊销会话（sid 不保密，不能拿来踢人）" || bad "伪造值把会话吊销了" "HTTP $C"
C=$(code -X POST "$PUB/v1/auth/refresh" -b "pandora_rt=$RT1")
[ "$C" = 200 ] && ok "30 秒宽限内用上一枚：只发 access" || bad "宽限内的并发刷新被拒" "HTTP $C"
echo "  等 31 秒越过宽限…"
sleep 31
C=$(code -X POST "$PUB/v1/auth/refresh" -b "pandora_rt=$RT1")
[ "$C" = 401 ] && ok "宽限外再用上一枚：401" || bad "重放没被拒" "HTTP $C"
C=$(code -X POST "$PUB/v1/auth/refresh" -b "pandora_rt=$RT2")
[ "$C" = 401 ] && ok "重放后整条会话吊销：当前 refresh 也失效" || bad "重放后会话仍在" "HTTP $C"
C=$(code "$PUB/v1/me" -H "Authorization: Bearer $NEW")
[ "$C" = 401 ] && ok "重放后 access 立即失效" || bad "重放后 access 仍可用" "HTTP $C"
A=$("$PSQL" -X -qAtc "SELECT count(*) FROM audit_events WHERE action = 'session.refresh_reuse'
  AND resource_id = '$UID_' AND actor_kind = 'anonymous'" | tr -d '[:space:]')
[ "${A:-0}" -ge 1 ] && ok "重放写了审计（anonymous，用户作资源）" || bad "重放没有审计" "count=$A"

#-------------------------------------------------------------------------------
sec "3. 跨域令牌"

UTOK=$(login "$PUB" "$UE" "$UP" "$TMP/u2")
ATOK=$(login "$ADM" "$ADMIN_EMAIL" "$ADMIN_PASS" "$TMP/a1")
[ -n "$ATOK" ] && ok "管理员登录后台" || { bad "管理员登录失败" ""; exit 1; }
C=$(code "$ADM/v1/me" -H "Authorization: Bearer $UTOK")
[ "$C" = 401 ] && ok "门户令牌进后台 401" || bad "门户令牌进了后台" "HTTP $C"
C=$(code "$PUB/v1/me" -H "Authorization: Bearer $ATOK")
[ "$C" = 401 ] && ok "后台令牌进门户 401" || bad "后台令牌进了门户" "HTTP $C"
PRT=$(rt_of "$TMP/u2" pandora_rt)
[ -n "$PRT" ] || bad "门户登录后 cookie jar 里没有 refresh" ""
C=$(code -X POST "$ADM/v1/auth/refresh" -b "pandora_admin_rt=$PRT")
[ "$C" = 401 ] && ok "门户 refresh 换不到后台令牌" || bad "门户 refresh 在后台可用" "HTTP $C"

#-------------------------------------------------------------------------------
sec "4. IAM-006：后台登录的三种失败对外一样"

S1=$(json -X POST "$ADM/v1/auth/login" -H 'X-Real-IP: 192.0.2.11' -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"wrong-pass-e2e\"}" | errsig)
S2=$(json -X POST "$ADM/v1/auth/login" -H 'X-Real-IP: 192.0.2.12' -d "{\"email\":\"nobody_${RUN}@example.test\",\"password\":\"wrong-pass-e2e\"}" | errsig)
S3=$(json -X POST "$ADM/v1/auth/login" -H 'X-Real-IP: 192.0.2.13' -d "{\"email\":\"$UE\",\"password\":\"$UP\"}" | errsig)
[[ "$S1" == unauthorized\|* ]] && [ "$S1" = "$S2" ] && [ "$S2" = "$S3" ] && ok "口令错 / 邮箱不存在 / 非管理员：$S1" || bad "回应可区分" "$S1 / $S2 / $S3"

#-------------------------------------------------------------------------------
sec "5. 重认证"

C=$(code -X POST "$ADM/v1/auth/reauth" -H "Authorization: Bearer $ATOK" -H 'Content-Type: application/json' -d '{"password":"wrong-pass-e2e"}')
[ "$C" = 401 ] && ok "口令错 401" || bad "错误口令通过了重认证" "HTTP $C"
R=$(json -X POST "$ADM/v1/auth/reauth" -H "Authorization: Bearer $ATOK" -d "{\"password\":\"$ADMIN_PASS\"}")
RTOK=$(echo "$R" | jqr "['access_token']")
[ -n "$RTOK" ] && ok "口令对：发新令牌" || bad "重认证失败" "$R"
[ -n "$RTOK" ] && ATOK=$RTOK

#-------------------------------------------------------------------------------
sec "6. 停用即吊销"

UTOK=$(login "$PUB" "$UE" "$UP" "$TMP/u3")
[ -n "$(rt_of "$TMP/u3" pandora_rt)" ] || bad "登录后 cookie jar 里没有 refresh" ""
C=$(code -X POST "$ADM/v1/users/$UID_/status" -H "Authorization: Bearer $ATOK" -H 'Content-Type: application/json' \
  -d '{"status":"suspended","reason":"auth e2e"}')
[ "$C" = 200 ] && ok "后台停用用户" || bad "停用失败" "HTTP $C"
C=$(code "$PUB/v1/me" -H "Authorization: Bearer $UTOK")
[ "$C" = 401 ] && ok "停用后 access 立即 401" || bad "停用后 access 仍可用" "HTTP $C"
C=$(code -X POST "$PUB/v1/auth/refresh" -b "$TMP/u3")
[ "$C" = 401 ] && ok "停用后 refresh 401" || bad "停用后 refresh 仍可用" "HTTP $C"
S1=$(json -X POST "$PUB/v1/auth/login" -H 'X-Real-IP: 192.0.2.21' -d "{\"email\":\"$UE\",\"password\":\"$UP\"}" | errsig)
S2=$(json -X POST "$PUB/v1/auth/login" -H 'X-Real-IP: 192.0.2.22' -d "{\"email\":\"$UE\",\"password\":\"wrong-pass-e2e\"}" | errsig)
[[ "$S1" == unauthorized\|* ]] && [ "$S1" = "$S2" ] && ok "停用账号口令对也与口令错一样：$S1" || bad "停用状态可被探知" "$S1 / $S2"
C=$(code -X POST "$ADM/v1/users/$UID_/status" -H "Authorization: Bearer $ATOK" -H 'Content-Type: application/json' \
  -d '{"status":"active","reason":""}')
[ "$C" = 200 ] && ok "恢复用户" || bad "恢复失败" "HTTP $C"

#-------------------------------------------------------------------------------
sec "7. 门户改密"

KEEP=$(login "$PUB" "$UE" "$UP" "$TMP/u4")
OTHER=$(login "$PUB" "$UE" "$UP" "$TMP/u5")
NP="Auth!E2eNew2026"
C=$(code -X POST "$PUB/v1/me/password" -H "Authorization: Bearer $KEEP" -H 'Content-Type: application/json' \
  -d "{\"old_password\":\"$UP\",\"new_password\":\"$NP\"}")
[ "$C" = 200 ] && ok "改密成功" || bad "改密失败" "HTTP $C"
C=$(code "$PUB/v1/me" -H "Authorization: Bearer $KEEP")
[ "$C" = 200 ] && ok "当前会话保留" || bad "当前会话被吊销" "HTTP $C"
C=$(code "$PUB/v1/me" -H "Authorization: Bearer $OTHER")
[ "$C" = 401 ] && ok "其余会话吊销" || bad "其余会话仍可用" "HTTP $C"
C=$(code -X POST "$PUB/v1/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"$UE\",\"password\":\"$UP\"}")
[ "$C" = 401 ] && ok "旧口令登不进" || bad "旧口令仍能登录" "HTTP $C"
UP=$NP

#-------------------------------------------------------------------------------
sec "8. 失败 10 次暂停，门户与后台共用"

LE="locke2e_${RUN}@example.com"; LP="Lock!E2e2026x"
register "$LE" "$LP" >/dev/null
for i in $(seq 1 10); do
  json -X POST "$PUB/v1/auth/login" -H "X-Real-IP: 192.0.2.$((30 + i))" -d "{\"email\":\"$LE\",\"password\":\"wrong-$i\"}" >/dev/null
done
H="$TMP/lock.h"
C=$(curl -s -o /dev/null -D "$H" -w '%{http_code}' -X POST "$PUB/v1/auth/login" -H 'X-Real-IP: 192.0.2.50' \
  -H 'Content-Type: application/json' -d "{\"email\":\"$LE\",\"password\":\"$LP\"}")
[ "$C" = 429 ] && grep -qi '^retry-after:' "$H" && ok "第 11 次口令对也 429，带 Retry-After" || bad "没有暂停" "HTTP $C"
C=$(code -X POST "$ADM/v1/auth/login" -H 'X-Real-IP: 192.0.2.51' -H 'Content-Type: application/json' \
  -d "{\"email\":\"$LE\",\"password\":\"$LP\"}")
[ "$C" = 429 ] && ok "后台登录同样 429（计数按账号共用）" || bad "后台不受门户的暂停约束" "HTTP $C"

#-------------------------------------------------------------------------------
sec "9. 登录失败审计记 anonymous"

N=$("$PSQL" -X -qAtc "SELECT count(*) FROM audit_events a JOIN users u ON u.id = a.resource_id
  WHERE u.email = '$LE' AND a.action = 'user.login_failed'" | tr -d '[:space:]')
W=$("$PSQL" -X -qAtc "SELECT count(*) FROM audit_events a JOIN users u ON u.id = a.resource_id
  WHERE u.email = '$LE' AND a.action = 'user.login_failed' AND (a.actor_kind <> 'anonymous' OR a.actor_id IS NOT NULL)" | tr -d '[:space:]')
[ "${N:-0}" -ge 1 ] && [ "${W:-1}" = 0 ] && ok "失败审计 $N 条，全部 anonymous、操作者为空" || bad "失败审计记错了操作者" "total=$N wrong=$W"

echo
echo "通过 $pass 项，失败 $fail 项"
[ "$fail" -eq 0 ]

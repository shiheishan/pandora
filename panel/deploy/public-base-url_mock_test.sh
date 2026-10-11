#!/usr/bin/env bash
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=public-base-url.sh
. "$DEPLOY/public-base-url.sh"
fail() { echo "public-base-url: $*" >&2; exit 1; }
tmp="$(mktemp)"; tmpbin="$(mktemp -d)"; trap 'rm -rf -- "$tmp" "$tmp.err" "$tmpbin"' EXIT

# 1. 规则矩阵：与 render-nginx.sh、platform/config 的 CanonicalPublicOrigin（生产）共用一张用例表
cases="$DEPLOY/fixtures/public-base-url-cases.txt"
n=0
while read -r verdict url; do
  case "$verdict" in ''|'#'*) continue ;; esac
  n=$((n + 1))
  if pandora_valid_public_base_url "$url"; then got=accept; else got=reject; fi
  [ "$got" = "$verdict" ] || fail "$url: want $verdict, got $got"
done <"$cases"
[ "$n" -ge 30 ] || fail "shared case table looks truncated ($n cases)"
if pandora_valid_public_base_url ''; then fail 'accepted an empty address'; fi

# 2. 取值：环境变量优先，去掉尾斜杠；不合规拒绝并给出重跑命令
# ip 桩：本机出公网的源地址取自 IP_SRC（空表示没有路由）
mkdir -p "$tmpbin"
cat >"$tmpbin/ip" <<'IPSTUB'
#!/usr/bin/env bash
[ -n "${IP_SRC:-}" ] || exit 2
echo "192.0.2.1 via 192.0.2.254 dev eth0 src $IP_SRC uid 0"
IPSTUB
chmod +x "$tmpbin/ip"
PATH="$tmpbin:$PATH"
got="$(PANDORA_PUBLIC_BASE_URL=https://panel.example.test/ pandora_resolve_public_base_url ./install.sh </dev/null)"
[ "$got" = https://panel.example.test ] || fail "resolved '$got'"
got="$(PANDORA_PUBLIC_BASE_URL=https://203.0.113.10 IP_SRC=198.51.100.7 pandora_resolve_public_base_url ./install.sh </dev/null)"
[ "$got" = https://203.0.113.10 ] || fail "an explicit IP address lost to detection: '$got'"
# 本机公网 IPv4 探测：只认公网地址；NAT 后面（私网源地址）、没有路由都是空
[ "$(IP_SRC=203.0.113.10 pandora_detect_public_ipv4)" = 203.0.113.10 ] || fail 'public source address not detected'
[ -z "$(IP_SRC=10.0.0.5 pandora_detect_public_ipv4)" ] || fail 'private source address detected as public'
[ -z "$(IP_SRC=100.64.1.1 pandora_detect_public_ipv4)" ] || fail 'CGNAT source address detected as public'
[ -z "$(IP_SRC= pandora_detect_public_ipv4)" ] || fail 'no route still produced an address'
# 无人值守、没给地址：用本机公网 IPv4，并在 stderr 说明
got="$(PANDORA_ASSUME_YES=1 IP_SRC=203.0.113.10 pandora_resolve_public_base_url ./install.sh 2>"$tmp.err" </dev/null)" \
  || fail 'unattended install did not fall back to the public IPv4'
[ "$got" = https://203.0.113.10 ] || fail "fallback resolved '$got'"
grep -Fq '用本机公网 IPv4：https://203.0.113.10' "$tmp.err" || fail "fallback not explained: $(cat "$tmp.err")"
# 非终端 stdin 也不去读：不会卡住，也不会误吃管道里的内容
got="$(printf 'https://panel.example.test\n' | IP_SRC=203.0.113.10 pandora_resolve_public_base_url 'bash install.sh' 2>/dev/null)"
[ "$got" = https://203.0.113.10 ] || fail "read the address from a non-terminal stdin: '$got'"
# 没有公网 IPv4（NAT 后面）又没给：中文报错，两种重跑命令都给
if err="$(PANDORA_ASSUME_YES=1 IP_SRC=10.0.0.5 pandora_resolve_public_base_url './install.sh' 2>&1 </dev/null)"; then
  fail 'missing address was accepted behind NAT'
fi
grep -Fq '首装需要面板的对外地址' <<<"$err" || fail "missing-address message: $err"
grep -Fq 'sudo PANDORA_PUBLIC_BASE_URL=https://panel.example.com ./install.sh' <<<"$err" || fail 'no rerun command'
grep -Fq 'PANDORA_PUBLIC_BASE_URL=https://<本机公网IPv4>' <<<"$err" || fail 'no IP rerun command'
if err="$(PANDORA_PUBLIC_BASE_URL=https://10.0.0.1 pandora_resolve_public_base_url 'bash install.sh' 2>&1 </dev/null)"; then
  fail 'private IP address was accepted'
fi
grep -Fq '面板对外地址不合规：https://10.0.0.1' <<<"$err" || fail "invalid-address message: $err"
grep -Fq 'bash install.sh' <<<"$err" || fail 'invalid-address message lacks the rerun command'

# 3. .env 单键读取：不 source，最后一次出现为准，值里可以有等号
printf 'A=1\nB=x=y\nA=2\n' >"$tmp"
[ "$(pandora_env_file_value "$tmp" A)" = 2 ] && [ "$(pandora_env_file_value "$tmp" B)" = x=y ] \
  && [ -z "$(pandora_env_file_value "$tmp" C)" ] || fail 'env_file_value'

# 4. 安装器用这一份，不另写校验；发布包带上它
for script in install.sh; do
  grep -Fq '. "$' "$DEPLOY/$script" && grep -Fq '/public-base-url.sh"' "$DEPLOY/$script" \
    || fail "$script does not source public-base-url.sh"
  grep -Fq 'pandora_resolve_public_base_url' "$DEPLOY/$script" || fail "$script does not resolve the address"
  if grep -Eq '^[[:space:]]*(valid_public_base_url|env_file_value)\(\)' "$DEPLOY/$script"; then
    fail "$script keeps its own copy of the validator"
  fi
done
[ "$(grep -c 'install.sh public-base-url.sh' "$DEPLOY/build-release.sh")" -eq 2 ] \
  || fail 'build-release.sh does not copy and archive public-base-url.sh'

# 5. install.sh：不再写示例值；.env 只在首装写
inst="$DEPLOY/install.sh"
if grep -Fq 'AEGIS_PUBLIC_BASE_URL=https://CHANGE_ME' "$inst"; then fail 'install.sh still writes the example address'; fi
grep -Fq 'AEGIS_PUBLIC_BASE_URL=${PUBLIC_BASE_URL}' "$inst" || fail 'install.sh does not write the resolved address'
# 写 .env 的 heredoc 必须落在 MODE=install 的分支里
awk '
  /^if \[\[ "\$MODE" = install \]\]; then$/ { inside = 1 }
  inside && /^cat > "\$INSTALL_DIR\/deploy\/\.env" <</ { guarded = 1 }
  inside && /^fi$/ { inside = 0 }
  !inside && /^cat > "\$INSTALL_DIR\/deploy\/\.env" <</ { unguarded = 1 }
  END { exit !(guarded && !unguarded) }
' "$inst" || fail 'install.sh writes .env outside the first-install branch'

printf 'public-base-url mock: PASS\n'

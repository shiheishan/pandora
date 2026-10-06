#!/usr/bin/env bash
# [INPUT]: 依赖同目录 public-base-url.sh，以及 install.sh、install-native.sh、build-release.sh 的源码
# [OUTPUT]: 首装对外地址闸门的契约：合规/不合规的地址矩阵、环境变量与无人值守下的取值与中文报错、两个安装脚本都经这一份（不各写一份）、发布包带上它、install-native.sh 不再写示例值且升级不动 .env
# [POS]: deploy 的桩测试，CI panel-deploy.yml 必跑；不需要 root、终端或网络
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=public-base-url.sh
. "$DEPLOY/public-base-url.sh"
fail() { echo "public-base-url: $*" >&2; exit 1; }

# 1. 规则矩阵：与 render-nginx.sh 相同
for url in https://panel.example.test https://panel.example.test/ https://Panel.Example.TEST https://a-b.c.example.test; do
  pandora_valid_public_base_url "$url" || fail "rejected valid $url"
done
for url in '' http://panel.example.test https://panel.example.test:8443 https://panel.example.test/x \
    https://1.2.3.4 https://localhost https://a.localhost https://u@panel.example.test \
    https://CHANGE_ME_TO_YOUR_PANEL_DOMAIN 'https://panel.example.test?x=1'; do
  if pandora_valid_public_base_url "$url"; then fail "accepted invalid '$url'"; fi
done

# 2. 取值：环境变量优先，去掉尾斜杠；无人值守且没给就中文报错并给出重跑命令；不合规同样拒绝
got="$(PANDORA_PUBLIC_BASE_URL=https://panel.example.test/ pandora_resolve_public_base_url ./install.sh </dev/null)"
[ "$got" = https://panel.example.test ] || fail "resolved '$got'"
if err="$(PANDORA_ASSUME_YES=1 pandora_resolve_public_base_url './install.sh' 2>&1 </dev/null)"; then
  fail 'missing address was accepted'
fi
grep -Fq '首装需要面板的对外地址' <<<"$err" || fail "missing-address message: $err"
grep -Fq 'sudo PANDORA_PUBLIC_BASE_URL=https://panel.example.com ./install.sh' <<<"$err" || fail 'no rerun command'
# 非终端 stdin 也不去读：不会卡住，也不会误吃管道里的内容
if printf 'https://panel.example.test\n' | pandora_resolve_public_base_url 'bash install-native.sh' >/dev/null 2>&1; then
  fail 'read the address from a non-terminal stdin'
fi
if err="$(PANDORA_PUBLIC_BASE_URL=https://10.0.0.1 pandora_resolve_public_base_url 'bash install-native.sh' 2>&1 </dev/null)"; then
  fail 'IP address was accepted'
fi
grep -Fq '面板对外地址不合规：https://10.0.0.1' <<<"$err" || fail "invalid-address message: $err"
grep -Fq 'bash install-native.sh' <<<"$err" || fail 'invalid-address message lacks the rerun command'

# 3. .env 单键读取：不 source，最后一次出现为准，值里可以有等号
tmp="$(mktemp)"; trap 'rm -f -- "$tmp"' EXIT
printf 'A=1\nB=x=y\nA=2\n' >"$tmp"
[ "$(pandora_env_file_value "$tmp" A)" = 2 ] && [ "$(pandora_env_file_value "$tmp" B)" = x=y ] \
  && [ -z "$(pandora_env_file_value "$tmp" C)" ] || fail 'env_file_value'

# 4. 两个安装脚本共用这一份，不各自再写校验；发布包带上它
for script in install.sh install-native.sh; do
  grep -Fq '. "$' "$DEPLOY/$script" && grep -Fq '/public-base-url.sh"' "$DEPLOY/$script" \
    || fail "$script does not source public-base-url.sh"
  grep -Fq 'pandora_resolve_public_base_url' "$DEPLOY/$script" || fail "$script does not resolve the address"
  if grep -Eq '^[[:space:]]*(valid_public_base_url|env_file_value)\(\)' "$DEPLOY/$script"; then
    fail "$script keeps its own copy of the validator"
  fi
done
[ "$(grep -c 'install-native.sh public-base-url.sh' "$DEPLOY/build-release.sh")" -eq 2 ] \
  || fail 'build-release.sh does not copy and archive public-base-url.sh'

# 5. install-native.sh：不再写示例值；.env 只在首装写
native="$DEPLOY/install-native.sh"
if grep -Fq 'AEGIS_PUBLIC_BASE_URL=https://CHANGE_ME' "$native"; then fail 'install-native.sh still writes the example address'; fi
grep -Fq 'AEGIS_PUBLIC_BASE_URL=${PUBLIC_BASE_URL}' "$native" || fail 'install-native.sh does not write the resolved address'
# 写 .env 的 heredoc 必须落在 MODE=install 的分支里
awk '
  /^if \[\[ "\$MODE" = install \]\]; then$/ { inside = 1 }
  inside && /^cat > "\$INSTALL_DIR\/deploy\/\.env" <</ { guarded = 1 }
  inside && /^fi$/ { inside = 0 }
  !inside && /^cat > "\$INSTALL_DIR\/deploy\/\.env" <</ { unguarded = 1 }
  END { exit !(guarded && !unguarded) }
' "$native" || fail 'install-native.sh writes .env outside the first-install branch'

printf 'public-base-url mock: PASS\n'

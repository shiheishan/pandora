#!/usr/bin/env bash
# [INPUT]: 依赖同目录 render-nginx.sh、update-cloudflare-realip.sh、nginx-aegis.conf，以及 build-release.sh / install-linux-binaries.sh / install-native.sh 的文件清单；curl 由桩替身
# [OUTPUT]: 真实来源 IP 信任表的桩测试：全新渲染写出不信任任何代理的默认文件、已有文件绝不覆盖、Cloudflare 更新脚本写出网段且坏列表不动旧文件、更新脚本与 nginx 模板随发布包与两个安装脚本落到 /opt/aegispanel/deploy
# [POS]: deploy 安装链「全新安装 nginx -t 能过、默认不误信 CF-Connecting-IP、Cloudflare 显式启用、升级不覆盖」的回归测试，只用虚构域名与文档网段，不碰真实 nginx 与网络；panel-deploy.yml 点名跑它
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
TEST_DIR="$(mktemp -d)"
trap 'rm -rf -- "$TEST_DIR"' EXIT

fail() { printf 'cloudflare-realip mock: %s\n' "$*" >&2; exit 1; }
# 「不应出现」不能写成取反的 grep：set -e 对取反的命令不生效
refute() {
  if grep "$@"; then
    fail "unexpected match: $*"
  fi
}

# ---------------------------------------------------------------------------
# 1. 模板 include 的路径就是渲染器与更新脚本的默认路径（三处是同一个文件）
# ---------------------------------------------------------------------------
realip_path=/etc/aegispanel/cloudflare-realip.conf
grep -Fq "include $realip_path;" "$SCRIPT_DIR/nginx-aegis.conf" || fail "template no longer includes $realip_path"
grep -Fq "REALIP_FILE=\"\${3:-$realip_path}\"" "$SCRIPT_DIR/render-nginx.sh" || fail "render-nginx.sh default differs from the template include"
grep -Fq "TARGET_FILE=\"\${1:-$realip_path}\"" "$SCRIPT_DIR/update-cloudflare-realip.sh" || fail "update-cloudflare-realip.sh default differs from the template include"

# ---------------------------------------------------------------------------
# 2. 全新渲染：信任表不存在 → 写一份只有注释的默认文件（没有任何 nginx 指令）
# ---------------------------------------------------------------------------
printf 'AEGIS_ADMIN_PATH=ops_0123456789abcdef0123456789abcdef\nAEGIS_PUBLIC_BASE_URL=https://panel.example.test\n' >"$TEST_DIR/.env"
fresh="$TEST_DIR/fresh/etc/aegispanel/cloudflare-realip.conf"
out="$("$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/.env" "$TEST_DIR/aegis.conf" "$fresh")"
[[ -f "$fresh" ]] || fail "fresh render did not create the real-IP file"
grep -Fq 'created' <<<"$out" || fail "fresh render did not say it created the default"
mode="$(stat -c '%a' "$fresh" 2>/dev/null || stat -f '%Lp' "$fresh")"
[[ "$mode" == 644 ]] || fail "default real-IP file mode is $mode, want 644 (nginx workers read it)"
# 默认文件不得含任何指令：没有 set_real_ip_from 就不信任任何来源的 CF-Connecting-IP / X-Real-IP
directives="$(grep -vE '^[[:space:]]*(#|$)' "$fresh" || true)"
[[ -z "$directives" ]] || fail "default real-IP file carries directives: $directives"
grep -Fq 'update-cloudflare-realip.sh' "$fresh" || fail "default file does not name the Cloudflare enable step"

# ---------------------------------------------------------------------------
# 3. 升级重渲染：已有文件（Cloudflare 网段或运维手写）原样保留
# ---------------------------------------------------------------------------
kept="$TEST_DIR/kept.conf"
printf 'set_real_ip_from 192.0.2.0/24;\nreal_ip_header CF-Connecting-IP;\n' >"$kept"
cp "$kept" "$TEST_DIR/kept.before"
out="$("$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/.env" "$TEST_DIR/aegis.conf" "$kept")"
cmp -s "$kept" "$TEST_DIR/kept.before" || fail "re-render overwrote an existing real-IP file"
refute -Fq 'created' <<<"$out"
# 渲染出的配置照旧 include 它
grep -Fq "include $realip_path;" "$TEST_DIR/aegis.conf" || fail "rendered config lost the include"

# ---------------------------------------------------------------------------
# 4. Cloudflare 显式启用：桩 curl 给出文档网段，写出 set_real_ip_from + CF 头
# ---------------------------------------------------------------------------
mkdir -p "$TEST_DIR/bin"
cat >"$TEST_DIR/bin/curl" <<'STUB'
#!/usr/bin/env bash
# 只认脚本里那两种调用：URL 结尾 ips-v4 / ips-v6，-o 指定输出
set -euo pipefail
out="" url=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) out="$2"; shift 2 ;;
    https://*) url="$1"; shift ;;
    *) shift ;;
  esac
done
[[ -n "$out" && -n "$url" ]] || exit 2
case "$url" in
  */ips-v4) cat "$CF_STUB_V4" >"$out" ;;
  */ips-v6) cat "$CF_STUB_V6" >"$out" ;;
  *) exit 22 ;;
esac
STUB
chmod 0755 "$TEST_DIR/bin/curl"
printf '192.0.2.0/24\n198.51.100.0/24\n' >"$TEST_DIR/v4"
printf '2001:db8::/32\n' >"$TEST_DIR/v6"
cf="$TEST_DIR/cf/etc/aegispanel/cloudflare-realip.conf"
PATH="$TEST_DIR/bin:$PATH" CF_STUB_V4="$TEST_DIR/v4" CF_STUB_V6="$TEST_DIR/v6" \
  "$SCRIPT_DIR/update-cloudflare-realip.sh" "$cf" >/dev/null
for net in 192.0.2.0/24 198.51.100.0/24 2001:db8::/32; do
  grep -Fxq "set_real_ip_from $net;" "$cf" || fail "Cloudflare file misses $net"
done
grep -Fxq 'real_ip_header CF-Connecting-IP;' "$cf" || fail "Cloudflare file misses real_ip_header"
[[ "$(grep -c '^real_ip_header' "$cf")" == 1 ]] || fail "real_ip_header must appear exactly once"
cp "$cf" "$TEST_DIR/cf.before"

# 坏列表（畸形、为空、v4 里混 v6）失败且不动已有文件
for bad in 'not-a-network' '' '2001:db8::/32'; do
  printf '%s\n' "$bad" >"$TEST_DIR/v4bad"
  if PATH="$TEST_DIR/bin:$PATH" CF_STUB_V4="$TEST_DIR/v4bad" CF_STUB_V6="$TEST_DIR/v6" \
      "$SCRIPT_DIR/update-cloudflare-realip.sh" "$cf" >/dev/null 2>&1; then
    fail "malformed v4 list accepted: $bad"
  fi
  cmp -s "$cf" "$TEST_DIR/cf.before" || fail "failed update changed the existing file"
done
# 之后再渲染也不碰 Cloudflare 网段
"$SCRIPT_DIR/render-nginx.sh" "$TEST_DIR/.env" "$TEST_DIR/aegis.conf" "$cf" >/dev/null
cmp -s "$cf" "$TEST_DIR/cf.before" || fail "render after Cloudflare enable overwrote the networks"

# ---------------------------------------------------------------------------
# 5. 安装链：更新脚本进发布包，更新脚本与模板落到 /opt/aegispanel/deploy
# ---------------------------------------------------------------------------
# build-release.sh 的两份脚本清单（拷贝与 tar 成员）都要有它，否则包里缺、或 SHA256SUMS 有而 tar 无
n="$(grep -c 'render-nginx.sh update-cloudflare-realip.sh; do' "$SCRIPT_DIR/build-release.sh")"
[[ "$n" == 2 ]] || fail "build-release.sh lists update-cloudflare-realip.sh in $n of 2 script loops"
grep -Eq '^for script in .*\brender-nginx\.sh\b.*\bupdate-cloudflare-realip\.sh\b.*; do' "$SCRIPT_DIR/install-linux-binaries.sh" \
  || fail "install-linux-binaries.sh does not stage update-cloudflare-realip.sh"
# render-nginx.sh 读同目录模板：Docker 版安装不装模板，装完提示的那条渲染命令就会失败
grep -Eq '^for data_file in .*\bnginx-aegis\.conf\b' "$SCRIPT_DIR/install-linux-binaries.sh" \
  || fail "install-linux-binaries.sh does not stage nginx-aegis.conf next to render-nginx.sh"
grep -Fq '"$SCRIPT_DIR/update-cloudflare-realip.sh"' "$SCRIPT_DIR/install-native.sh" \
  || fail "install-native.sh does not copy update-cloudflare-realip.sh"
grep -Fq '"$SCRIPT_DIR/nginx-aegis.conf"' "$SCRIPT_DIR/install-native.sh" \
  || fail "install-native.sh does not copy nginx-aegis.conf"
# 两个安装脚本收尾都要把 Cloudflare 的启用步骤告诉人
grep -Fq 'update-cloudflare-realip.sh' "$SCRIPT_DIR/install.sh" || fail "install.sh does not mention the Cloudflare step"

printf 'cloudflare-realip mock: PASS\n'

#!/usr/bin/env bash
# build-release.sh 的 Go 工具链固定、geoip 下载与发布树：不需要真 Go、npm、GNU tar、网络。
# 在临时目录里放一份 build-release.sh 与桩 go / npm / tar / curl，实跑整条构建，验证：
#   1. 正常构建产物与 release-artifact.env（5 键）、无 migrations/goose/旧二进制名；
#   2. 所有 go build 带 GOTOOLCHAIN=go<panel|pdnd 版本>，BUILD-INFO 写两版本；
#   3. curl 只调一次，缓存命中后不再下载；
#   4–6. 摘要/只给 URL/工具链三种失败；
#   7. 脚本静态禁词；
#   8. pdnd 发布清单（摘要与二进制一致、serial 编进 pdnd）与可选签名。
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
fail() { printf 'build-release toolchain: %s\n' "$*" >&2; exit 1; }
for tool in make sha256sum realpath gzip; do
  command -v "$tool" >/dev/null 2>&1 || { printf 'build-release toolchain: SKIP (no %s)\n' "$tool"; exit 0; }
done

T="$(mktemp -d)"
trap 'rm -rf -- "$T"' EXIT
PANEL_TC_VERSION=1.26.8
PDND_TC_VERSION=1.26.7
FAKE_XDB='fake-xdb'
FAKE_XDB_SHA="$(printf '%s' "$FAKE_XDB" | sha256sum | awk '{print $1}')"

mkdir -p "$T/tree/panel" "$T/tree/pdnd" "$T/stub"
cp -R "$DEPLOY" "$T/tree/panel/deploy"
printf 'module example.test/panel\ngo %s\n' "$PANEL_TC_VERSION" >"$T/tree/panel/go.mod"
printf 'module example.test/pdnd\ngo %s\n' "$PDND_TC_VERSION" >"$T/tree/pdnd/go.mod"
cat >"$T/tree/panel/Makefile" <<'MK'
frontend-embed:
	mkdir -p web/admin web/portal
	echo real > web/admin/index.html
	echo real > web/portal/index.html
MK

# 清单里 p7 尚未改名的文件：占位即可，测的是 build-release 拷贝与失败语义。
for f in nginx-pandora.conf logrotate-pandora RUNBOOK.md; do
  [ -f "$T/tree/panel/deploy/$f" ] || : >"$T/tree/panel/deploy/$f"
done
for u in pandora-backup.service pandora-backup.timer pandora-health.service pandora-health.timer \
  pandora-tls-renew.service pandora-tls-renew.timer; do
  [ -f "$T/tree/panel/deploy/systemd/$u" ] || : >"$T/tree/panel/deploy/systemd/$u"
done

mkdir -p "$T/tree/panel/cmd/pandora" "$T/tree/pdnd"
: >"$T/tree/panel/cmd/pandora/main.go"
: >"$T/tree/pdnd/main.go"

GEOIP_ENV=(
  "PANDORA_GEOIP_V4_URL=https://example.test/x.xdb"
  "PANDORA_GEOIP_V4_SHA256=$FAKE_XDB_SHA"
)

cat >"$T/stub/go" <<'GO'
#!/usr/bin/env bash
local_tc="${FAKE_GO_LOCAL:-go1.27.1}"
effective() {
  if [ "${FAKE_GO_IGNORE_TOOLCHAIN:-}" != 1 ] && [[ "${GOTOOLCHAIN:-}" == go1* ]]; then echo "$GOTOOLCHAIN"; else echo "$local_tc"; fi
}
[ -n "${FAKE_GO_ARGS_LOG:-}" ] && printf '%s|%s\n' "$PWD" "$*" >>"$FAKE_GO_ARGS_LOG"
printf '%s|%s|GOTOOLCHAIN=%s\n' "$1" "$PWD" "${GOTOOLCHAIN-<unset>}" >>"$FAKE_GO_LOG"
case "$1" in
  env) [ "${2:-}" = GOVERSION ] && effective ;;
  version)
    if [ -n "${2:-}" ]; then printf '%s: %s\n' "$2" "$(sed -n 's/^gobuild://p' "$2")"; else echo "go version $(effective) linux/amd64"; fi ;;
  build)
    out=""; prev=""
    for a in "$@"; do [ "$prev" = -o ] && out="$a"; prev="$a"; done
    printf 'gobuild:%s\n' "${FAKE_GO_BUILD_TC:-$(effective)}" >"$out" ;;
  run)
    if [[ "$*" == *./release/signmanifest* ]]; then
      out=""; key=""; infile=""; prev=""
      for a in "$@"; do
        [ "$prev" = -out ] && out="$a"
        [ "$prev" = -key ] && key="$a"
        [ "$prev" = -in ] && infile="$a"
        case "$a" in -out|-key|-in) prev="$a" ;; *) prev="" ;; esac
      done
      printf '{"key_id":"0000000000000000","sig":"stub"}\n' >"$out"
      [ -n "${FAKE_SIGN_LOG:-}" ] && printf 'signmanifest key=%s in=%s out=%s\n' "$key" "$infile" "$out" >>"$FAKE_SIGN_LOG"
    fi ;;
esac
exit 0
GO
printf '#!/usr/bin/env bash\nexit 0\n' >"$T/stub/npm"
cat >"$T/stub/tar" <<'TAR'
#!/usr/bin/env bash
prev=""
for a in "$@"; do [ "$prev" = -cf ] && : >"$a"; prev="$a"; done
exit 0
TAR
cat >"$T/stub/curl" <<'CURL'
#!/usr/bin/env bash
out=""
prev=""
for a in "$@"; do
  case "$a" in
    -o) prev=o ;;
    -*) ;;
    *)
      if [ "$prev" = o ]; then out="$a"; prev=""; fi ;;
  esac
done
printf '%s' 'fake-xdb' >"$out"
printf 'curl %s\n' "$*" >>"$FAKE_CURL_LOG"
exit 0
CURL
chmod +x "$T/stub/go" "$T/stub/npm" "$T/stub/tar" "$T/stub/curl"

run_build() {
  local keep_out=0
  if [ "${1:-}" = --keep-out ]; then keep_out=1; shift; fi
  if [ "$keep_out" = 0 ]; then
    rm -rf "$T/out"
    : >"$T/curl.log"
  fi
  : >"$T/go.log"
  : >"$T/sign.log"
  : >"$T/goargs.log"
  env PATH="$T/stub:$PATH" FAKE_GO_LOG="$T/go.log" FAKE_CURL_LOG="$T/curl.log" \
    FAKE_SIGN_LOG="$T/sign.log" FAKE_GO_ARGS_LOG="$T/goargs.log" \
    PANDORA_VERSION=toolchain-test PANDORA_BUILD_SERIAL=1792540800 GOTOOLCHAIN=local "$@" \
    bash "$T/tree/panel/deploy/build-release.sh" "$T/out" >"$T/stdout" 2>"$T/stderr"
}

assert_release_tree() {
  local arch root
  arch="$1"
  root="$T/out/pandora-panel_toolchain-test_linux_${arch}"
  [ -f "$root/bin/pandora" ] || fail "missing bin/pandora ($arch)"
  [ -f "$root/pdnd-dist/pandora-native-linux-amd64" ] || fail "missing amd64 native ($arch)"
  [ -f "$root/pdnd-dist/pandora-native-linux-arm64" ] || fail "missing arm64 native ($arch)"
  [ -f "$root/geoip/ip2region_v4.xdb" ] || fail "missing geoip ($arch)"
  [ -f "$root/deploy/release-artifact.env" ] || fail "missing release-artifact.env ($arch)"
  [ -f "$root/deploy/BUILD-INFO" ] || fail "missing BUILD-INFO ($arch)"
  grep -qx 'PANDORA_RELEASE_VERSION=toolchain-test' "$root/deploy/release-artifact.env" || fail "release version ($arch)"
  for key in PANDORA_BINARY_SHA256 PANDORA_NATIVE_ARTIFACT_AMD64_SHA256 \
    PANDORA_NATIVE_ARTIFACT_ARM64_SHA256 PANDORA_GEOIP_V4_SHA256; do
    grep -q "^${key}=" "$root/deploy/release-artifact.env" || fail "missing $key ($arch)"
  done
  local n_units
  n_units="$(find "$root/deploy/systemd" -maxdepth 1 -type f | wc -l | tr -d ' ')"
  [ "$n_units" = 8 ] || fail "expected 8 systemd units ($arch), got $n_units"
  local script
  for script in install.sh install-lib.sh public-base-url.sh platform.sh admin-url.sh bootstrap.sh \
    render-nginx.sh backup-postgres.sh verify-backup.sh restore-postgres.sh healthcheck.sh \
    edge-tls.sh update-cloudflare-realip.sh pg-revive.sh psql.sh; do
    [ -f "$root/deploy/$script" ] || fail "missing deploy/$script ($arch)"
  done
  for script in configure-app-role.sql nginx-pandora.conf postgresql-pandora.conf logrotate-pandora \
    backup-webdav.example.json RUNBOOK.md config.example.yaml secrets.env.example; do
    [ -f "$root/deploy/$script" ] || fail "missing deploy/$script ($arch)"
  done
  [ ! -e "$root/migrations" ] || fail "migrations must not exist ($arch)"
  find "$root" -name goose -print -quit | grep -q . && fail "goose must not exist ($arch)"
  find "$root" -name 'aegis-*' -print -quit | grep -q . && fail "aegis-named file must not exist ($arch)"
  grep -q 'geoip/ip2region_v4.xdb' "$root/SHA256SUMS" || fail "SHA256SUMS missing geoip line ($arch)"
  [ -f "$root/pdnd-dist/pdnd-manifest.json" ] || fail "missing pdnd-manifest.json ($arch)"
  local amd64_sha arm64_sha
  amd64_sha="$(sha256sum "$root/pdnd-dist/pandora-native-linux-amd64" | awk '{print $1}')"
  arm64_sha="$(sha256sum "$root/pdnd-dist/pandora-native-linux-arm64" | awk '{print $1}')"
  grep -Fq "\"linux-amd64\": { \"file\": \"pandora-native-linux-amd64\", \"sha256\": \"$amd64_sha\" }" "$root/pdnd-dist/pdnd-manifest.json" \
    || fail "manifest amd64 sha ($arch)"
  grep -Fq "\"linux-arm64\": { \"file\": \"pandora-native-linux-arm64\", \"sha256\": \"$arm64_sha\" }" "$root/pdnd-dist/pdnd-manifest.json" \
    || fail "manifest arm64 sha ($arch)"
  grep -Fq '"serial": 1792540800,' "$root/pdnd-dist/pdnd-manifest.json" || fail "manifest serial ($arch)"
  grep -Fq '"version": "toolchain-test",' "$root/pdnd-dist/pdnd-manifest.json" || fail "manifest version ($arch)"
  grep -Fq '"schema": 1,' "$root/pdnd-dist/pdnd-manifest.json" || fail "manifest schema ($arch)"
  grep -q 'pdnd-dist/pdnd-manifest.json' "$root/SHA256SUMS" || fail "SHA256SUMS missing manifest ($arch)"
}

# --- 1. 正常构建 ---
run_build "${GEOIP_ENV[@]}" || { cat "$T/stderr" >&2; fail 'build with pinned toolchains failed'; }
assert_release_tree amd64
assert_release_tree arm64

panel_tc="go$PANEL_TC_VERSION"
pdnd_tc="go$PDND_TC_VERSION"
mutating="$(grep -E '^build\|' "$T/go.log")"
[ -n "$mutating" ] || fail 'the stub go was never asked to build'
bad="$(grep -Ev "GOTOOLCHAIN=(${panel_tc}|${pdnd_tc})\$" <<<"$mutating" || true)"
[ -z "$bad" ] || fail "go invoked without a pinned toolchain:\n$bad"
grep -E '^build\|.*/tree/pdnd\|' "$T/go.log" | grep -vq "GOTOOLCHAIN=${pdnd_tc}\$" && fail 'a pdnd build did not use the pdnd go.mod toolchain'
grep -qE '^build\|.*/tree/pdnd\|' "$T/go.log" || fail 'pdnd was never built'
grep -E '^build\|' "$T/go.log" | grep -v '/tree/pdnd|' | grep -vq "GOTOOLCHAIN=${panel_tc}\$" && fail 'a panel build did not use the panel go.mod toolchain'
[ "$(grep -c '^build|.*/tree/panel|' "$T/go.log")" -eq 2 ] || fail 'expected two panel pandora builds (amd64+arm64)'
info="$T/out/pandora-panel_toolchain-test_linux_amd64/deploy/BUILD-INFO"
grep -qx "PANEL_GO_TOOLCHAIN=$panel_tc" "$info" && grep -qx "PDND_GO_TOOLCHAIN=$pdnd_tc" "$info" || fail "BUILD-INFO content: $(cat "$info")"

for arch in amd64 arm64; do
  [ ! -f "$T/out/pandora-panel_toolchain-test_linux_${arch}/pdnd-dist/pdnd-manifest.json.sig" ] \
    || fail "unexpected .sig without signing key ($arch)"
done
[ ! -s "$T/sign.log" ] || fail 'sign.log must be empty without signing key'
# goargs.log 每行是「目录|全部参数」：pdnd 的四次 build（两棵树 × 两架构）都要带 buildSerial。
pdnd_builds="$(grep -E '/tree/pdnd\|build ' "$T/goargs.log" || true)"
[ "$(grep -c . <<<"$pdnd_builds")" = 4 ] || fail "expected four pdnd builds in goargs.log, got: $pdnd_builds"
grep -vq -- '-X main.buildSerial=1792540800' <<<"$pdnd_builds" && fail 'pdnd build missing buildSerial ldflag'

# --- 3. curl 只一次；再构建命中缓存 ---
curl_calls="$(wc -l <"$T/curl.log" | tr -d ' ')"
[ "$curl_calls" = 1 ] || fail "expected one curl call, got $curl_calls"
run_build --keep-out "${GEOIP_ENV[@]}" || fail 'cached rebuild failed'
[ "$(wc -l <"$T/curl.log" | tr -d ' ')" = 1 ] || fail 'curl must not run again when cache hits'

# --- 4. 摘要对不上 ---
rm -rf "$T/out"
if run_build "${GEOIP_ENV[0]}" PANDORA_GEOIP_V4_SHA256=0000000000000000000000000000000000000000000000000000000000000000; then
  fail 'wrong geoip digest was accepted'
fi
grep -q 'SHA-256 mismatch' "$T/stderr" || fail "digest message missing: $(cat "$T/stderr")"
cache_glob="$T/out/.cache/ip2region_v4-*.xdb"
if compgen -G "$cache_glob" >/dev/null; then
  fail 'cache file left after digest mismatch'
fi

# --- 5. 只给 URL ---
if run_build PANDORA_GEOIP_V4_URL=https://example.test/x.xdb; then
  fail 'URL without SHA was accepted'
fi
grep -q 'PANDORA_GEOIP_V4_URL and PANDORA_GEOIP_V4_SHA256' "$T/stderr" || fail "override pair message missing: $(cat "$T/stderr")"

# --- 6. 工具链三种失败 ---
if run_build FAKE_GO_IGNORE_TOOLCHAIN=1 "${GEOIP_ENV[@]}"; then fail 'go ignoring GOTOOLCHAIN was accepted'; fi
grep -q 'Go toolchain mismatch' "$T/stderr" || fail "mismatch message missing: $(cat "$T/stderr")"

if run_build FAKE_GO_BUILD_TC=go1.20.0 "${GEOIP_ENV[@]}"; then fail 'wrong binary Go version was accepted'; fi
grep -q 'was built with' "$T/stderr" || fail "binary mismatch message missing: $(cat "$T/stderr")"

printf 'module example.test/panel\ngo 1.26\n' >"$T/tree/panel/go.mod"
if run_build "${GEOIP_ENV[@]}"; then fail 'go directive without patch was accepted'; fi
grep -q 'full x.y.z' "$T/stderr" || fail "directive message missing: $(cat "$T/stderr")"
printf 'module example.test/panel\ngo %s\n' "$PANEL_TC_VERSION" >"$T/tree/panel/go.mod"

# --- 8. pdnd 发布清单与签名（带私钥与失败路径）---
printf 'stub-key\n' >"$T/test.key"
chmod 600 "$T/test.key"
run_build "${GEOIP_ENV[@]}" PDND_SIGNING_KEY_FILE="$T/test.key" || fail 'build with signing key failed'
for arch in amd64 arm64; do
  root="$T/out/pandora-panel_toolchain-test_linux_${arch}"
  [ -f "$root/pdnd-dist/pdnd-manifest.json.sig" ] || fail "missing .sig ($arch)"
  grep -q 'pdnd-dist/pdnd-manifest.json.sig' "$root/SHA256SUMS" || fail "SHA256SUMS missing .sig ($arch)"
done
sign_lines="$(wc -l <"$T/sign.log" | tr -d ' ')"
[ "$sign_lines" = 2 ] || fail "expected two signmanifest calls, got $sign_lines"
grep -q "key=$T/test.key" "$T/sign.log" || fail 'sign log missing key path'
grep -q 'in=.*/pdnd-dist/pdnd-manifest.json' "$T/sign.log" || fail 'sign log missing manifest path'

if run_build "${GEOIP_ENV[@]}" PDND_SIGNING_KEY_FILE="$T/missing.key"; then
  fail 'missing signing key file was accepted'
fi

if run_build "${GEOIP_ENV[@]}" PDND_SIGNING_KEY_FILE="$T/test.key" PANDORA_BUILD_SERIAL=0; then
  fail 'signing with serial 0 was accepted'
fi
grep -q serial "$T/stderr" || fail "serial 0 message missing: $(cat "$T/stderr")"

if run_build "${GEOIP_ENV[@]}" PANDORA_BUILD_SERIAL=abc; then
  fail 'non-numeric BUILD_SERIAL was accepted'
fi

# --- 7. 静态禁词 ---
for word in aegis goose PREBUILT migrations; do
  if grep -qi "$word" "$DEPLOY/build-release.sh"; then
    fail "build-release.sh must not contain $word"
  fi
done
if grep -nE '(^|[^[:alnum:]_.])go1\.[0-9]+\.[0-9]+' "$DEPLOY/build-release.sh" | grep -v '^[0-9]*:[[:space:]]*#'; then
  fail 'build-release.sh hard-codes a Go version'
fi

grep -Fq 'require_binary_toolchain "$PANEL_TOOLCHAIN" "$target/bin/pandora"' "$DEPLOY/build-release.sh" || fail 'panel binary is not checked after build'
grep -Fq 'require_binary_toolchain "$PDND_TOOLCHAIN" "$target/pdnd-dist/$pdnd_name"' "$DEPLOY/build-release.sh" || fail 'pdnd binaries are not checked'

printf 'build-release toolchain mock: PASS\n'

#!/usr/bin/env bash
# build-release.sh 的 Go 工具链固定：不需要真 Go、npm、GNU tar。
# 在临时目录里放一份 build-release.sh 与桩 go / npm / tar，实跑整条构建，验证：
#   - panel 与 pdnd 各自读自己 go.mod 的 go 指令，所有 go build / go get / go mod init 都带
#     GOTOOLCHAIN=go<该版本>，调用方环境里的 GOTOOLCHAIN=local 被覆盖，脚本里没有写死的版本号；
#   - 实际用的版本写进包内 deploy/BUILD-INFO；
#   - go 命令解析出的版本对不上（旧 Go 不认 GOTOOLCHAIN）、产出的二进制记的版本对不上、
#     go 指令不是完整的 x.y.z，三种情况都失败。
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

# 与真实源码树同一布局：<根>/panel/{deploy,migrations,go.mod,Makefile}、<根>/pdnd/go.mod
mkdir -p "$T/tree/panel" "$T/tree/pdnd" "$T/stub"
cp -R "$DEPLOY" "$T/tree/panel/deploy"
cp -R "$DEPLOY/../migrations" "$T/tree/panel/migrations"
printf 'module example.test/panel\n\ngo %s\n' "$PANEL_TC_VERSION" >"$T/tree/panel/go.mod"
printf 'module example.test/pdnd\n\ngo %s\n' "$PDND_TC_VERSION" >"$T/tree/pdnd/go.mod"
# frontend-embed 的替身：写出不含占位标记的入口
cat >"$T/tree/panel/Makefile" <<'MK'
frontend-embed:
	mkdir -p web/admin web/portal
	echo real > web/admin/index.html
	echo real > web/portal/index.html
MK

cat >"$T/stub/go" <<'GO'
#!/usr/bin/env bash
# 桩 go：本机版本 FAKE_GO_LOCAL；GOTOOLCHAIN=go<版本> 时“切换”到该版本，除非 FAKE_GO_IGNORE_TOOLCHAIN=1（模拟不认 GOTOOLCHAIN 的旧 Go）
local_tc="${FAKE_GO_LOCAL:-go1.27.1}"
effective() {
  if [ "${FAKE_GO_IGNORE_TOOLCHAIN:-}" != 1 ] && [[ "${GOTOOLCHAIN:-}" == go1* ]]; then echo "$GOTOOLCHAIN"; else echo "$local_tc"; fi
}
printf '%s|%s|GOTOOLCHAIN=%s\n' "$1" "$PWD" "${GOTOOLCHAIN-<unset>}" >>"$FAKE_GO_LOG"
case "$1" in
  env) [ "${2:-}" = GOVERSION ] && effective ;;
  version)
    if [ -n "${2:-}" ]; then printf '%s: %s\n' "$2" "$(sed -n 's/^gobuild://p' "$2")"; else echo "go version $(effective) linux/amd64"; fi ;;
  build)
    out=""; prev=""
    for a in "$@"; do [ "$prev" = -o ] && out="$a"; prev="$a"; done
    printf 'gobuild:%s\n' "${FAKE_GO_BUILD_TC:-$(effective)}" >"$out" ;;
esac
exit 0
GO
printf '#!/usr/bin/env bash\nexit 0\n' >"$T/stub/npm"
# 桩 tar：只造出 -cf 指定的空归档（真实打包要 GNU tar，由静态测试和 CI 覆盖）
cat >"$T/stub/tar" <<'TAR'
#!/usr/bin/env bash
prev=""
for a in "$@"; do [ "$prev" = -cf ] && : >"$a"; prev="$a"; done
exit 0
TAR
chmod +x "$T/stub/go" "$T/stub/npm" "$T/stub/tar"

run_build() { # 额外环境以 NAME=value 参数传入
  rm -rf "$T/out"; : >"$T/go.log"
  env PATH="$T/stub:$PATH" FAKE_GO_LOG="$T/go.log" PANDORA_VERSION=toolchain-test GOTOOLCHAIN=local "$@" \
    bash "$T/tree/panel/deploy/build-release.sh" "$T/out" >"$T/stdout" 2>"$T/stderr"
}

# --- 1. 正常：各读各的 go.mod，覆盖调用方的 GOTOOLCHAIN=local ---
run_build || { cat "$T/stderr" >&2; fail 'build with pinned toolchains failed'; }
panel_tc="go$PANEL_TC_VERSION"; pdnd_tc="go$PDND_TC_VERSION"
mutating="$(grep -E '^(build|get|mod)\|' "$T/go.log")"
[ -n "$mutating" ] || fail 'the stub go was never asked to build'
bad="$(grep -Ev "GOTOOLCHAIN=(${panel_tc}|${pdnd_tc})\$" <<<"$mutating" || true)"
[ -z "$bad" ] || fail "go invoked without a pinned toolchain:
$bad"
# pdnd 目录里的构建用 pdnd 的版本，其余（panel 命令、goose）用 panel 的版本
grep -E '^build\|.*/tree/pdnd\|' "$T/go.log" | grep -vq "GOTOOLCHAIN=${pdnd_tc}\$" && fail 'a pdnd build did not use the pdnd go.mod toolchain'
grep -qE '^build\|.*/tree/pdnd\|' "$T/go.log" || fail 'pdnd was never built'
grep -E '^build\|' "$T/go.log" | grep -v '/tree/pdnd|' | grep -vq "GOTOOLCHAIN=${panel_tc}\$" && fail 'a panel build did not use the panel go.mod toolchain'
[ "$(grep -c '^build|.*/tree/panel|' "$T/go.log")" -ge 12 ] || fail 'panel binaries were not all built from the panel module directory'
grep -q "^get|.*GOTOOLCHAIN=${panel_tc}\$" "$T/go.log" || fail 'goose build did not use the panel toolchain'
# 记录
info="$T/out/pandora-panel_toolchain-test_linux_amd64/deploy/BUILD-INFO"
[ -f "$info" ] || fail 'BUILD-INFO missing from the release tree'
grep -qx "PANEL_GO_TOOLCHAIN=$panel_tc" "$info" && grep -qx "PDND_GO_TOOLCHAIN=$pdnd_tc" "$info" || fail "BUILD-INFO content: $(cat "$info")"
grep -q 'deploy/BUILD-INFO' "$DEPLOY/build-release.sh" || fail 'BUILD-INFO is not in the archive list'
# 脚本里不写死具体版本号
if grep -nE '(^|[^[:alnum:]_.])go1\.[0-9]+\.[0-9]+' "$DEPLOY/build-release.sh" | grep -v '^[0-9]*:[[:space:]]*#'; then
  fail 'build-release.sh hard-codes a Go version'
fi

# --- 2. go 命令不认 GOTOOLCHAIN：解析出的版本不符，构建前就失败 ---
if run_build FAKE_GO_IGNORE_TOOLCHAIN=1; then fail 'a go command that ignores GOTOOLCHAIN was accepted'; fi
grep -q 'Go toolchain mismatch' "$T/stderr" || fail "mismatch message missing: $(cat "$T/stderr")"
grep -q '^build|' "$T/go.log" && fail 'binaries were built despite the toolchain mismatch'

# --- 3. 二进制里记的版本不符：产出后失败 ---
if run_build FAKE_GO_BUILD_TC=go1.20.0; then fail 'a binary built with the wrong Go was accepted'; fi
grep -q 'was built with' "$T/stderr" || fail "binary mismatch message missing: $(cat "$T/stderr")"

# --- 4. go 指令不是完整 x.y.z：无法固定工具链 ---
printf 'module example.test/panel\n\ngo 1.26\n' >"$T/tree/panel/go.mod"
if run_build; then fail 'a go directive without a patch version was accepted'; fi
grep -q 'full x.y.z' "$T/stderr" || fail "directive message missing: $(cat "$T/stderr")"
printf 'module example.test/panel\n\ngo %s\n' "$PANEL_TC_VERSION" >"$T/tree/panel/go.mod"

# --- 5. 预构建模式：二进制的 Go 版本同样要对 ---
# （readelf 校验另有覆盖；这里只确认读版本这一步接在拷贝之后，源码里能找到）
grep -Fq 'require_binary_toolchain "$PANEL_TOOLCHAIN" "$target/bin/$binary"' "$DEPLOY/build-release.sh" || fail 'panel binaries are not checked after build/copy'
grep -Fq 'require_binary_toolchain "$PANEL_TOOLCHAIN" "$target/bin/goose"' "$DEPLOY/build-release.sh" || fail 'goose is not checked'
grep -Fq 'require_binary_toolchain "$PDND_TOOLCHAIN" "$target/pdnd-dist/$pdnd_name"' "$DEPLOY/build-release.sh" || fail 'pdnd binaries are not checked'

printf 'build-release toolchain mock: PASS\n'

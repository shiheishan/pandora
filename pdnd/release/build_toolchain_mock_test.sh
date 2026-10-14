#!/usr/bin/env bash
# release/build.sh 与 verify.sh 的 Go 工具链固定：不需要真 Go。
# 在临时目录里放一份 release 脚本与桩 go，实跑 build.sh 再 verify.sh，验证：
#   - 所有 go 调用（go list / run / build 全部）带 GOTOOLCHAIN=go<go.mod 的 go 指令>，
#     调用方环境里的 GOTOOLCHAIN=local 被覆盖，脚本里没有写死的版本号；
#   - manifest.json 记下 go_toolchain，verify.sh 对得上 go.mod 才通过；
#   - go 命令解析出的版本不符、产出的二进制记的版本不符、go 指令不是完整 x.y.z 都失败。
set -euo pipefail

REL="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
fail() { printf 'pdnd release toolchain: %s\n' "$*" >&2; exit 1; }
command -v sha256sum >/dev/null 2>&1 || { printf 'pdnd release toolchain: SKIP (no sha256sum)\n'; exit 0; }
T="$(mktemp -d)"
trap 'rm -rf -- "$T"' EXIT
TC_VERSION=1.26.5

mkdir -p "$T/pdnd/release" "$T/pdnd/internal/reality" "$T/stub"
cp "$REL/build.sh" "$REL/verify.sh" "$REL/check_native_stdout.sh" "$T/pdnd/release/"
printf 'module example.test/pdnd\n\ngo %s\n' "$TC_VERSION" >"$T/pdnd/go.mod"

cat >"$T/stub/go" <<'GO'
#!/usr/bin/env bash
local_tc="${FAKE_GO_LOCAL:-go1.27.1}"
effective() {
  if [ "${FAKE_GO_IGNORE_TOOLCHAIN:-}" != 1 ] && [[ "${GOTOOLCHAIN:-}" == go1* ]]; then echo "$GOTOOLCHAIN"; else echo "$local_tc"; fi
}
printf '%s|GOTOOLCHAIN=%s\n' "$1" "${GOTOOLCHAIN-<unset>}" >>"$FAKE_GO_LOG"
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
# verify.sh 会用 file 看架构；桩二进制不是 ELF，按文件名给出对应的描述
cat >"$T/stub/file" <<'FILE'
#!/usr/bin/env bash
case "$2" in
  *arm64*) echo 'ELF 64-bit LSB executable, ARM aarch64' ;;
  *) echo 'ELF 64-bit LSB executable, x86-64' ;;
esac
FILE
chmod +x "$T/stub/go" "$T/stub/file"

run_build() {
  rm -rf "$T/out"; : >"$T/go.log"
  env PATH="$T/stub:$PATH" FAKE_GO_LOG="$T/go.log" GOTOOLCHAIN=local "$@" \
    bash "$T/pdnd/release/build.sh" "$T/out" toolchain-test >"$T/stdout" 2>"$T/stderr"
}
# verify.sh 的解析用了 GNU sed 的 \| 交替；macOS 的 sed 读不了，verify 这几步只在 GNU sed 上跑（CI 即是）
if sed --version >/dev/null 2>&1; then have_gnu_sed=1; else have_gnu_sed=0; fi
run_verify() { env PATH="$T/stub:$PATH" bash "$T/pdnd/release/verify.sh" "$T/out" toolchain-test >"$T/vstdout" 2>"$T/vstderr"; }

# --- 1. 正常 ---
run_build || { cat "$T/stderr" >&2; fail 'build with the pinned toolchain failed'; }
tc="go$TC_VERSION"
# 例外：读二进制里版本的 go version <文件> 故意用 GOTOOLCHAIN=local（只读文件，不触发下载）
bad="$(grep -v "GOTOOLCHAIN=${tc}\$" "$T/go.log" | grep -v '^version|GOTOOLCHAIN=local$' || true)"
[ -z "$bad" ] || fail "go invoked without the pinned toolchain:
$bad"
[ "$(grep -c '^build|' "$T/go.log")" = 4 ] || fail 'expected four go build calls'
grep -q '^list|' "$T/go.log" && grep -q '^run|' "$T/go.log" || fail 'go list / go run were not exercised'
grep -q "\"go_toolchain\": \"$tc\"" "$T/out/manifest.json" || fail "manifest lacks go_toolchain: $(cat "$T/out/manifest.json")"
if [ "$have_gnu_sed" = 1 ]; then run_verify || { cat "$T/vstderr" >&2; fail 'verify.sh rejected a correct release'; }; fi
if grep -nE '(^|[^[:alnum:]_.])go1\.[0-9]+\.[0-9]+' "$REL/build.sh" "$REL/verify.sh" | grep -v ':[[:space:]]*#'; then
  fail 'a release script hard-codes a Go version'
fi

# --- 2. verify.sh：manifest 与 go.mod 对不上 ---
if [ "$have_gnu_sed" = 1 ]; then
printf 'module example.test/pdnd\n\ngo 1.26.6\n' >"$T/pdnd/go.mod"
if run_verify; then fail 'verify.sh accepted a manifest built with a different toolchain than go.mod'; fi
grep -q 'go toolchain mismatch' "$T/vstderr" || fail "verify mismatch message: $(cat "$T/vstderr")"
sed -i.bak 's/"go_toolchain": "[^"]*",//' "$T/out/manifest.json" && rm -f "$T/out/manifest.json.bak"
if run_verify; then fail 'verify.sh accepted a manifest without go_toolchain'; fi
fi
printf 'module example.test/pdnd\n\ngo %s\n' "$TC_VERSION" >"$T/pdnd/go.mod"

# --- 3. go 命令不认 GOTOOLCHAIN ---
if run_build FAKE_GO_IGNORE_TOOLCHAIN=1; then fail 'a go command that ignores GOTOOLCHAIN was accepted'; fi
grep -q 'Go toolchain mismatch' "$T/stderr" || fail "mismatch message missing: $(cat "$T/stderr")"
grep -q '^build|' "$T/go.log" && fail 'binaries were built despite the toolchain mismatch'

# --- 4. 二进制里记的版本不符 ---
if run_build FAKE_GO_BUILD_TC=go1.20.0; then fail 'a binary built with the wrong Go was accepted'; fi
grep -q 'was built with' "$T/stderr" || fail "binary mismatch message missing: $(cat "$T/stderr")"

# --- 5. go 指令不是完整 x.y.z ---
printf 'module example.test/pdnd\n\ngo 1.26\n' >"$T/pdnd/go.mod"
if run_build; then fail 'a go directive without a patch version was accepted'; fi
grep -q 'full x.y.z' "$T/stderr" || fail "directive message missing: $(cat "$T/stderr")"

printf 'pdnd release toolchain mock: PASS\n'

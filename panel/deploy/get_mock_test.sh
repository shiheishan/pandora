#!/usr/bin/env bash
# get.sh 桩测试：PATH 前放假 curl/uname/id/stat；PANDORA_GET_SANDBOX 映射 /opt/pandora-release 与 /dev/tty；
# 现做小型 tar.gz 发布包，包内 deploy/install.sh 为桩。
# 覆盖：1 latest 跳转 2 指定版本与仓库 3 非法版本号 4 架构/平台/root
#       5 校验失败 6 同版本复用 7 路径链 8 环境变量透传 9 标准输入
#       10 旧版本提示 11 参数 12 静态
set -euo pipefail

DEPLOY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
GET="$DEPLOY/get.sh"
for tool in tar gzip sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || { printf 'get mock: SKIP (no %s)\n' "$tool"; exit 0; }
done
T="$(mktemp -d "${TMPDIR:-/tmp}/pandora-get.XXXXXX")"
T="$(cd "$T" && pwd -P)"
trap 'rm -rf -- "$T"' EXIT

fail() {
  if [ -f "$T/out" ]; then
    tail -n 30 "$T/out" >&2
  fi
  printf 'get mock: %s\n' "$*" >&2
  exit 1
}

unset PANDORA_VERSION PANDORA_REPO PANDORA_ASSUME_YES PANDORA_PUBLIC_BASE_URL \
  PANDORA_ADMIN_EMAIL PANDORA_GRPC_PUBLIC_HOST PANDORA_SKIP_NGINX PANDORA_ACME_EMAIL \
  PANDORA_GET_SANDBOX

mkdir -p "$T/bin"

cat >"$T/bin/curl" <<'CURL'
#!/usr/bin/env bash
# 假 curl：URL 是最后一个参数；-o 后面是输出文件；-w 后面是 write-out 格式。每次调用把 URL 记一行
out="" fmt="" prev=""
for a in "$@"; do
  case "$prev" in -o) out="$a" ;; -w) fmt="$a" ;; esac
  prev="$a"
done
url="${!#}"
printf '%s\n' "$url" >>"$GET_TEST_ROOT/curl.log"
case "$url" in
  https://github.com/*/*/releases/latest)
    repo="${url#https://github.com/}"; repo="${repo%/releases/latest}"
    if [ -f "$GET_TEST_ROOT/latest" ]; then loc="https://github.com/$repo/releases/tag/$(cat "$GET_TEST_ROOT/latest")"
    else loc="https://github.com/$repo/releases"; fi
    case "$fmt" in *redirect_url*) printf '%s' "$loc" ;; esac
    exit 0 ;;
  https://github.com/*/*/releases/download/*/*)
    rest="${url#https://github.com/}"
    owner="${rest%%/*}"; rest="${rest#*/}"
    repo="${rest%%/*}"; rest="${rest#*/releases/download/}"
    ver="${rest%%/*}"; file="${rest#*/}"
    src="$GET_TEST_ROOT/srv/$owner/$repo/$ver/$file"
    if [ -f "$src" ]; then cp "$src" "$out"; exit 0; fi
    echo "curl: (22) The requested URL returned error: 404" >&2; exit 22 ;;
esac
echo "curl: (6) Could not resolve host" >&2; exit 6
CURL

cat >"$T/bin/uname" <<'UNAME'
#!/usr/bin/env bash
case "${1:-}" in
  -m) printf '%s\n' "${FAKE_UNAME_M:-x86_64}" ;;
  *) printf '%s\n' "${FAKE_UNAME_S:-Linux}" ;;
esac
UNAME

cat >"$T/bin/id" <<'ID'
#!/usr/bin/env bash
case "${1:-}" in
  -u) printf '%s\n' "${FAKE_UID:-0}" ;;
  *) printf 'uid=%s\n' "${FAKE_UID:-0}" ;;
esac
ID

cat >"$T/bin/stat" <<'STAT'
#!/usr/bin/env bash
path="${!#}"
if [ "$path" = "${FAKE_STAT_BAD:-}" ]; then
  printf '0 777\n'
else
  printf '0 755\n'
fi
STAT

chmod +x "$T/bin/"*

make_release() {
  local repo ver arch name d out
  repo="$1"
  ver="$2"
  arch="$3"
  name="pandora-panel_${ver}_linux_${arch}"
  d="$T/build/$repo/$name"
  rm -rf "$d"
  mkdir -p "$d/bin" "$d/deploy"
  printf 'fake pandora %s %s %s\n' "$repo" "$ver" "$arch" >"$d/bin/pandora"
  cat >"$d/deploy/install.sh" <<'INSTALL'
#!/usr/bin/env bash
# get_mock_test 的桩安装器：记下自己在哪、收到的环境变量和标准输入的第一行
{
  printf 'script=%s\n' "$0"
  for v in PANDORA_ASSUME_YES PANDORA_PUBLIC_BASE_URL PANDORA_ADMIN_EMAIL PANDORA_GRPC_PUBLIC_HOST PANDORA_SKIP_NGINX PANDORA_ACME_EMAIL; do
    printf '%s=%s\n' "$v" "${!v-<unset>}"
  done
  if IFS= read -r line; then printf 'stdin=%s\n' "$line"; else printf 'stdin=<EOF>\n'; fi
} >"${GET_TEST_ROOT:?}/install.log"
INSTALL
  chmod 0755 "$d/deploy/install.sh"
  (cd "$d" && sha256sum bin/pandora deploy/install.sh >SHA256SUMS)
  out="$T/srv/$repo/$ver"
  mkdir -p "$out"
  (cd "$(dirname "$d")" && COPYFILE_DISABLE=1 tar -czf "$out/$name.tar.gz" "$name")
  (cd "$out" && sha256sum "$name.tar.gz" >"$name.tar.gz.sha256")
  printf '%s  SHA256SUMS\n' "$(sha256sum "$d/SHA256SUMS" | awk '{print $1}')" >"$out/$name.tar.gz.manifest.sha256"
}

make_release shiheishan/pandora v1.0.0 amd64
make_release shiheishan/pandora v1.0.0 arm64
make_release shiheishan/pandora v1.1.0 amd64
make_release example-org/pandora-fork v2.0.0 amd64

BASE="$T/sb/opt/pandora-release"
reset_sb() { rm -rf "$T/sb" "$T/stdin"; mkdir -p "$T/sb"; }
with_tty() { mkdir -p "$T/sb/dev"; printf 'from-tty\n' >"$T/sb/dev/tty"; }

run_file() {
  local in=/dev/null
  [ -f "$T/stdin" ] && in="$T/stdin"
  rm -f "$T/install.log"; : >"$T/curl.log"
  env PATH="$T/bin:$PATH" GET_TEST_ROOT="$T" PANDORA_GET_SANDBOX="$T/sb" "$@" bash "$GET" <"$in" >"$T/out" 2>&1
}

run_pipe() {
  rm -f "$T/install.log"; : >"$T/curl.log"
  cat "$GET" | env PATH="$T/bin:$PATH" GET_TEST_ROOT="$T" PANDORA_GET_SANDBOX="$T/sb" "$@" bash >"$T/out" 2>&1
}

installed() { [ -f "$T/install.log" ] && grep -Fxq "$1" "$T/install.log"; }
out_has() { grep -Fq -- "$1" "$T/out"; }
no_requests() { [ ! -s "$T/curl.log" ]; }

# 1. latest 跳转
reset_sb
printf 'v1.1.0\n' >"$T/latest"
run_file || fail '1a: latest 跳转应成功'
grep -q '/opt/pandora-release/v1.1.0/pandora-panel_v1.1.0_linux_amd64/deploy/install.sh$' "$T/install.log" || fail '1a: 安装器路径应为 v1.1.0 amd64'
first_line="$(head -n 1 "$T/curl.log")"
[ "$first_line" = 'https://github.com/shiheishan/pandora/releases/latest' ] || fail "1a: curl.log 第一行不对: $first_line"
grep -Fq 'https://github.com/shiheishan/pandora/releases/download/v1.1.0/pandora-panel_v1.1.0_linux_amd64.tar.gz' "$T/curl.log" || fail '1a: 缺少 v1.1.0 包下载 URL'
grep -Fq 'api.github.com' "$T/curl.log" && fail '1a: 不应请求 api.github.com'

reset_sb
rm -f "$T/latest"
if run_file; then fail '1b: 无正式版应失败'; fi
out_has '没有找到' || fail '1b: 应提示没有找到'
[ "$(wc -l <"$T/curl.log" | tr -d ' ')" -eq 1 ] || fail '1b: curl.log 应只有 1 行'
[ ! -f "$T/install.log" ] || fail '1b: 不应安装'

reset_sb
printf 'v1%%2F..\n' >"$T/latest"
if run_file; then fail '1c: 不合规标签应失败'; fi
out_has '版本号不合规' || fail '1c: 应提示版本号不合规'
grep -Fq 'releases/download' "$T/curl.log" && fail '1c: 不应有 releases/download'
rm -f "$T/latest"

# 2. 指定版本与仓库
reset_sb
run_file PANDORA_VERSION=v1.0.0 || fail '2a: 指定版本应成功'
grep -Fq 'releases/latest' "$T/curl.log" && fail '2a: 不应请求 releases/latest'
grep -q '/opt/pandora-release/v1.0.0/pandora-panel_v1.0.0_linux_amd64/deploy/install.sh$' "$T/install.log" || fail '2a: 安装器路径 v1.0.0 amd64'

reset_sb
run_file PANDORA_REPO=example-org/pandora-fork PANDORA_VERSION=v2.0.0 || fail '2b: fork 仓库应成功'
grep -Fq 'https://github.com/example-org/pandora-fork/releases/download/v2.0.0/' "$T/curl.log" || fail '2b: 应有 fork 下载 URL'

for bad in '../evil' 'a/b/c' 'owner/..'; do
  reset_sb
  if run_file PANDORA_REPO="$bad" PANDORA_VERSION=v1.0.0; then fail "2c: PANDORA_REPO=$bad 应失败"; fi
  out_has 'PANDORA_REPO 不合规' || fail "2c: PANDORA_REPO=$bad 应提示不合规"
  no_requests || fail "2c: PANDORA_REPO=$bad 不应发请求"
done

# 3. 非法版本号
for bad in '1.0.0' 'v1.0.0+build' 'v1/../x' '..' 'v 1'; do
  reset_sb
  if run_file PANDORA_VERSION="$bad"; then fail "3: PANDORA_VERSION=$bad 应失败"; fi
  out_has '版本号不合规' || fail "3: PANDORA_VERSION=$bad 应提示不合规"
  no_requests || fail "3: PANDORA_VERSION=$bad 不应发请求"
  [ ! -e "$T/sb/opt" ] || fail "3: PANDORA_VERSION=$bad 不应建目录"
done

# 4. 架构、平台、root
reset_sb
run_file FAKE_UNAME_M=aarch64 PANDORA_VERSION=v1.0.0 || fail '4a: aarch64 应成功'
grep -q 'pandora-panel_v1.0.0_linux_arm64' "$T/install.log" || fail '4a: 安装器路径应含 arm64'
grep -Fq '_linux_arm64.tar.gz' "$T/curl.log" || fail '4a: 应下载 arm64 包'

reset_sb
if run_file FAKE_UNAME_M=armv7l PANDORA_VERSION=v1.0.0; then fail '4b: armv7l 应失败'; fi
out_has '不支持的 CPU 架构' || fail '4b: 应提示不支持的架构'
no_requests || fail '4b: 不应发请求'

reset_sb
if run_file FAKE_UNAME_S=Darwin PANDORA_VERSION=v1.0.0; then fail '4c: Darwin 应失败'; fi
out_has '只支持 Linux' || fail '4c: 应提示只支持 Linux'
no_requests || fail '4c: 不应发请求'

reset_sb
if run_file FAKE_UID=1000 PANDORA_VERSION=v1.0.0; then fail '4d: 非 root 应失败'; fi
out_has '请用 root 运行' || fail '4d: 应提示 root'
out_has 'sudo' || fail '4d: 应提示 sudo'
no_requests || fail '4d: 不应发请求'

# 5. 校验失败即停
reset_sb
sha_bak="$T/srv/shiheishan/pandora/v1.0.0/pandora-panel_v1.0.0_linux_amd64.tar.gz.sha256"
cp "$sha_bak" "$sha_bak.bak"
printf '%064d  pandora-panel_v1.0.0_linux_amd64.tar.gz\n' 0 >"$sha_bak"
if run_file PANDORA_VERSION=v1.0.0; then fail '5a: 错误 sha256 应失败'; fi
out_has '校验失败' || fail '5a: 应提示校验失败'
[ ! -e "$BASE/v1.0.0" ] || fail '5a: 不应留下 v1.0.0 目录'
[ -z "$(find "$BASE" -mindepth 1 -maxdepth 1 2>/dev/null)" ] || fail '5a: BASE 下不应有条目'
[ ! -f "$T/install.log" ] || fail '5a: 不应安装'

printf '%064d  other.tar.gz\n' 0 >"$sha_bak"
if run_file PANDORA_VERSION=v1.0.0; then fail '5b: 错误校验文件名应失败'; fi
out_has '校验文件格式不对' || fail '5b: 应提示校验文件格式不对'

mv "$sha_bak.bak" "$sha_bak"

# 6. 同版本目录复用与不一致即停
reset_sb
run_file PANDORA_VERSION=v1.0.0 || fail '6a: 首次解包应成功'
[ "$(find "$BASE" -mindepth 1 -maxdepth 1 | wc -l | tr -d ' ')" -eq 1 ] || fail '6a: BASE 下应只有 v1.0.0'
[ -d "$BASE/v1.0.0" ] || fail '6a: 应有 v1.0.0'
for pat in '.dl-' '.tmp-'; do
  [ -z "$(find "$BASE" -maxdepth 1 -name "${pat}*")" ] || fail "6a: 不应残留 ${pat}*"
done

run_file PANDORA_VERSION=v1.0.0 || fail '6b: 复用应成功'
out_has '复用' || fail '6b: 应提示复用'
grep -q '\.tar\.gz$' "$T/curl.log" && fail '6b: 不应再下载 .tar.gz'
grep -Fq '.tar.gz.manifest.sha256' "$T/curl.log" || fail '6b: 应下载 manifest.sha256'
[ -f "$T/install.log" ] || fail '6b: 应执行安装器'

printf 'tampered\n' >>"$BASE/v1.0.0/pandora-panel_v1.0.0_linux_amd64/bin/pandora"
if run_file PANDORA_VERSION=v1.0.0; then fail '6c: 被篡改应失败'; fi
out_has '不一致' || fail '6c: 应提示不一致'
out_has '没有覆盖' || fail '6c: 应提示没有覆盖'
grep -q tampered "$BASE/v1.0.0/pandora-panel_v1.0.0_linux_amd64/bin/pandora" || fail '6c: tampered 应仍在'
[ ! -f "$T/install.log" ] || fail '6c: 不应安装'

reset_sb
run_file PANDORA_VERSION=v1.0.0 || fail '6d: 首次装应成功'
man_bak="$T/srv/shiheishan/pandora/v1.0.0/pandora-panel_v1.0.0_linux_amd64.tar.gz.manifest.sha256"
cp "$man_bak" "$man_bak.bak"
printf '%064d  SHA256SUMS\n' 0 >"$man_bak"
if run_file PANDORA_VERSION=v1.0.0; then fail '6d: 错误 manifest 应失败'; fi
out_has '不一致' || fail '6d: 应提示不一致'
mv "$man_bak.bak" "$man_bak"

# 7. 路径链
reset_sb
mkdir -p "$BASE"
if run_file FAKE_STAT_BAD="$BASE" PANDORA_VERSION=v1.0.0; then fail '7: 路径链不合规应失败'; fi
out_has '不归 root 或可被他人写入' || fail '7: 应提示路径链问题'
no_requests || fail '7: 不应发请求'
[ ! -f "$T/install.log" ] || fail '7: 不应安装'

# 8. 环境变量透传
reset_sb
run_file PANDORA_VERSION=v1.0.0 PANDORA_ASSUME_YES=1 \
  PANDORA_PUBLIC_BASE_URL=https://panel.example.test \
  PANDORA_GRPC_PUBLIC_HOST=grpc.example.test \
  PANDORA_SKIP_NGINX=1 PANDORA_ACME_EMAIL=ops@example.test || fail '8: 透传应成功'
installed 'PANDORA_ASSUME_YES=1' || fail '8: PANDORA_ASSUME_YES'
installed 'PANDORA_PUBLIC_BASE_URL=https://panel.example.test' || fail '8: PANDORA_PUBLIC_BASE_URL'
installed 'PANDORA_GRPC_PUBLIC_HOST=grpc.example.test' || fail '8: PANDORA_GRPC_PUBLIC_HOST'
installed 'PANDORA_SKIP_NGINX=1' || fail '8: PANDORA_SKIP_NGINX'
installed 'PANDORA_ACME_EMAIL=ops@example.test' || fail '8: PANDORA_ACME_EMAIL'

# 9. 标准输入
reset_sb
printf 'fixture-pw-1\n' >"$T/stdin"
run_file PANDORA_VERSION=v1.0.0 PANDORA_ADMIN_EMAIL=admin@example.test PANDORA_ASSUME_YES=1 \
  PANDORA_PUBLIC_BASE_URL=https://panel.example.test || fail '9.1: 文件模式口令透传应成功'
installed 'stdin=fixture-pw-1' || fail '9.1: stdin 应为 fixture-pw-1'
installed 'PANDORA_ADMIN_EMAIL=admin@example.test' || fail '9.1: PANDORA_ADMIN_EMAIL'

reset_sb
with_tty
if run_pipe PANDORA_VERSION=v1.0.0 PANDORA_ADMIN_EMAIL=admin@example.test; then fail '9.2: 管道+ADMIN_EMAIL 应失败'; fi
out_has '先下载 get.sh' || fail '9.2: 应提示先下载 get.sh'
no_requests || fail '9.2: 不应发请求'
[ ! -f "$T/install.log" ] || fail '9.2: 不应安装'

reset_sb
if run_pipe PANDORA_VERSION=v1.0.0; then fail '9.3: 管道无终端应失败'; fi
out_has '没有终端' || fail '9.3: 应提示没有终端'
out_has 'PANDORA_ASSUME_YES=1' || fail '9.3: 应提示 PANDORA_ASSUME_YES=1'
no_requests || fail '9.3: 不应发请求'

reset_sb
with_tty
run_pipe PANDORA_VERSION=v1.0.0 || fail '9.4: 管道有终端应成功'
installed 'stdin=from-tty' || fail '9.4: stdin 应来自 tty'

reset_sb
with_tty
run_pipe PANDORA_VERSION=v1.0.0 PANDORA_ASSUME_YES=1 || fail '9.5: 管道 ASSUME_YES 应成功'
installed 'stdin=<EOF>' || fail '9.5: stdin 应为 EOF'

reset_sb
run_pipe PANDORA_VERSION=v1.0.0 PANDORA_PUBLIC_BASE_URL=https://panel.example.test || fail '9.6: 管道仅 PUBLIC_BASE_URL 应成功'
installed 'stdin=<EOF>' || fail '9.6: stdin 应为 EOF'

# 10. 旧版本提示
reset_sb
mkdir -p "$BASE/v0.0.1" "$BASE/v0.0.2" "$BASE/v0.0.3"
run_file PANDORA_VERSION=v1.0.0 || fail '10a: 有旧版本时应成功'
# 不写成 grep | grep -q：pipefail 下前一个 grep 可能被 SIGPIPE 打断，断言会误判
rm_line="$(grep -F 'rm -rf' "$T/out" || true)"
[ -n "$rm_line" ] || fail '10a: 应提示 rm -rf'
[[ "$rm_line" == *"$BASE/v0.0.1"* ]] || fail "10a: rm -rf 行应含 v0.0.1：$rm_line"
[[ "$rm_line" != *v1.0.0* ]] || fail "10a: rm -rf 行不应含 v1.0.0：$rm_line"
[ -d "$BASE/v0.0.1" ] && [ -d "$BASE/v0.0.2" ] && [ -d "$BASE/v0.0.3" ] || fail '10a: 旧目录应仍在'

reset_sb
mkdir -p "$BASE/v0.0.1" "$BASE/v0.0.2"
run_file PANDORA_VERSION=v1.0.0 || fail '10b: 两个旧版本应成功'
grep -q 'rm -rf' "$T/out" && fail '10b: 不应出现 rm -rf'

# 11. 参数
reset_sb
env PATH="$T/bin:$PATH" GET_TEST_ROOT="$T" PANDORA_GET_SANDBOX="$T/sb" bash "$GET" --help >"$T/out" 2>&1 || fail '11a: --help 应成功'
out_has 'PANDORA_VERSION' || fail '11a: help 应含 PANDORA_VERSION'

if env PATH="$T/bin:$PATH" GET_TEST_ROOT="$T" PANDORA_GET_SANDBOX="$T/sb" bash "$GET" --bogus >"$T/out" 2>&1; then
  fail '11b: --bogus 应失败'
fi
out_has '不认识的参数' || fail '11b: 应提示不认识的参数'

# 12. 静态
[ "$(tail -n 1 "$GET")" = 'main "$@"' ] || fail '12: 末行应为 main "$@"'
[ "$(tail -n 2 "$GET" | head -n 1)" = 'GET_SH_SOURCE="${BASH_SOURCE[0]:-}"' ] || fail '12: 倒数第二行应为 GET_SH_SOURCE'
grep -Fq 'api.github.com' "$GET" && fail '12: get.sh 不应含 api.github.com'
grep -qiE 'aegis|valkey' "$GET" && fail '12: get.sh 不应含 aegis/valkey'

printf 'get mock: PASS\n'

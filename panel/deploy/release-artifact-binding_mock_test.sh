#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
DEPLOY="$ROOT/deploy"
BUILD="$DEPLOY/build-release.sh"

fail() { echo "release-artifact-binding: $*" >&2; exit 1; }
need() { grep -Fq -- "$2" "$1" || fail "${1##*/} is missing: $2"; }

bash -n "$BUILD"
# 1. 生成：本包 pdnd-dist 的两个摘要与版本号，随包进入 tar 的数据文件
for fragment in \
  'PANDORA_NATIVE_ARTIFACT_AMD64_SHA256' \
  'PANDORA_NATIVE_ARTIFACT_ARM64_SHA256' \
  'PANDORA_NATIVE_RELEASE_VERSION' \
  'release-artifact.env' \
  'sha256sum "$target/pdnd-dist/pandora-native-linux-amd64"' \
  'sha256sum "$target/pdnd-dist/pandora-native-linux-arm64"' \
  '"$target_base/deploy/release-artifact.env"'; do
  need "$BUILD" "$fragment"
done
# 版本号与 pandora-native 里 -X main.buildVersion 注入的是同一个值
need "$BUILD" "printf 'PANDORA_NATIVE_RELEASE_VERSION=%s\\n' \"\$VERSION\""
need "$BUILD" '-X main.buildVersion=$VERSION'
# 运行模式归 .env 管：发布物绑定文件若带 AEGIS_ENV，升级会悄悄改掉已装机器的模式
if grep -Eq "printf 'AEGIS_ENV=" "$BUILD"; then fail 'build-release.sh writes AEGIS_ENV into release-artifact.env'; fi
if grep -Eq '^[[:space:]]*AEGIS_ENV=' "$DEPLOY/release-artifact.env.example"; then
  fail 'release-artifact.env.example sets AEGIS_ENV'
fi

# 2. 安装：install.sh 装到 /opt/pandora/deploy/，升级随包覆盖；发布目录缺它时动手之前就拒绝
need "$DEPLOY/install.sh" 'cp -f "$SCRIPT_DIR/release-artifact.env" "$INSTALL_DIR/deploy/"'
need "$DEPLOY/install.sh" '[ -f "$root/deploy/release-artifact.env" ]'

# 3. 加载：接入节点的是 aegis-node，发布物文件排在 .env 之后（发布包的值优先）
unit="$DEPLOY/systemd/aegis-node.service"
env_line="$(grep -n '^EnvironmentFile=/opt/pandora/deploy/\.env$' "$unit" | cut -d: -f1)"
artifact_line="$(grep -n '^EnvironmentFile=-/opt/pandora/deploy/release-artifact\.env$' "$unit" | cut -d: -f1)"
[ -n "$env_line" ] && [ -n "$artifact_line" ] || fail 'aegis-node.service does not load both .env and release-artifact.env'
[ "$artifact_line" -gt "$env_line" ] || fail 'release-artifact.env must be loaded after .env'

# 4. 发布包装出来的就是生产：首装写 AEGIS_ENV=production，并要求对外地址
need "$DEPLOY/install.sh" 'AEGIS_ENV=production'
need "$DEPLOY/install.sh" 'PANDORA_PUBLIC_BASE_URL'

# 5. 版本号：不设 PANDORA_VERSION 时由 git describe 推出，必须过脚本自己的格式检查。
#    仓库里有 archive/client-auth 这种带斜杠的归档标签，裸 describe 会取到它（曾经就这样
#    出不了包），所以只认 v* 标签。这里抠出脚本里的版本行与格式检查，在临时仓库里实跑。
need "$BUILD" "describe --tags --match 'v*' --always --dirty"
version_line="$(grep -E '^VERSION="\$\{PANDORA_VERSION:-' "$BUILD")"
check_block="$(awk '/^if \[\[ ! "\$VERSION" =~/{p=1} p{print} p&&/^fi$/{exit}' "$BUILD")"
[ -n "$version_line" ] && [ -n "$check_block" ] || fail 'cannot locate the VERSION derivation or its format check'
command -v git >/dev/null 2>&1 || fail 'git is required for the version stub'
derive() (
  ROOT="$1"
  unset PANDORA_VERSION
  eval "$version_line"
  eval "$check_block"
  printf '%s' "$VERSION"
)
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
repo="$TMP/repo"
mkdir -p "$repo"
g() { git -C "$repo" -c user.name=stub -c user.email=stub@example.invalid -c commit.gpgsign=false -c tag.gpgsign=false "$@" >/dev/null; }
g init -q
echo one >"$repo/f"; g add f; g commit -q -m one
g tag -a archive/client-auth -m archived
echo two >"$repo/f"; g commit -q -am two
got="$(derive "$repo")" || fail 'version check rejected the describe output next to an archive/* tag'
case "$got" in */*) fail "version picked up a slash tag: $got" ;; esac
[ "$got" = "$(git -C "$repo" rev-parse --short HEAD)" ] || fail "without a v* tag the version must be the short commit, got $got"
g tag -a v1.2.3 -m release
[ "$(derive "$repo")" = v1.2.3 ] || fail "on a v* tag the version must be the tag, got $(derive "$repo")"
echo three >"$repo/f"; g commit -q -am three
echo dirty >"$repo/f"
got="$(derive "$repo")" || fail 'version check rejected a dirty tree after a v* tag'
case "$got" in v1.2.3-1-g*-dirty) ;; *) fail "dirty tree after v1.2.3 gave $got" ;; esac
[ "$(PANDORA_VERSION=custom-1 bash -c "ROOT=$repo; $version_line; printf %s \"\$VERSION\"")" = custom-1 ] ||
  fail 'PANDORA_VERSION must still override git describe'

printf 'release-artifact-binding mock: PASS\n'

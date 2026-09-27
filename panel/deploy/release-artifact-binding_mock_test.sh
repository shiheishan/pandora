#!/usr/bin/env bash
# [INPUT]: 依赖同目录 build-release.sh、install-linux-binaries.sh、install.sh、install-native.sh、systemd/aegis-node.service、release-artifact.env.example
# [OUTPUT]: 发布物绑定全链路的静态契约：生成（两个架构 SHA-256 + 版本，不含 AEGIS_ENV）→ 打包 → 事务安装到固定位置 → aegis-node 以 EnvironmentFile= 加载；首装定为 production
# [POS]: deploy 的桩测试，CI panel-deploy.yml 必跑；只读源码，不构建、不需要 root；链路说明见 docs/RELEASE-ARTIFACT-BINDING.md
# [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
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

# 2. 安装：与二进制同一事务装到 /opt/aegispanel/deploy/，升级随包覆盖
need "$DEPLOY/install-linux-binaries.sh" 'configure-app-role.sql release-artifact.env; do'
need "$DEPLOY/install-linux-binaries.sh" 'stage_file "$RELEASE_DIR/deploy/$data_file" "/opt/aegispanel/deploy/$data_file" 0644'
need "$DEPLOY/install.sh" '"$RELEASE_ROOT/deploy/release-artifact.env"'
need "$DEPLOY/install-native.sh" '"$SCRIPT_DIR/release-artifact.env" "$INSTALL_DIR/deploy/"'

# 3. 加载：接入节点的是 aegis-node，发布物文件排在 .env 之后（发布包的值优先）
unit="$DEPLOY/systemd/aegis-node.service"
env_line="$(grep -n '^EnvironmentFile=/opt/aegispanel/deploy/\.env$' "$unit" | cut -d: -f1)"
artifact_line="$(grep -n '^EnvironmentFile=-/opt/aegispanel/deploy/release-artifact\.env$' "$unit" | cut -d: -f1)"
[ -n "$env_line" ] && [ -n "$artifact_line" ] || fail 'aegis-node.service does not load both .env and release-artifact.env'
[ "$artifact_line" -gt "$env_line" ] || fail 'release-artifact.env must be loaded after .env'

# 4. 发布包装出来的就是生产：首装写 AEGIS_ENV=production，并要求对外地址
need "$DEPLOY/install.sh" '-e "s|^AEGIS_ENV=.*|AEGIS_ENV=production|"'
need "$DEPLOY/install.sh" 'PANDORA_PUBLIC_BASE_URL'
need "$DEPLOY/install-native.sh" 'AEGIS_ENV=production'

printf 'release-artifact-binding mock: PASS\n'

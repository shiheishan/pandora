#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PDND_ROOT="$(cd "$ROOT/../pdnd" && pwd)"
OUT="${1:-$ROOT/dist}"
# 只认 v* 发布标签：仓库里还有 archive/client-auth 这类带斜杠的归档标签，裸 describe
# 会取到它，版本号带斜杠，被下面的格式检查拒掉，不设 PANDORA_VERSION 就出不了包。
# 没有 v* 标签时 --always 回落到短提交号。
VERSION="${PANDORA_VERSION:-$(git -C "$ROOT" describe --tags --match 'v*' --always --dirty 2>/dev/null || echo dev)}"
PREBUILT_ROOT="${PANDORA_PREBUILT_ROOT:-}"
# 迁移工具版本随发布包固定，不跟 @latest 漂移。
GOOSE_VERSION="${PANDORA_GOOSE_VERSION:-v3.26.0}"

command -v grep >/dev/null 2>&1 || { echo "missing grep for release validation" >&2; exit 1; }

if [[ ! "$VERSION" =~ ^[A-Za-z0-9._-]+$ ]] || [ "$VERSION" = . ] || [ "$VERSION" = .. ]; then
  echo "PANDORA_VERSION must match [A-Za-z0-9._-]+ and cannot be . or .." >&2
  exit 1
fi

# Go 工具链固定：发布构建一律用各自 go.mod 的 go 指令所指的那一版（panel 与 pdnd 分别读），
# 不写死版本号、不信构建机上恰好装着哪一版。
#
# 不固定的后果：维护者本机的 Go 比 go 指令新时，go 命令直接用本机版本编，发布物里的标准库
# 版本取决于谁来打包；构建机若被 GOTOOLCHAIN=local 锁在旧的 1.26.x，则会用上没修漏洞的标准库。
# 这里显式设 GOTOOLCHAIN=go<版本>（覆盖环境里已有的值，包括 local），本机版本不同就下载那一版；
# 随后用 go env GOVERSION 核对实际解析出的版本，产出后再用 go version <文件> 读二进制里
# 记的版本，任何一步对不上都停下。实际用的版本写进包内 deploy/BUILD-INFO。
go_toolchain_of() {
  local dir="$1" directive
  directive="$(tr -d '\r' < "$dir/go.mod" | awk '/^go[[:space:]]+[0-9]/ { print $2; exit }')"
  if [[ ! "$directive" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    echo "$dir/go.mod: the go directive must be a full x.y.z version to pin the toolchain, got '${directive:-none}'" >&2
    return 1
  fi
  printf 'go%s' "$directive"
}
PANEL_TOOLCHAIN="$(go_toolchain_of "$ROOT")" || exit 1
PDND_TOOLCHAIN="$(go_toolchain_of "$PDND_ROOT")" || exit 1
# 这个模块目录里的 go 命令（带上固定的 GOTOOLCHAIN）实际解析成哪一版
require_toolchain() {
  local toolchain="$1" dir="$2" resolved
  resolved="$(cd "$dir" && GOTOOLCHAIN="$toolchain" GOFLAGS='' go env GOVERSION 2>/dev/null)" || resolved=""
  [ "$resolved" = "$toolchain" ] || {
    echo "Go toolchain mismatch for $dir: go.mod requires $toolchain but the go command resolved '${resolved:-nothing}'" >&2
    echo "  (GOTOOLCHAIN=$toolchain must be installed or downloadable on this build host)" >&2
    exit 1
  }
}
# 二进制里记的构建版本（go version <文件>；GOTOOLCHAIN=local 只读文件，不触发下载）
require_binary_toolchain() {
  local toolchain="$1" file="$2" out built
  out="$(GOTOOLCHAIN=local GOFLAGS='' go version "$file" 2>/dev/null)" || out=""
  built="${out##*: }"
  [ "$built" = "$toolchain" ] || {
    echo "binary ${file#"$OUT"/} was built with '${built:-unknown}', expected $toolchain" >&2
    exit 1
  }
}

if [ -n "$PREBUILT_ROOT" ]; then
  [ -d "$PREBUILT_ROOT" ] || {
    echo "PANDORA_PREBUILT_ROOT must be an existing directory" >&2
    exit 1
  }
  PREBUILT_ROOT="$(cd "$PREBUILT_ROOT" && pwd -P)"
  [ "$PREBUILT_ROOT" != / ] || {
    echo "PANDORA_PREBUILT_ROOT cannot be /" >&2
    exit 1
  }
  command -v readelf >/dev/null 2>&1 || { echo "missing readelf for prebuilt validation" >&2; exit 1; }
  # 预构建的二进制也要证明是 go.mod 指定的那一版编的，读它要 go 命令
  command -v go >/dev/null 2>&1 || { echo "missing go: needed to check the Go version recorded in prebuilt binaries" >&2; exit 1; }
else
  command -v go >/dev/null 2>&1 || { echo "missing Go (the go command; the pinned toolchain $PANEL_TOOLCHAIN / $PDND_TOOLCHAIN is selected via GOTOOLCHAIN)" >&2; exit 1; }
  require_toolchain "$PANEL_TOOLCHAIN" "$ROOT"
  require_toolchain "$PDND_TOOLCHAIN" "$PDND_ROOT"
fi
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd -P)"
[ "$OUT" != / ] || { echo "release output directory cannot be /" >&2; exit 1; }
if [ -n "$PREBUILT_ROOT" ]; then
  case "$PREBUILT_ROOT/" in
    "$OUT/"*) echo "prebuilt root must not be inside release output" >&2; exit 1 ;;
  esac
  case "$OUT/" in
    "$PREBUILT_ROOT/"*) echo "release output must not be inside prebuilt root" >&2; exit 1 ;;
  esac
fi

# 面板前端经 go:embed 编进 aegis-admin / aegis-public，必须先于 Go 构建同步进 web/{admin,portal}。
# 仓库里只有占位入口；没有 npm 就失败，不把占位页静默打进发布物。
# 预构建模式下二进制来自别处，嵌入由产出它们的那台构建机负责。
if [ -z "$PREBUILT_ROOT" ]; then
  command -v npm >/dev/null 2>&1 || {
    echo "missing npm (Node 22.12+): release binaries embed the panel frontend and must not ship its placeholder" >&2
    exit 1
  }
  # 后台登录页与侧栏显示的版本号由 vite 构建时读 PANDORA_RELEASE 注入，与发布物版本同源
  PANDORA_RELEASE="$VERSION" make -C "$ROOT" frontend-embed
  for app in admin portal; do
    if grep -q 'name="pandora-placeholder"' "$ROOT/web/$app/index.html"; then
      echo "web/$app still holds the placeholder entry after frontend-embed" >&2
      exit 1
    fi
  done
fi

binaries=(aegis-public aegis-admin aegis-node aegis-payctl aegis-adminctl aegis-backup-webdav)
for arch in amd64 arm64; do
  expected_machine=""
  case "$arch" in
    amd64) expected_machine='Advanced Micro Devices X86-64' ;;
    arm64) expected_machine='AArch64' ;;
  esac
  target="$OUT/pandora-panel_${VERSION}_linux_${arch}"
  rm -rf "$target"
  mkdir -p "$target/bin" "$target/pdnd-dist" "$target/migrations" "$target/deploy/systemd"
  if [ -n "$PREBUILT_ROOT" ]; then
    source_arch_dir="$PREBUILT_ROOT/$arch"
    [ -d "$source_arch_dir" ] && [ ! -L "$source_arch_dir" ] || {
      echo "missing or unsafe prebuilt directory: $arch" >&2
      exit 1
    }
    source_arch_dir="$(cd "$source_arch_dir" && pwd -P)"
    [ "$source_arch_dir" = "$PREBUILT_ROOT/$arch" ] || {
      echo "prebuilt architecture directory escapes its root: $arch" >&2
      exit 1
    }
  fi
  for binary in "${binaries[@]}"; do
    if [ -n "$PREBUILT_ROOT" ]; then
      source_binary="$source_arch_dir/$binary"
      [ -f "$source_binary" ] && [ ! -L "$source_binary" ] || {
        echo "missing or unsafe prebuilt $arch/$binary" >&2
        exit 1
      }
      source_binary_real="$(realpath "$source_binary")"
      [ "$source_binary_real" = "$source_arch_dir/$binary" ] || {
        echo "prebuilt binary escapes its architecture directory: $arch/$binary" >&2
        exit 1
      }
      echo "packaging prebuilt linux/$arch $binary"
      cp "$source_binary" "$target/bin/$binary"
      [ -s "$target/bin/$binary" ] || {
        echo "empty prebuilt linux/$arch $binary" >&2
        exit 1
      }
      elf_header="$(LC_ALL=C readelf -h "$target/bin/$binary" 2>/dev/null)"
      machine="$(printf '%s\n' "$elf_header" | awk -F: '/Machine:/{sub(/^[[:space:]]+/,"",$2); print $2; exit}')"
      [ "$machine" = "$expected_machine" ] || {
        echo "prebuilt binary architecture mismatch: linux/$arch $binary" >&2
        exit 1
      }
      elf_type="$(printf '%s\n' "$elf_header" | awk -F: '/Type:/{sub(/^[[:space:]]+/,"",$2); print $2; exit}')"
      case "$elf_type" in
        EXEC*) ;;
        *) echo "prebuilt binary is not an ELF executable: linux/$arch $binary" >&2; exit 1 ;;
      esac
    else
      echo "building linux/$arch $binary"
      (cd "$ROOT" && GOTOOLCHAIN="$PANEL_TOOLCHAIN" GOFLAGS='' CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
        go build -buildvcs=false -trimpath -ldflags="-s -w -buildid=" \
        -o "$target/bin/$binary" "./cmd/$binary")
    fi
    require_binary_toolchain "$PANEL_TOOLCHAIN" "$target/bin/$binary"
  done

  # 迁移工具跟着发布包走。
  #
  # 以前它是「运维机上预装的东西」（migrate.sh 里写死 /root/go/bin/goose），
  # 于是发布包并不自足：干净机器上一键安装会卡在「找不到 goose」，而要求
  # 客户先装一套 Go 工具链只为拿一个二进制，不合理。
  #
  # 版本写死，不用 @latest：同一个发布包在任何机器上都该用同一版 goose
  # 去跑同一批迁移。
  if [ -n "$PREBUILT_ROOT" ]; then
    goose_source="$source_arch_dir/goose"
    [ -f "$goose_source" ] && [ ! -L "$goose_source" ] || {
      echo "missing or unsafe prebuilt $arch/goose" >&2
      exit 1
    }
    cp "$goose_source" "$target/bin/goose"
  else
    echo "building linux/$arch goose ($GOOSE_VERSION)"
    goose_work="$(mktemp -d)"
    (
      cd "$goose_work"
      GOTOOLCHAIN="$PANEL_TOOLCHAIN" go mod init pandora-goose-build >/dev/null 2>&1
      GOTOOLCHAIN="$PANEL_TOOLCHAIN" GOFLAGS='' go get "github.com/pressly/goose/v3/cmd/goose@$GOOSE_VERSION" >/dev/null 2>&1
      GOTOOLCHAIN="$PANEL_TOOLCHAIN" GOFLAGS='' CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
        go build -buildvcs=false -trimpath -ldflags="-s -w -buildid=" \
        -o "$target/bin/goose" github.com/pressly/goose/v3/cmd/goose
    ) || { echo "failed to build goose for linux/$arch" >&2; rm -rf "$goose_work"; exit 1; }
    rm -rf "$goose_work"
  fi
  [ -s "$target/bin/goose" ] || { echo "empty goose binary: linux/$arch" >&2; exit 1; }
  require_binary_toolchain "$PANEL_TOOLCHAIN" "$target/bin/goose"
  chmod 0755 "$target/bin/goose"
  for node_arch in amd64 arm64; do
    pdnd_name="pandora-native-linux-$node_arch"
    case "$node_arch" in
      amd64) node_expected_machine='Advanced Micro Devices X86-64' ;;
      arm64) node_expected_machine='AArch64' ;;
    esac
    if [ -n "$PREBUILT_ROOT" ]; then
    node_source_arch_dir="$PREBUILT_ROOT/$node_arch"
    [ -d "$node_source_arch_dir" ] && [ ! -L "$node_source_arch_dir" ] || {
      echo "missing or unsafe prebuilt node directory: $node_arch" >&2
      exit 1
    }
    node_source_arch_dir="$(cd "$node_source_arch_dir" && pwd -P)"
    pdnd_source="$node_source_arch_dir/$pdnd_name"
    [ -f "$pdnd_source" ] && [ ! -L "$pdnd_source" ] || {
      echo "missing or unsafe prebuilt $arch/$pdnd_name" >&2
      exit 1
    }
    pdnd_source_real="$(realpath "$pdnd_source")"
    [ "$pdnd_source_real" = "$node_source_arch_dir/$pdnd_name" ] || {
      echo "prebuilt node binary escapes its architecture directory: $node_arch/$pdnd_name" >&2
      exit 1
    }
    cp "$pdnd_source" "$target/pdnd-dist/$pdnd_name"
    elf_header="$(LC_ALL=C readelf -h "$target/pdnd-dist/$pdnd_name" 2>/dev/null)"
    machine="$(printf '%s\n' "$elf_header" | awk -F: '/Machine:/{sub(/^[[:space:]]+/,"",$2); print $2; exit}')"
    [ "$machine" = "$node_expected_machine" ] || {
      echo "prebuilt node binary architecture mismatch: linux/$node_arch $pdnd_name" >&2
      exit 1
    }
    else
    echo "building linux/$node_arch $pdnd_name"
    (cd "$PDND_ROOT" && GOTOOLCHAIN="$PDND_TOOLCHAIN" GOFLAGS='' CGO_ENABLED=0 GOOS=linux GOARCH="$node_arch" \
      go build -buildvcs=false -mod=readonly -trimpath -ldflags="-s -w -buildid= -X main.buildVersion=$VERSION" \
      -o "$target/pdnd-dist/$pdnd_name" .)
    fi
    [ -s "$target/pdnd-dist/$pdnd_name" ] || {
    echo "empty node binary: linux/$node_arch $pdnd_name" >&2
    exit 1
    }
    require_binary_toolchain "$PDND_TOOLCHAIN" "$target/pdnd-dist/$pdnd_name"
  done
  # Cross-building from Windows does not preserve a Unix executable bit.
  # Normalize it before archiving so the same release package passes the
  # Linux-side manifest and startup preflight regardless of build host.
  chmod 0755 "$target"/bin/* "$target"/pdnd-dist/*

  # Database migrations and the release controller are one immutable release
  # unit.  Shipping only binaries makes it possible to run new code against an
  # old schema (or vice versa), which is not a supported rollout mode.
  cp "$ROOT"/migrations/*.sql "$target/migrations/"
  for script in install.sh public-base-url.sh install-lib.sh admin-url.sh platform.sh check-migrations.sh migrate.sh release-stop-the-world.sh backup-postgres.sh verify-backup.sh restore-postgres.sh bootstrap.sh psql.sh pg-revive.sh healthcheck.sh edge-tls.sh render-nginx.sh update-cloudflare-realip.sh; do
    cp "$ROOT/deploy/$script" "$target/deploy/$script"
  done
  cp "$ROOT/deploy/nginx-aegis.conf" "$target/deploy/nginx-aegis.conf" 2>/dev/null || true
  cp "$ROOT/deploy/backup-webdav.example.json" "$target/deploy/backup-webdav.example.json"
  # 迁移失败与回滚的操作手册：出事时人在服务器上，手册要跟着发布包走（安装器再拷到 deploy/ 下）
  cp "$ROOT/deploy/MIGRATION-RUNBOOK.md" "$target/deploy/MIGRATION-RUNBOOK.md"
  # 应用角色收窄：少了它，没法把 aegis_app 收敛成 NOSUPERUSER + NOBYPASSRLS 的运行时角色。
  cp "$ROOT/deploy/configure-app-role.sql" "$target/deploy/configure-app-role.sql"
  # PostgreSQL 参数（低内存调参、关 JIT 等），install.sh 装到集群的 conf.d/pandora.conf
  cp "$ROOT/deploy/postgresql-pandora.conf" "$target/deploy/postgresql-pandora.conf"
  # 网关日志轮转，install.sh 装到 /etc/logrotate.d/aegis
  cp "$ROOT/deploy/logrotate-aegis" "$target/deploy/logrotate-aegis"
  # 节点端发布物绑定：本包里两个架构 pandora-native 的 SHA-256 与版本号（即
  # -X main.buildVersion 注入的 $VERSION）。install.sh 把它装到
  # /opt/pandora/deploy/release-artifact.env，aegis-node.service 以 EnvironmentFile=
  # 加载，每次升级随包覆盖。运行模式 AEGIS_ENV 不写在这里：它归 .env 管，
  # 否则升级会把已装机器的 development 悄悄翻成 production。
  amd64_digest="$(sha256sum "$target/pdnd-dist/pandora-native-linux-amd64" | awk '{print $1}')"
  arm64_digest="$(sha256sum "$target/pdnd-dist/pandora-native-linux-arm64" | awk '{print $1}')"
  {
    printf 'PANDORA_NATIVE_RELEASE_VERSION=%s\n' "$VERSION"
    printf 'PANDORA_NATIVE_ARTIFACT_AMD64_SHA256=%s\n' "$amd64_digest"
    printf 'PANDORA_NATIVE_ARTIFACT_ARM64_SHA256=%s\n' "$arm64_digest"
  } > "$target/deploy/release-artifact.env"
  chmod 0644 "$target/deploy/release-artifact.env"
  # 本包实际用的 Go 工具链（上面逐个二进制核对过）。只是记录，安装器与服务都不读它；
  # 进 SHA256SUMS 与归档，事后能从包里查出发布物是哪一版 Go 编的。
  {
    printf 'PANDORA_RELEASE_VERSION=%s\n' "$VERSION"
    printf 'PANEL_GO_TOOLCHAIN=%s\n' "$PANEL_TOOLCHAIN"
    printf 'PDND_GO_TOOLCHAIN=%s\n' "$PDND_TOOLCHAIN"
  } > "$target/deploy/BUILD-INFO"
  chmod 0644 "$target/deploy/BUILD-INFO"
  for unit in aegis-public.service aegis-admin.service aegis-node.service; do
    cp "$ROOT/deploy/systemd/$unit" "$target/deploy/systemd/$unit"
  done
  cp "$ROOT/deploy/systemd/aegis-backup.service" "$target/deploy/systemd/aegis-backup.service"
  cp "$ROOT/deploy/systemd/aegis-backup.timer" "$target/deploy/systemd/aegis-backup.timer"
  # HTTPS 证书续期（edge-tls.sh renew），install.sh 装到 /etc/systemd/system
  cp "$ROOT/deploy/systemd/aegis-tls-renew.service" "$target/deploy/systemd/aegis-tls-renew.service"
  cp "$ROOT/deploy/systemd/aegis-tls-renew.timer" "$target/deploy/systemd/aegis-tls-renew.timer"
  # 健康巡检（healthcheck.sh 每 10 分钟一次），install.sh 装好后启用 timer
  cp "$ROOT/deploy/systemd/aegis-health.service" "$target/deploy/systemd/aegis-health.service"
  cp "$ROOT/deploy/systemd/aegis-health.timer" "$target/deploy/systemd/aegis-health.timer"
  chmod 0755 "$target"/deploy/*.sh
  if find "$target/bin" "$target/pdnd-dist" "$target/migrations" "$target/deploy" ! -type d ! -type f -print -quit | grep -q .; then
    echo "release tree contains a non-regular object" >&2
    exit 1
  fi

  # 包里不许有 CRLF。
  #
  # 在 Windows 上 clone 再构建，会把 CRLF 带进包里，装到一半炸在
  # `$'\r': command not found`——而且报错指向 /opt 下生成的文件，
  # 完全看不出问题出在几步之前的 checkout。已经这么栽过两次：
  # 一次是 run-pg18-gates.sh，一次是 .env.example。
  #
  # .gitattributes 是根治（默认 eol=lf），这里是第二道：构建脚本不该
  # 假设源码树一定干净。在这里拦住，报错就落在构建机上，而不是等
  # 装到一半才在目标机上炸。
  crlf_files="$(
    find "$target/deploy" "$target/migrations" -type f -print0 | xargs -0 grep -rlU $'\r' 2>/dev/null || true
  )"
  if [ -n "$crlf_files" ]; then
    echo "release tree contains CRLF line endings:" >&2
    printf '  %s
' $crlf_files >&2
    echo "fix .gitattributes or re-checkout with eol=lf; a CRLF script dies on the target host" >&2
    exit 1
  fi
  (
    cd "$target"
    find bin pdnd-dist migrations deploy -type f -print0 \
      | sort -z \
      | xargs -0 sha256sum > SHA256SUMS
  )
  manifest_digest="$(sha256sum "$target/SHA256SUMS" | awk '{print $1}')"
  archive="$target.tar.gz"
  archive_tar="$target.tar"
  rm -f "$archive" "$archive_tar"
  target_base="$(basename "$target")"
  release_scripts=()
  for script in install.sh public-base-url.sh install-lib.sh admin-url.sh platform.sh check-migrations.sh migrate.sh release-stop-the-world.sh backup-postgres.sh verify-backup.sh restore-postgres.sh bootstrap.sh psql.sh pg-revive.sh healthcheck.sh edge-tls.sh render-nginx.sh update-cloudflare-realip.sh; do
    release_scripts+=("$target_base/deploy/$script")
  done
  release_data=(
    "$target_base/deploy/release-artifact.env"
    "$target_base/deploy/BUILD-INFO"
    "$target_base/deploy/nginx-aegis.conf"
    "$target_base/deploy/backup-webdav.example.json"
    "$target_base/deploy/MIGRATION-RUNBOOK.md"
    "$target_base/deploy/configure-app-role.sql"
    "$target_base/deploy/postgresql-pandora.conf"
    "$target_base/deploy/logrotate-aegis"
    "$target_base/deploy/systemd/aegis-public.service"
    "$target_base/deploy/systemd/aegis-admin.service"
    "$target_base/deploy/systemd/aegis-node.service"
    "$target_base/deploy/systemd/aegis-backup.service"
    "$target_base/deploy/systemd/aegis-backup.timer"
    "$target_base/deploy/systemd/aegis-tls-renew.service"
    "$target_base/deploy/systemd/aegis-tls-renew.timer"
    "$target_base/deploy/systemd/aegis-health.service"
    "$target_base/deploy/systemd/aegis-health.timer"
  )
  migration_files=()
  for migration in "$target"/migrations/*.sql; do
    migration_files+=("$target_base/migrations/$(basename "$migration")")
  done
  # NTFS does not retain Unix ownership or chmod metadata.  Create each class
  # in a separate tar pass, pin every member to numeric root ownership, and do
  # not recursively add deploy/ before its executable and data files have
  # received their distinct modes.  The Linux installer intentionally rejects
  # release trees that are not root-owned.
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner --no-recursion \
    --mode=0755 -cf "$archive_tar" "$target_base"
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner \
    --mode=0755 -rf "$archive_tar" "$target_base/bin"
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner \
    --mode=0755 -rf "$archive_tar" "$target_base/pdnd-dist"
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner --no-recursion \
    --mode=0755 -rf "$archive_tar" "$target_base/deploy" "$target_base/deploy/systemd"
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner \
    --mode=0755 -rf "$archive_tar" "${release_scripts[@]}"
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner \
    --mode=0644 -rf "$archive_tar" "${release_data[@]}"
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner --no-recursion \
    --mode=0755 -rf "$archive_tar" "$target_base/migrations"
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner \
    --mode=0644 -rf "$archive_tar" "${migration_files[@]}" "$target_base/SHA256SUMS"
  gzip -n "$archive_tar"
  # Keep the sidecar portable: consumers should be able to verify it after
  # moving the pair to a staging host or another directory.
  (
    cd "$OUT"
    sha256sum "$(basename "$archive")" > "$(basename "$archive").sha256"
    printf '%s  SHA256SUMS\n' "$manifest_digest" \
      > "$(basename "$archive").manifest.sha256"
  )
done

echo "release artifacts written to $OUT"

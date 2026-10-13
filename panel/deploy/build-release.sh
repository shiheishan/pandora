#!/usr/bin/env bash
# 发布包构建：产出 dist/pandora-panel_<版本>_linux_{amd64,arm64}/
#   bin/pandora、pdnd-dist 下两架构节点端、geoip/ip2region_v4.xdb、deploy/ 与 SHA256SUMS，
#   再打成 .tar.gz 并写 .sha256 / .manifest.sha256 旁路校验文件。
#
# Go 工具链：panel 与 pdnd 各读自己 go.mod 的 go 指令（须为完整 x.y.z），
# 构建时 GOTOOLCHAIN=go<版本> 覆盖环境（含 local），构建后核对 go env 与 go version <二进制>。
# 实际版本写入 deploy/BUILD-INFO。
#
# IPv4 离线库：默认从 GEOIP_V4_URL 下载，SHA-256 须等于 GEOIP_V4_SHA256；
# 缓存于输出目录 .cache/，两架构共用。可用 PANDORA_GEOIP_V4_URL 与 PANDORA_GEOIP_V4_SHA256
# 同时覆盖（只给一个则失败）。提交号与 panel/go.mod 里 ip2region binding 伪版本同一提交，换版本时 URL 与摘要一起改。
#
# 依赖：go、npm（Node 22.12+）、GNU tar、curl、sha256sum、make、grep。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PDND_ROOT="$(cd "$ROOT/../pdnd" && pwd)"
OUT="${1:-$ROOT/dist}"
# 只认 v* 发布标签：仓库里还有 archive/client-auth 这类带斜杠的归档标签，裸 describe
# 会取到它，版本号带斜杠，被下面的格式检查拒掉，不设 PANDORA_VERSION 就出不了包。
# 没有 v* 标签时 --always 回落到短提交号。
VERSION="${PANDORA_VERSION:-$(git -C "$ROOT" describe --tags --match 'v*' --always --dirty 2>/dev/null || echo dev)}"

# 与 panel/go.mod 里 github.com/lionsoul2014/ip2region/binding/golang 伪版本同一提交（xdb 与读取库配套），约 11 MB。
GEOIP_V4_URL_DEFAULT='https://raw.githubusercontent.com/lionsoul2014/ip2region/5a401aaebbe26b80cf0702bcc1934f1a556c9cf7/data/ip2region_v4.xdb'
GEOIP_V4_SHA256_DEFAULT='7ae88a996f8b464b68813609fe725f687bf484f5b1480c39f0f3eeb1c6c597be'

# 发布包 deploy/：拷贝与归档共用同一组名单（勿在此处再写第二份列表）。
release_scripts=(
  install.sh install-lib.sh public-base-url.sh platform.sh admin-url.sh bootstrap.sh
  render-nginx.sh backup-postgres.sh verify-backup.sh restore-postgres.sh healthcheck.sh
  edge-tls.sh update-cloudflare-realip.sh pg-revive.sh
)
release_data=(
  configure-app-role.sql nginx-pandora.conf postgresql-pandora.conf logrotate-pandora
  backup-webdav.example.json RUNBOOK.md config.example.yaml secrets.env.example
)
release_units=(
  pandora-serve.service pandora-worker.service pandora-backup.service pandora-backup.timer
  pandora-health.service pandora-health.timer pandora-tls-renew.service pandora-tls-renew.timer
)

command -v grep >/dev/null 2>&1 || { echo "missing grep for release validation" >&2; exit 1; }

if [[ ! "$VERSION" =~ ^[A-Za-z0-9._-]+$ ]] || [ "$VERSION" = . ] || [ "$VERSION" = .. ]; then
  echo "PANDORA_VERSION must match [A-Za-z0-9._-]+ and cannot be . or .." >&2
  exit 1
fi

# Go 工具链固定：发布构建一律用各自 go.mod 的 go 指令所指的那一版（panel 与 pdnd 分别读），
# 不写死版本号、不信构建机上恰好装着哪一版。
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

require_toolchain() {
  local toolchain="$1" dir="$2" resolved
  resolved="$(cd "$dir" && GOTOOLCHAIN="$toolchain" GOFLAGS='' go env GOVERSION 2>/dev/null)" || resolved=""
  [ "$resolved" = "$toolchain" ] || {
    echo "Go toolchain mismatch for $dir: go.mod requires $toolchain but the go command resolved '${resolved:-nothing}'" >&2
    echo "  (GOTOOLCHAIN=$toolchain must be installed or downloadable on this build host)" >&2
    exit 1
  }
}

require_binary_toolchain() {
  local toolchain="$1" file="$2" out built
  out="$(GOTOOLCHAIN=local GOFLAGS='' go version "$file" 2>/dev/null)" || out=""
  built="${out##*: }"
  [ "$built" = "$toolchain" ] || {
    echo "binary ${file#"$OUT"/} was built with '${built:-unknown}', expected $toolchain" >&2
    exit 1
  }
}

command -v go >/dev/null 2>&1 || {
  echo "missing Go (the go command; the pinned toolchain $PANEL_TOOLCHAIN / $PDND_TOOLCHAIN is selected via GOTOOLCHAIN)" >&2
  exit 1
}
require_toolchain "$PANEL_TOOLCHAIN" "$ROOT"
require_toolchain "$PDND_TOOLCHAIN" "$PDND_ROOT"

resolve_geoip_source() {
  local url_set=0 sha_set=0
  GEOIP_V4_URL="$GEOIP_V4_URL_DEFAULT"
  GEOIP_V4_SHA256="$GEOIP_V4_SHA256_DEFAULT"
  if [ -n "${PANDORA_GEOIP_V4_URL:-}" ]; then url_set=1; GEOIP_V4_URL="$PANDORA_GEOIP_V4_URL"; fi
  if [ -n "${PANDORA_GEOIP_V4_SHA256:-}" ]; then sha_set=1; GEOIP_V4_SHA256="$PANDORA_GEOIP_V4_SHA256"; fi
  if [ "$url_set" -ne "$sha_set" ]; then
    echo "PANDORA_GEOIP_V4_URL and PANDORA_GEOIP_V4_SHA256 must both be set to override the default ip2region bundle" >&2
    exit 1
  fi
  if [[ ! "$GEOIP_V4_SHA256" =~ ^[0-9a-fA-F]{64}$ ]]; then
    echo "GEOIP_V4_SHA256 must be 64 hex digits" >&2
    exit 1
  fi
  GEOIP_V4_SHA256="$(printf '%s' "$GEOIP_V4_SHA256" | tr '[:upper:]' '[:lower:]')"
}

ensure_geoip_cache() {
  local cache_dir cache_file tmp digest
  cache_dir="$OUT/.cache"
  cache_file="$cache_dir/ip2region_v4-${GEOIP_V4_SHA256}.xdb"
  if [ -f "$cache_file" ]; then
    digest="$(sha256sum "$cache_file" | awk '{print $1}')"
    if [ "$digest" = "$GEOIP_V4_SHA256" ]; then
      GEOIP_CACHE_FILE="$cache_file"
      return 0
    fi
  fi
  command -v curl >/dev/null 2>&1 || { echo "missing curl to fetch ip2region_v4.xdb" >&2; exit 1; }
  mkdir -p "$cache_dir"
  tmp="$(mktemp "$cache_dir/.ip2region_v4.XXXXXX")"
  curl -fsSL --retry 3 -o "$tmp" "$GEOIP_V4_URL"
  digest="$(sha256sum "$tmp" | awk '{print $1}')"
  if [ "$digest" != "$GEOIP_V4_SHA256" ]; then
    rm -f "$tmp"
    echo "ip2region_v4.xdb SHA-256 mismatch: expected $GEOIP_V4_SHA256 got $digest" >&2
    exit 1
  fi
  mv "$tmp" "$cache_file"
  GEOIP_CACHE_FILE="$cache_file"
}

mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd -P)"
[ "$OUT" != / ] || { echo "release output directory cannot be /" >&2; exit 1; }

resolve_geoip_source
ensure_geoip_cache

# 面板前端经 go:embed 编进 pandora，必须先于 Go 构建同步进 web/{admin,portal}。
command -v npm >/dev/null 2>&1 || {
  echo "missing npm (Node 22.12+): release binaries embed the panel frontend and must not ship its placeholder" >&2
  exit 1
}
PANDORA_RELEASE="$VERSION" make -C "$ROOT" frontend-embed
for app in admin portal; do
  if grep -q 'name="pandora-placeholder"' "$ROOT/web/$app/index.html"; then
    echo "web/$app still holds the placeholder entry after frontend-embed" >&2
    exit 1
  fi
done

for arch in amd64 arm64; do
  target="$OUT/pandora-panel_${VERSION}_linux_${arch}"
  rm -rf "$target"
  mkdir -p "$target/bin" "$target/pdnd-dist" "$target/geoip" "$target/deploy/systemd"

  echo "building linux/$arch pandora"
  (cd "$ROOT" && GOTOOLCHAIN="$PANEL_TOOLCHAIN" GOFLAGS='' CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build -buildvcs=false -trimpath -ldflags="-s -w -buildid=" \
    -o "$target/bin/pandora" ./cmd/pandora)
  require_binary_toolchain "$PANEL_TOOLCHAIN" "$target/bin/pandora"

  for node_arch in amd64 arm64; do
    pdnd_name="pandora-native-linux-$node_arch"
    echo "building linux/$node_arch $pdnd_name"
    (cd "$PDND_ROOT" && GOTOOLCHAIN="$PDND_TOOLCHAIN" GOFLAGS='' CGO_ENABLED=0 GOOS=linux GOARCH="$node_arch" \
      go build -buildvcs=false -mod=readonly -trimpath -ldflags="-s -w -buildid= -X main.buildVersion=$VERSION" \
      -o "$target/pdnd-dist/$pdnd_name" .)
    [ -s "$target/pdnd-dist/$pdnd_name" ] || {
      echo "empty node binary: linux/$node_arch $pdnd_name" >&2
      exit 1
    }
    require_binary_toolchain "$PDND_TOOLCHAIN" "$target/pdnd-dist/$pdnd_name"
  done

  cp "$GEOIP_CACHE_FILE" "$target/geoip/ip2region_v4.xdb"
  chmod 0644 "$target/geoip/ip2region_v4.xdb"

  for script in "${release_scripts[@]}"; do
    cp "$ROOT/deploy/$script" "$target/deploy/$script"
  done
  for data in "${release_data[@]}"; do
    cp "$ROOT/deploy/$data" "$target/deploy/$data"
  done
  for unit in "${release_units[@]}"; do
    cp "$ROOT/deploy/systemd/$unit" "$target/deploy/systemd/$unit"
  done

  pandora_digest="$(sha256sum "$target/bin/pandora" | awk '{print $1}')"
  amd64_digest="$(sha256sum "$target/pdnd-dist/pandora-native-linux-amd64" | awk '{print $1}')"
  arm64_digest="$(sha256sum "$target/pdnd-dist/pandora-native-linux-arm64" | awk '{print $1}')"
  geoip_digest="$(sha256sum "$target/geoip/ip2region_v4.xdb" | awk '{print $1}')"
  {
    printf 'PANDORA_RELEASE_VERSION=%s\n' "$VERSION"
    printf 'PANDORA_BINARY_SHA256=%s\n' "$pandora_digest"
    printf 'PANDORA_NATIVE_ARTIFACT_AMD64_SHA256=%s\n' "$amd64_digest"
    printf 'PANDORA_NATIVE_ARTIFACT_ARM64_SHA256=%s\n' "$arm64_digest"
    printf 'PANDORA_GEOIP_V4_SHA256=%s\n' "$geoip_digest"
  } > "$target/deploy/release-artifact.env"
  chmod 0644 "$target/deploy/release-artifact.env"

  {
    printf 'PANDORA_RELEASE_VERSION=%s\n' "$VERSION"
    printf 'PANEL_GO_TOOLCHAIN=%s\n' "$PANEL_TOOLCHAIN"
    printf 'PDND_GO_TOOLCHAIN=%s\n' "$PDND_TOOLCHAIN"
    printf 'GEOIP_V4_URL=%s\n' "$GEOIP_V4_URL"
  } > "$target/deploy/BUILD-INFO"
  chmod 0644 "$target/deploy/BUILD-INFO"

  chmod 0755 "$target/bin/pandora" "$target"/pdnd-dist/*
  chmod 0755 "$target"/deploy/*.sh
  chmod 0755 "$target/geoip"
  chmod 0644 "$target"/deploy/systemd/*

  if find "$target/bin" "$target/pdnd-dist" "$target/geoip" "$target/deploy" ! -type d ! -type f -print -quit | grep -q .; then
    echo "release tree contains a non-regular object" >&2
    exit 1
  fi

  crlf_files="$(
    find "$target/deploy" -type f -print0 | xargs -0 grep -rlU $'\r' 2>/dev/null || true
  )"
  if [ -n "$crlf_files" ]; then
    echo "release tree contains CRLF line endings:" >&2
    printf '  %s\n' $crlf_files >&2
    echo "fix .gitattributes or re-checkout with eol=lf; a CRLF script dies on the target host" >&2
    exit 1
  fi

  (
    cd "$target"
    find bin pdnd-dist geoip deploy -type f -print0 \
      | sort -z \
      | xargs -0 sha256sum > SHA256SUMS
  )
  manifest_digest="$(sha256sum "$target/SHA256SUMS" | awk '{print $1}')"
  archive="$target.tar.gz"
  archive_tar="$target.tar"
  rm -f "$archive" "$archive_tar"
  target_base="$(basename "$target")"

  tar_release_scripts=()
  for script in "${release_scripts[@]}"; do
    tar_release_scripts+=("$target_base/deploy/$script")
  done
  tar_release_data=()
  for data in "${release_data[@]}"; do
    tar_release_data+=("$target_base/deploy/$data")
  done
  tar_release_data+=(
    "$target_base/deploy/release-artifact.env"
    "$target_base/deploy/BUILD-INFO"
  )
  tar_release_units=()
  for unit in "${release_units[@]}"; do
    tar_release_units+=("$target_base/deploy/systemd/$unit")
  done

  tar -C "$OUT" --owner=0 --group=0 --numeric-owner --no-recursion \
    --mode=0755 -cf "$archive_tar" "$target_base"
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner \
    --mode=0755 -rf "$archive_tar" "$target_base/bin"
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner \
    --mode=0755 -rf "$archive_tar" "$target_base/pdnd-dist"
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner --no-recursion \
    --mode=0755 -rf "$archive_tar" "$target_base/geoip"
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner \
    --mode=0644 -rf "$archive_tar" "$target_base/geoip/ip2region_v4.xdb"
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner --no-recursion \
    --mode=0755 -rf "$archive_tar" "$target_base/deploy" "$target_base/deploy/systemd"
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner \
    --mode=0755 -rf "$archive_tar" "${tar_release_scripts[@]}"
  tar -C "$OUT" --owner=0 --group=0 --numeric-owner \
    --mode=0644 -rf "$archive_tar" "${tar_release_data[@]}" "${tar_release_units[@]}" "$target_base/SHA256SUMS"
  gzip -n "$archive_tar"
  (
    cd "$OUT"
    sha256sum "$(basename "$archive")" > "$(basename "$archive").sha256"
    printf '%s  SHA256SUMS\n' "$manifest_digest" \
      > "$(basename "$archive").manifest.sha256"
  )
done

echo "release artifacts written to $OUT"

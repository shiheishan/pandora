#!/usr/bin/env bash
# Shared Linux platform detection for Pandora Panel deployment scripts.

pandora_die() {
  echo "pandora-platform: $*" >&2
  exit 1
}

pandora_detect_platform() {
  [ "$(uname -s)" = "Linux" ] || pandora_die "仅支持 Linux 宿主机"
  [ -r /etc/os-release ] || pandora_die "缺少 /etc/os-release，无法确认发行版"

  # shellcheck disable=SC1091
  . /etc/os-release
  PANDORA_DISTRO="${ID:-}"
  PANDORA_VERSION="${VERSION_ID:-}"
  PANDORA_VERSION_MAJOR="${PANDORA_VERSION%%.*}"

  case "$PANDORA_DISTRO:$PANDORA_VERSION_MAJOR" in
    ubuntu:22|ubuntu:23|ubuntu:24|ubuntu:25|ubuntu:26) ;;
    debian:12|debian:13|debian:14) ;;
    *) pandora_die "不支持 ${PRETTY_NAME:-$PANDORA_DISTRO $PANDORA_VERSION}；支持 Ubuntu 22.04-26.04、Debian 12-14" ;;
  esac

  case "$(uname -m)" in
    x86_64|amd64) PANDORA_ARCH=amd64 ;;
    aarch64|arm64) PANDORA_ARCH=arm64 ;;
    *) pandora_die "不支持 CPU 架构 $(uname -m)；仅支持 amd64/x86_64 与 arm64/aarch64" ;;
  esac

  command -v systemctl >/dev/null 2>&1 || pandora_die "需要 systemd"
  [ -d /run/systemd/system ] || pandora_die "systemd 不是当前 init 系统"

  export PANDORA_DISTRO PANDORA_VERSION PANDORA_VERSION_MAJOR PANDORA_ARCH
}

pandora_require_root() {
  [ "$(id -u)" -eq 0 ] || pandora_die "该操作必须由 root 执行"
}

pandora_require_command() {
  command -v "$1" >/dev/null 2>&1 || pandora_die "缺少依赖：$1"
}

pandora_go_version_ok() {
  command -v go >/dev/null 2>&1 || return 1
  local version major minor
  version="$(go env GOVERSION 2>/dev/null)"
  version="${version#go}"
  major="${version%%.*}"
  minor="${version#*.}"
  minor="${minor%%.*}"
  [ "$major" -gt 1 ] || { [ "$major" -eq 1 ] && [ "$minor" -ge 26 ]; }
}


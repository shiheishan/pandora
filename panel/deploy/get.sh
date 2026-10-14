#!/usr/bin/env bash
# Pandora 面板在线安装引导：从 GitHub Release 下载对应架构的发布包，解到 /opt/pandora-release/<版本>/，再 exec 包内 deploy/install.sh。
#
#   首装或升级（最新正式版）：
#     curl -fsSL https://github.com/shiheishan/pandora/releases/latest/download/get.sh | sudo bash
#   指定版本：
#     curl -fsSL https://github.com/shiheishan/pandora/releases/latest/download/get.sh | sudo PANDORA_VERSION=v1.2.3 bash
#   无人值守首装：
#     curl -fsSL https://github.com/shiheishan/pandora/releases/latest/download/get.sh | sudo PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://panel.example.test bash
#   无人值守并建管理员（须先下载脚本，不能把口令塞进 curl|bash 管道）：
#     curl -fsSLo get.sh https://github.com/shiheishan/pandora/releases/latest/download/get.sh
#     printf '%s\n' "$pw" | sudo PANDORA_ADMIN_EMAIL=admin@example.test PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://panel.example.test bash get.sh
#
# 环境变量：
#   PANDORA_VERSION          发布标签（形如 v1.2.3）；不设则解析 releases/latest 跳转得到最新正式版
#   PANDORA_REPO             GitHub 仓库 owner/repo，缺省 shiheishan/pandora（fork 可改）
#   其余 PANDORA_*            原样交给 install.sh（见 install.sh 头注释）
#   PANDORA_GET_SANDBOX      仅桩测试：/opt/pandora-release 与 /dev/tty 映射到此目录下
#
# 目录：/opt/pandora-release/<版本>/pandora-panel_<版本>_linux_<架构>/（root:root 0755）。
# 同版本目录已存在时核对 .manifest.sha256 与包内 SHA256SUMS 后复用；不一致则停止、不覆盖。
# 旧版本目录不自动删除；超过 3 个时提示可手动 rm -rf。
#
# 校验：下载后用 Release 附带的 .sha256 核对包（防传输损坏）；防篡改可用 gh attestation verify；
# install.sh 还会按包内 SHA256SUMS 逐文件核对。exec 前 get.sh 检查父路径链仅 root 可写。
#
# 仅支持 Linux x86_64 / aarch64；须 root；依赖 curl、tar、gzip、sha256sum。
set -euo pipefail

say() { printf '\033[1;32m%s\033[0m\n' "$*"; }
die() { printf '\033[1;31m%s\033[0m\n' "$*" >&2; exit 1; }

usage() {
  cat <<EOF
用法: curl -fsSL https://github.com/shiheishan/pandora/releases/latest/download/get.sh | sudo bash
  PANDORA_VERSION   指定发布标签（如 v1.2.3）
  PANDORA_REPO      GitHub 仓库（缺省 shiheishan/pandora）
其余选项为 install.sh 的环境变量，见 install.sh 头注释。
EOF
}

check_platform() {
  [ "$(uname -s)" = Linux ] || die "只支持 Linux（Debian 12–14 / Ubuntu 22.04–26.04），这台是 $(uname -s)"
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "不支持的 CPU 架构：$(uname -m)（发布包只有 x86_64 与 aarch64）" ;;
  esac
}

check_root() {
  [ "$(id -u)" = 0 ] || die "请用 root 运行：在 bash 前面加 sudo，例如 curl -fsSL https://github.com/$REPO/releases/latest/download/get.sh | sudo bash"
}

check_deps() {
  local missing=() cmd
  for cmd in curl tar gzip sha256sum; do
    command -v "$cmd" >/dev/null 2>&1 || missing+=("$cmd")
  done
  [ "${#missing[@]}" -eq 0 ] || die "缺少 ${missing[*]}：先装上再重跑，例如 apt-get update && apt-get install -y curl tar gzip coreutils"
}

check_repo() {
  if [[ ! "$REPO" =~ ^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$ ]]; then
    die "PANDORA_REPO 不合规：$REPO（形如 owner/repo）"
  fi
  local owner="${REPO%%/*}" repo_name="${REPO#*/}"
  if [ "$owner" = . ] || [ "$owner" = .. ] || [ "$repo_name" = . ] || [ "$repo_name" = .. ]; then
    die "PANDORA_REPO 不合规：$REPO（形如 owner/repo）"
  fi
}

setup_stdin() {
  local self="$1"
  # 文件模式（bash get.sh、bash <(curl …)）：标准输入是部署者自己给的——终端，或者管道里的管理员口令
  #（设了 PANDORA_ADMIN_EMAIL 时 install.sh 从标准输入读一行口令）。原样留给 install.sh，get.sh 不碰、不读。
  [ -z "$self" ] || return 0
  # 管道模式（curl … | bash）：bash 从标准输入读脚本，脚本读完后标准输入只剩 EOF，给不了 install.sh 任何东西。
  if [ -n "${PANDORA_ADMIN_EMAIL:-}" ]; then
    die "设了 PANDORA_ADMIN_EMAIL：install.sh 要从标准输入读管理员口令，但 curl … | bash 时标准输入就是脚本本身。先下载 get.sh 再运行：
  curl -fsSLo get.sh https://github.com/$REPO/releases/latest/download/get.sh
  printf '%s\n' "\$pw" | sudo PANDORA_ADMIN_EMAIL=<邮箱> PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://<域名> bash get.sh"
  fi
  # PANDORA_ASSUME_YES=1：不问任何问题，也不让 apt / dpkg 等在终端上卡住
  if [ "${PANDORA_ASSUME_YES:-}" = 1 ]; then exec </dev/null; return 0; fi
  # 有控制终端则接到 /dev/tty，install.sh 才能问对外地址、不回显地问口令
  if { : <"$TTY"; } 2>/dev/null; then exec <"$TTY"; return 0; fi
  # 只给了对外地址时首装不必再问；管理员留给收尾提示里的手工命令
  if [ -n "${PANDORA_PUBLIC_BASE_URL:-}" ]; then exec </dev/null; return 0; fi
  die "没有终端可以提问（ssh 没带 -t、cloud-init、CI 里跑？），首装要问对外地址和管理员。两种办法：
  交互：ssh -t 登录后再跑同一条命令
  无人值守：curl -fsSL https://github.com/$REPO/releases/latest/download/get.sh | sudo PANDORA_ASSUME_YES=1 PANDORA_PUBLIC_BASE_URL=https://<域名或公网IP> bash"
}

valid_version() {
  # 与 build-release.sh 的版本号字符集一致（[A-Za-z0-9._-]+，不能是 . / ..），再限定 v 开头（发版标签 v*）
  [[ "$1" =~ ^v[A-Za-z0-9._-]*$ ]]
}

resolve_version() {
  if [ -n "${PANDORA_VERSION:-}" ]; then
    valid_version "$PANDORA_VERSION" || die "版本号不合规：$PANDORA_VERSION（发布标签形如 v1.2.3，只能有字母、数字、点、下划线、连字符）"
    VERSION="$PANDORA_VERSION"
    return 0
  fi
  local latest_url loc candidate
  # 用 releases/latest 的 302 跳转解析标签，不调 API，不受限流；latest 只指向正式版
  latest_url="https://github.com/$REPO/releases/latest"
  loc="$(curl -fsS --proto '=https' --connect-timeout 15 --max-time 60 -o /dev/null -w '%{redirect_url}' "$latest_url")" \
    || die "连不上 GitHub，或仓库 $REPO 不存在：$latest_url"
  if [[ "$loc" =~ ^https://github\.com/[^/]+/[^/]+/releases/tag/([^/?#]+)$ ]]; then
    candidate="${BASH_REMATCH[1]}"
    valid_version "$candidate" || die "版本号不合规：$candidate（发布标签形如 v1.2.3，只能有字母、数字、点、下划线、连字符）"
    VERSION="$candidate"
    return 0
  fi
  die "没有找到 $REPO 的正式版（还没发布过，或仓库名不对）；预发布版要用 PANDORA_VERSION=<标签> 指定"
}

check_owner_chain() {
  local p="$1" owner perm
  while :; do
    read -r owner perm < <(stat -c '%u %a' -- "$p") || die "读不出 $p 的属主与权限"
    if [ "$owner" != 0 ] || (( (8#$perm & 8#022) != 0 )); then
      die "$p 不归 root 或可被他人写入。发布目录必须在 root 独占的路径下（防止有人在解包与执行之间换掉文件）；改好属主与权限再重跑"
    fi
    [ "$p" = / ] && break
    p="$(dirname -- "$p")"
  done
}

prepare_base() {
  mkdir -p -- "$BASE"
  BASE="$(cd -- "$BASE" && pwd -P)"
  # exec 的是这个目录里的 install.sh，install.sh 自检那时已经晚了，所以 get.sh 在 exec 前查路径链
  check_owner_chain "$BASE"
}

fetch() {
  curl -fsSL --proto '=https' --connect-timeout 15 --retry 3 -o "$2" "$1" || die "下载失败：$1"
}

read_sum() {
  local f="$1" want_name="$2" lines sum fname extra
  lines="$(wc -l < "$f")"
  [ "$lines" -eq 1 ] || return 1
  read -r sum fname extra < "$f"
  [[ "$sum" =~ ^[0-9a-f]{64}$ ]] || return 1
  [ "$fname" = "$want_name" ] || return 1
  [ -z "${extra:-}" ] || return 1
  SUM="$sum"
  return 0
}

cleanup() {
  [ -z "${DL:-}" ] || rm -rf -- "$DL"
  [ -z "${WORK:-}" ] || rm -rf -- "$WORK"
}

extract_fresh() {
  say "下载 $ARCHIVE"
  fetch "$BASE_URL/$ARCHIVE" "$DL/$ARCHIVE"
  fetch "$BASE_URL/$ARCHIVE.sha256" "$DL/$ARCHIVE.sha256"
  read_sum "$DL/$ARCHIVE.sha256" "$ARCHIVE" || die "校验文件格式不对：$ARCHIVE.sha256（应为一行「<SHA-256>  $ARCHIVE」）"
  # .sha256 与包同源（同一 Release），只防下载损坏；防篡改用 gh attestation verify；install.sh 还会按 SHA256SUMS 逐文件核对
  (cd "$DL" && sha256sum --quiet --strict -c "$ARCHIVE.sha256") || die "校验失败：$ARCHIVE 与 $ARCHIVE.sha256 对不上（下载损坏？），什么都没改；重跑一次，还不对就换个网络再试"
  WORK="$(mktemp -d "$BASE/.tmp-$VERSION.XXXXXX")"
  # --no-same-owner 归 root；--no-same-permissions + umask 022 去掉组/他人写权限，满足 install.sh 的 pandora_verify_release_tree
  tar --no-same-owner --no-same-permissions -xzf "$DL/$ARCHIVE" -C "$WORK" || die "解包失败：$ARCHIVE"
  local n
  n="$(find "$WORK" -mindepth 1 -maxdepth 1 | wc -l)"
  [ "$n" -eq 1 ] && [ -f "$WORK/$NAME/deploy/install.sh" ] || die "包的结构不对：应只有 $NAME/ 一个顶层目录，且带 deploy/install.sh"
  chmod 0755 "$WORK"
  if [ -e "$DEST" ] || [ -L "$DEST" ]; then
    die "$DEST 刚被别的进程建好（同时跑了两个 get.sh？），没有覆盖；重跑一次即可"
  fi
  mv -- "$WORK" "$DEST"
  WORK=""
  say "已解到 $DEST"
}

reuse_existing() {
  [ -d "$DEST" ] && [ ! -L "$DEST" ] || die "$DEST 已存在但不是目录，没有覆盖；确认没用后删掉再重跑：sudo rm -rf $DEST"
  local want got
  fetch "$BASE_URL/$ARCHIVE.manifest.sha256" "$DL/$ARCHIVE.manifest.sha256"
  read_sum "$DL/$ARCHIVE.manifest.sha256" SHA256SUMS || die "校验文件格式不对：$ARCHIVE.manifest.sha256"
  want="$SUM"
  # .manifest.sha256 是发布时对包内 SHA256SUMS 的摘要，不用重下整包即可确认已解目录与发布一致
  if [ -f "$TREE/SHA256SUMS" ]; then
    got="$(sha256sum "$TREE/SHA256SUMS" | awk '{print $1}')"
    if [ "$got" = "$want" ] && (cd "$TREE" && sha256sum --quiet --strict -c SHA256SUMS); then
      say "复用已解好的 $DEST（与发布的 $VERSION 一致）"
      return 0
    fi
  fi
  die "$DEST 已存在，但与发布的 $VERSION 不一致（SHA256SUMS 对不上或文件被改过），没有覆盖。确认里面的东西不要了再删掉重跑：sudo rm -rf $DEST"
}

hint_old_versions() {
  local dirs=() d n rm_args
  for d in "$BASE"/v*; do
    [ -e "$d" ] || continue
    [ -d "$d" ] && [ ! -L "$d" ] && dirs+=("$d")
  done
  n="${#dirs[@]}"
  [ "$n" -le 3 ] && return 0
  say "提示：$BASE 下已有 $n 个版本目录，get.sh 不自动删；不用的旧版本可以删掉（正要装的 $VERSION 别删）："
  rm_args=()
  for d in "${dirs[@]}"; do
    [ "$d" = "$DEST" ] && continue
    rm_args+=("$(printf '%q' "$d")")
  done
  printf '  sudo rm -rf %s\n' "${rm_args[*]}"
}

main() {
  for arg in "$@"; do
    case "$arg" in
      -h|--help) usage; exit 0 ;;
      *) die "不认识的参数：$arg（只认 --help；选项用环境变量，见脚本头注释）" ;;
    esac
  done
  umask 022
  SB="${PANDORA_GET_SANDBOX:-}"
  BASE="$SB/opt/pandora-release"
  if [ -n "$SB" ]; then TTY="$SB/dev/tty"; else TTY=/dev/tty; fi
  REPO="${PANDORA_REPO:-shiheishan/pandora}"
  check_platform
  detect_arch
  check_root
  check_deps
  check_repo
  setup_stdin "$GET_SH_SOURCE"
  resolve_version
  NAME="pandora-panel_${VERSION}_linux_${ARCH}"
  ARCHIVE="$NAME.tar.gz"
  BASE_URL="https://github.com/$REPO/releases/download/$VERSION"
  prepare_base
  DEST="$BASE/$VERSION"
  TREE="$DEST/$NAME"
  DL=""
  WORK=""
  trap cleanup EXIT
  DL="$(mktemp -d "$BASE/.dl-$VERSION.XXXXXX")"
  if [ -e "$DEST" ] || [ -L "$DEST" ]; then reuse_existing; else extract_fresh; fi
  hint_old_versions
  # exec 之后 EXIT trap 不会跑，先 cleanup 再取消 trap；PANDORA_* 随 exec 交给 install.sh，不传参数
  cleanup
  trap - EXIT
  say "Pandora $VERSION（$ARCH）→ 运行 $TREE/deploy/install.sh"
  exec bash "$TREE/deploy/install.sh"
}

# curl … | bash 时 bash 边读边执行：逻辑都在 main 里，文件末尾才调用 main，脚本未完整下载则不会执行。
# BASH_SOURCE[0] 只在顶层取：管道模式下为空；bash get.sh 时为脚本路径（函数内各版本 bash 行为不一致）。
GET_SH_SOURCE="${BASH_SOURCE[0]:-}"
main "$@"

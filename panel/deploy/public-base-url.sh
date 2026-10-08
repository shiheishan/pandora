#!/usr/bin/env bash
# 发布包装出来的面板以 production 运行，网关启动时要求 AEGIS_PUBLIC_BASE_URL 是
# https + 公网 Host（platform/config 的 CanonicalPublicOrigin），nginx 的 server_name
# 与证书也从它生成。所以首装在动手之前就要拿到一个合规的地址：域名，或本机公网 IPv4
# （没有域名时的默认，装完自动申请 Let's Encrypt 的 IP 证书）。

# 公网 IPv4：点分十进制、不带前导零；排除私网、回环、链路本地、运营商级 NAT、本网络、
# IETF 协议分配、基准测试、组播与保留段。文档段（TEST-NET）视同公网，测试夹具用它们。
pandora_public_ipv4() {
  local o='(0|[1-9][0-9]{0,2})' a b c d
  [[ "$1" =~ ^$o\.$o\.$o\.$o$ ]] || return 1
  a="${BASH_REMATCH[1]}" b="${BASH_REMATCH[2]}" c="${BASH_REMATCH[3]}" d="${BASH_REMATCH[4]}"
  (( a <= 255 && b <= 255 && c <= 255 && d <= 255 )) || return 1
  (( a != 0 && a != 10 && a != 127 && a < 224 )) || return 1
  (( !(a == 100 && b >= 64 && b <= 127) )) || return 1
  (( !(a == 169 && b == 254) )) || return 1
  (( !(a == 172 && b >= 16 && b <= 31) )) || return 1
  (( !(a == 192 && b == 168) )) || return 1
  (( !(a == 192 && b == 0 && c == 0) )) || return 1
  (( !(a == 198 && (b == 18 || b == 19)) ))
}

# 与 render-nginx.sh、platform/config 的 CanonicalPublicOrigin（生产）同一规则，用例表在
# fixtures/public-base-url-cases.txt：https://<DNS 域名或公网 IPv4>，不带端口与路径，
# 不是 localhost。IPv6 字面量不收（只有 IPv6 的机器用解析到它的域名）。
pandora_valid_public_base_url() {
  local url="${1%/}" host
  [[ "$url" != *CHANGE_ME* ]] || return 1
  [[ "$url" =~ ^https://([^/:]+)$ ]] || return 1
  host="${BASH_REMATCH[1],,}"
  pandora_public_ipv4 "$host" && return 0
  [[ ${#host} -le 253 ]] || return 1
  [[ "$host" != *.localhost ]] || return 1
  [[ "$host" =~ ^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]([a-z0-9-]*[a-z0-9])?$ ]]
}

# 本机出公网的 IPv4（路由表里去往公网的源地址，不发任何包、不问外部服务）。查的是文档段
# 192.0.2.1 的路由：本机没有它的专门路由，走的就是默认路由。只在源地址本身是公网地址时打印；
# NAT 后面（源地址是私网）打印空，调用方只能让人给地址。
pandora_detect_public_ipv4() {
  local src
  command -v ip >/dev/null 2>&1 || return 0
  src="$(ip -4 -o route get 192.0.2.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\).*/\1/p' | head -1)"
  if pandora_public_ipv4 "$src"; then printf '%s\n' "$src"; fi
  return 0
}

# 取首装的对外地址：先读 PANDORA_PUBLIC_BASE_URL；没有时在交互终端里现场问（直接回车用本机
# 公网 IPv4），无人值守就直接用本机公网 IPv4。合规则把去掉尾斜杠的值打印到 stdout；否则把原因
# 与重跑命令（$1）写到 stderr，返回 1。提示语写 stderr，调用方可以直接 URL="$(pandora_resolve_public_base_url ...)"。
pandora_resolve_public_base_url() {
  local rerun="$1" url="${PANDORA_PUBLIC_BASE_URL:-}" ip="" fallback=""
  if [[ -z "$url" ]]; then
    ip="$(pandora_detect_public_ipv4)"
    [[ -z "$ip" ]] || fallback="https://$ip"
  fi
  if [[ -z "$url" && "${PANDORA_ASSUME_YES:-}" != 1 && -t 0 ]]; then
    printf '    面板对外地址（接入命令、支付回调、订阅链接都从它拼出来）：\n' >&2
    printf '      有域名填 https://你的域名；没有域名用本机公网 IP，装完自动申请 IP 证书\n' >&2
    if [[ -n "$fallback" ]]; then
      printf '    [直接回车用 %s] ' "$fallback" >&2
    else
      printf '    ' >&2
    fi
    read -r url || url=""
  fi
  if [[ -z "$url" && -n "$fallback" ]]; then
    url="$fallback"
    printf '    没有给对外地址，用本机公网 IPv4：%s（以后有了域名，改 .env 的 AEGIS_PUBLIC_BASE_URL 再重跑安装脚本）\n' "$url" >&2
  fi
  if [[ -z "$url" ]]; then
    printf '%s\n' \
      "首装需要面板的对外地址：面板以 production 模式运行，网关启动时要求 AEGIS_PUBLIC_BASE_URL" \
      "是 https://域名 或 https://公网IPv4，否则拒绝启动。本机没找到公网 IPv4（在 NAT 后面？），" \
      "请带上它重新运行，例如：" \
      "  sudo PANDORA_PUBLIC_BASE_URL=https://panel.example.com $rerun" \
      "  sudo PANDORA_PUBLIC_BASE_URL=https://<本机公网IPv4> $rerun" >&2
    return 1
  fi
  url="${url%/}"
  if ! pandora_valid_public_base_url "$url"; then
    printf '%s\n' \
      "面板对外地址不合规：$url" \
      "必须形如 https://panel.example.com 或 https://<公网IPv4>：https、DNS 域名或公网 IPv4" \
      "（不能是 localhost、私网 IP、IPv6 字面量），不带端口与路径。" \
      "production 模式下网关拿不到这样的地址会拒绝启动；nginx 的 server_name 与证书也从它生成。" \
      "改正后重新运行：sudo PANDORA_PUBLIC_BASE_URL=https://你的域名或公网IP $rerun" >&2
    return 1
  fi
  printf '%s\n' "$url"
}

# 读 .env 的单个键（最后一次出现为准），不 source：文件里是机密，不执行它。
pandora_env_file_value() {
  awk -F= -v key="$2" '$1 == key { sub(/^[^=]*=/, ""); sub(/\r$/, ""); v = $0 } END { print v }' "$1"
}

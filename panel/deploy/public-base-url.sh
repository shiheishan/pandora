#!/usr/bin/env bash
# [INPUT]: 依赖调用方的 PANDORA_PUBLIC_BASE_URL、PANDORA_ASSUME_YES 与终端（stdin）
# [OUTPUT]: 被 source 的函数：pandora_valid_public_base_url（校验）、pandora_resolve_public_base_url（取值并校验，打印到 stdout）、pandora_env_file_value（不 source 地读 .env 的单个键）
# [POS]: deploy 两个安装脚本 install.sh 与 install-native.sh 共用的首装对外地址闸门；规则与 render-nginx.sh 相同，桩测试 public-base-url_mock_test.sh
#
# 发布包装出来的面板以 production 运行，网关启动时要求 AEGIS_PUBLIC_BASE_URL 是
# https + 公网 Host（platform/config 的 CanonicalPublicOrigin），nginx 的 server_name
# 与证书路径也从它生成。所以首装在动手之前就要拿到一个合规的域名。

# 与 render-nginx.sh 同一规则：https://<DNS 域名>，不带端口与路径，不是 IP、不是 localhost。
pandora_valid_public_base_url() {
  local url="${1%/}" host
  [[ "$url" != *CHANGE_ME* ]] || return 1
  [[ "$url" =~ ^https://([^/:]+)$ ]] || return 1
  host="${BASH_REMATCH[1],,}"
  [[ ${#host} -le 253 ]] || return 1
  [[ "$host" != *.localhost ]] || return 1
  [[ "$host" =~ ^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]([a-z0-9-]*[a-z0-9])?$ ]]
}

# 取首装的对外地址：先读 PANDORA_PUBLIC_BASE_URL，没有且在交互终端里就现场问。
# 合规则把去掉尾斜杠的值打印到 stdout；否则把原因与重跑命令（$1）写到 stderr，返回 1。
# 提示语写 stderr，调用方可以直接 URL="$(pandora_resolve_public_base_url ...)"。
pandora_resolve_public_base_url() {
  local rerun="$1" url="${PANDORA_PUBLIC_BASE_URL:-}"
  if [[ -z "$url" && "${PANDORA_ASSUME_YES:-}" != 1 && -t 0 ]]; then
    printf '    面板对外地址（https://你的域名，接入命令、支付回调、订阅链接都从它拼出来）：' >&2
    read -r url || url=""
  fi
  if [[ -z "$url" ]]; then
    printf '%s\n' \
      "首装需要面板的对外地址：面板以 production 模式运行，网关启动时要求 AEGIS_PUBLIC_BASE_URL" \
      "是 https://公网域名，否则拒绝启动。请带上它重新运行，例如：" \
      "  sudo PANDORA_PUBLIC_BASE_URL=https://panel.example.com $rerun" >&2
    return 1
  fi
  url="${url%/}"
  if ! pandora_valid_public_base_url "$url"; then
    printf '%s\n' \
      "面板对外地址不合规：$url" \
      "必须形如 https://panel.example.com：https、DNS 域名（不能是 IP、localhost），不带端口与路径。" \
      "production 模式下网关拿不到这样的地址会拒绝启动；nginx 的 server_name 与证书路径也从它生成。" \
      "改正后重新运行：sudo PANDORA_PUBLIC_BASE_URL=https://你的域名 $rerun" >&2
    return 1
  fi
  printf '%s\n' "$url"
}

# 读 .env 的单个键（最后一次出现为准），不 source：文件里是机密，不执行它。
pandora_env_file_value() {
  awk -F= -v key="$2" '$1 == key { sub(/^[^=]*=/, ""); sub(/\r$/, ""); v = $0 } END { print v }' "$1"
}

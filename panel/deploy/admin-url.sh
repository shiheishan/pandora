#!/usr/bin/env bash
# 随时重看管理后台的完整地址。
#
#   sudo /opt/aegispanel/deploy/admin-url.sh        （install.sh 装的）
#   sudo /opt/pandora/deploy/admin-url.sh           （install-native.sh 装的）
#   admin-url.sh <.env 路径>                         （指定别的 .env）
#
# 后台前缀是安装时随机生成的高熵路径，只记在 .env 的 AEGIS_ADMIN_PATH 里；有
# AEGIS_PUBLIC_BASE_URL 就拼成完整 URL。只读 .env 的这两个键，不 source 它（里面是机密），
# 也不打印别的值。后台地址本身就是入口，别贴到公开的地方。
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
ENV_FILE="${1:-$HERE/.env}"

[ -e "$ENV_FILE" ] || { echo "找不到 $ENV_FILE（面板装在别处时把 .env 路径作为参数传进来）" >&2; exit 1; }
[ -r "$ENV_FILE" ] || { echo "读不了 $ENV_FILE：它只给 root 读，用 sudo 再跑一次" >&2; exit 1; }

# 读单个键（最后一次出现为准），与 public-base-url.sh 的 pandora_env_file_value 同一口径
env_value() {
  awk -F= -v key="$1" '$1 == key { sub(/^[^=]*=/, ""); sub(/\r$/, ""); v = $0 } END { print v }' "$ENV_FILE"
}

admin_path="$(env_value AEGIS_ADMIN_PATH)"
admin_path="${admin_path#/}"; admin_path="${admin_path%/}"
if [ -z "$admin_path" ] || [[ "$admin_path" == *CHANGE_ME* ]]; then
  echo "$ENV_FILE 里没有设置 AEGIS_ADMIN_PATH" >&2
  exit 1
fi
[[ "$admin_path" =~ ^[A-Za-z0-9_-]+$ ]] || { echo "AEGIS_ADMIN_PATH 不是一个 URL 安全的路径段，检查 $ENV_FILE" >&2; exit 1; }

base="$(env_value AEGIS_PUBLIC_BASE_URL)"
base="${base%/}"
if [ -n "$base" ] && [[ "$base" != *CHANGE_ME* ]]; then
  printf '%s/%s/\n' "$base" "$admin_path"
else
  printf '/%s/\n' "$admin_path"
  echo "（.env 没有 AEGIS_PUBLIC_BASE_URL，上面只是路径：拼在面板域名后面访问）" >&2
fi

#!/usr/bin/env bash
# 随时重看管理后台的完整地址：sudo /opt/pandora/deploy/admin-url.sh
# 只读 deploy.env 的 PANDORA_ADMIN_PATH 与 config.yaml 的 public_base_url，不 source 机密文件。
set -euo pipefail

ETC="${PANDORA_ETC_DIR:-/etc/pandora}"
DEPLOY_ENV="$ETC/deploy.env"
CONFIG_YAML="$ETC/config.yaml"

env_file_value() {
  awk -F= -v key="$1" '$1 == key { sub(/^[^=]*=/, ""); sub(/\r$/, ""); v = $0 } END { print v }' "$DEPLOY_ENV"
}

config_top_value() {
  awk -v key="public_base_url" '
    /^[[:space:]]/ { next }
    $0 ~ "^" key ":" {
      sub(/^[^:]*:[[:space:]]*/, "")
      gsub(/^[[:space:]]+|[[:space:]]+$/, "")
      if (length($0) >= 2 && substr($0, 1, 1) == "\"" && substr($0, length($0), 1) == "\"") {
        print substr($0, 2, length($0) - 2)
        next
      }
      if (length($0) >= 2 && substr($0, 1, 1) == "\047" && substr($0, length($0), 1) == "\047") {
        print substr($0, 2, length($0) - 2)
        next
      }
      print $0
    }
  ' "$CONFIG_YAML"
}

if [ ! -e "$DEPLOY_ENV" ]; then
  echo "找不到 $DEPLOY_ENV" >&2
  exit 1
fi
if [ ! -r "$DEPLOY_ENV" ]; then
  echo "读不了 $DEPLOY_ENV：请用 sudo 再跑一次" >&2
  exit 1
fi

admin_path="$(env_file_value PANDORA_ADMIN_PATH)"
admin_path="${admin_path#/}"
admin_path="${admin_path%/}"
if [ -z "$admin_path" ]; then
  echo "$DEPLOY_ENV 里没有设置 PANDORA_ADMIN_PATH" >&2
  exit 1
fi
if [[ ! "$admin_path" =~ ^[A-Za-z0-9_-]+$ ]]; then
  echo "PANDORA_ADMIN_PATH 不是一个 URL 安全的路径段，检查 $DEPLOY_ENV" >&2
  exit 1
fi

base=""
if [ -r "$CONFIG_YAML" ]; then
  base="$(config_top_value public_base_url)"
  base="${base%/}"
fi

if [ -n "$base" ]; then
  printf '%s/%s/\n' "$base" "$admin_path"
else
  printf '/%s/\n' "$admin_path"
  echo "（config.yaml 没有可用的 public_base_url，上面只是路径：拼在面板域名后面访问）" >&2
fi

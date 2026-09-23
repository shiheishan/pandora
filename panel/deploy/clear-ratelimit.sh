#!/usr/bin/env bash
# 清空限流计数器。仅供开发与集成测试使用。
#
# 必须用 while read 而不是 for k in $(...)：限流键可能含空格等字符，
# 词分割会让 DEL 删到错误的键名，表现为「怎么清都还在被限流」。
set -euo pipefail
cd "$(dirname "$0")"
set -a; . ./.env; set +a
n=0
while IFS= read -r k; do
  [ -z "$k" ] && continue
  docker exec aegis-valkey valkey-cli -a "$VALKEY_PASSWORD" --no-auth-warning DEL "$k" >/dev/null
  n=$((n+1))
done < <(docker exec aegis-valkey valkey-cli -a "$VALKEY_PASSWORD" --no-auth-warning --scan --pattern "rl:*")
echo "已清理 $n 个限流键，剩余 $(docker exec aegis-valkey valkey-cli -a "$VALKEY_PASSWORD" --no-auth-warning --scan --pattern "rl:*" | wc -l)"

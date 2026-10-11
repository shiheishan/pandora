#!/usr/bin/env bash
# 清空开发数据基座（dev/docker-compose.yml，容器 pandora-dev-valkey）里的全部限流计数器 rl:*。
# 只给本机开发与集成测试：所有用户的冷却一起没了。测试机与生产上不用它（那里只按完整键名删单个键）。
#
# 口令从 deploy/.env 的 VALKEY_PASSWORD 读，经环境变量 REDISCLI_AUTH 交给容器里的 valkey-cli，
# 不进任何命令行参数（docker exec -e 只写变量名，值取自本进程环境）。
# 必须用 while read 而不是 for k in $(...)：限流键可能含空格等字符，
# 词分割会让 DEL 删到错误的键名，表现为「怎么清都还在被限流」。
set -euo pipefail
cd "$(dirname "$0")"
ENV_FILE=../deploy/.env
[ -r "$ENV_FILE" ] || { echo "clear-ratelimit: 读不到 $(pwd)/$ENV_FILE（从 deploy/.env.example 复制后填）" >&2; exit 1; }
REDISCLI_AUTH="$(awk -F= '$1 == "VALKEY_PASSWORD" { sub(/^[^=]*=/, ""); sub(/\r$/, ""); v = $0 } END { print v }' "$ENV_FILE")"
[ -n "$REDISCLI_AUTH" ] || { echo "clear-ratelimit: $ENV_FILE 里没有 VALKEY_PASSWORD" >&2; exit 1; }
export REDISCLI_AUTH
CONTAINER=pandora-dev-valkey
vk() { docker exec -i -e REDISCLI_AUTH "$CONTAINER" valkey-cli "$@"; }
n=0
while IFS= read -r k; do
  [ -z "$k" ] && continue
  vk DEL "$k" >/dev/null
  n=$((n+1))
done < <(vk --scan --pattern "rl:*")
echo "已清理 $n 个限流键，剩余 $(vk --scan --pattern "rl:*" | wc -l | tr -d ' ')"

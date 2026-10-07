#!/usr/bin/env bash
# 把一个提交的 CI 失败压成一页：检查机结论、每个失败 run 的失败测试与首条报错、已知偶发项。
# 用法：triage.sh <sha 或分支> [输出目录]   只读；完整失败日志存进输出目录（默认 $TMPDIR/ci-triage-<sha>）。
set -euo pipefail
ref="${1:?sha 或分支}"
repo="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
git fetch -q origin 2>/dev/null || true
if git rev-parse -q --verify "origin/$ref" >/dev/null; then sha="$(git rev-parse "origin/$ref")"; else sha="$(git rev-parse "$ref")"; fi
out="${2:-${TMPDIR:-/tmp}/ci-triage-${sha:0:12}}"
mkdir -p "$out"
echo "提交 $(git log -1 --format='%h %s' "$sha")"
echo "日志目录 $out"

echo
echo "== 检查机（memoh/ci-replay）"
gh api "repos/$repo/commits/$sha/statuses" \
  --jq '[.[] | select(.context=="memoh/ci-replay")][0] | "  \(.state)  \(.description // "")"' || true
echo "  检查机的完整日志在云电脑 /data/memoh-ci/logs/<sha>/<job>.log，本机读不到；它红而 GitHub 同一 job 绿，先按偶发处理（见 SKILL.md）"

echo
echo "== GitHub Actions"
runs="$(gh run list -R "$repo" --commit "$sha" --json databaseId,name,conclusion,status \
  --jq '.[] | "\(.databaseId)\t\(.name)\t\(.status)/\(.conclusion)"')"
[ -n "$runs" ] || { echo "  这个提交没有 run（路径过滤没触发，或一次推多个提交时只有最新那个有 run）"; exit 0; }
printf '%s\n' "$runs" | sed 's/^/  /'

printf '%s\n' "$runs" | while IFS=$'\t' read -r id name state; do
  case "$state" in */failure|*/cancelled|*/timed_out) ;; *) continue ;; esac
  log="$out/$id.log"
  gh run view "$id" -R "$repo" --log-failed > "$log" 2>&1 || true
  # 去掉「job<TAB>step<TAB>时间戳 」前缀与颜色码，只留正文
  clean="$out/$id.txt"
  # BSD sed 不认 \t，用 perl
  perl -pe 's/^[^\t]*\t[^\t]*\t\x{FEFF}?[0-9TZ:.-]+ //; s/\e\[[0-9;]*m//g; s/\^\[\[[0-9;]*m//g' "$log" > "$clean"
  echo
  echo "== 失败 run $id「$name」（$state）"
  echo "-- 失败步骤"
  grep -E '^##\[error\]' "$clean" | sort -u | head -8 | sed 's/^/  /' || true
  echo "-- Go 失败测试（含首条报错）"
  grep -nE -- '--- FAIL: ' "$clean" | sed -E 's/^[0-9]+:\s*//' | sort -u | head -20 | sed 's/^/  /' || true
  grep -nE -- '--- FAIL: ' "$clean" | cut -d: -f1 | while read -r n; do
    start=$((n > 15 ? n - 15 : 1))
    sed -n "${start},${n}p" "$clean" | grep -E '_test\.go:[0-9]+:' | tail -1 | sed 's/^ */    → /'
  done | sort -u | head -20 || true
  echo "-- SQL 报错"
  grep -oE 'ERROR: [^(]{0,120}\(SQLSTATE [0-9A-Z]+\)' "$clean" | sort | uniq -c | sort -rn | head -8 | sed 's/^/  /' || true
  echo "-- 前端 vitest"
  grep -E '^ *FAIL +tests/|Failed Tests|Tests +[0-9]+ failed' "$clean" | sort -u | head -10 | sed 's/^/  /' || true
  grep -E 'Serialized Error|返回 [0-9]{3} ' "$clean" | sort -u | head -5 | sed 's/^/  /' || true
  echo "-- e2e 脚本"
  grep -E '失败（退出码|\[FAIL\]' "$clean" | head -12 | sed 's/^/  /' || true
  echo "-- 已知偶发"
  if grep -qE 'TestIdempotencyMiddlewarePG18' "$clean" && grep -qE 'lock timeout|55P03|canceling statement due to lock' "$clean"; then
    echo "  TestIdempotencyMiddlewarePG18 锁超时：gh run rerun $id -R $repo --failed"
  fi
  if grep -qE 'recent account payments have failed|spending limit needs to be increased' "$log"; then
    echo "  Actions 没启动（付款问题）：gh run rerun $id -R $repo"
  fi
done

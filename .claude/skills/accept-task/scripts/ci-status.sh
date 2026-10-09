#!/usr/bin/env bash
# 看一个任务分支头（或任意提交）的 CI 结论：检查机回写的 commit status 加 GitHub Actions 各 workflow。
# 用法：ci-status.sh <分支名或 sha>   例：ci-status.sh feat/panel-redesign-w2pay
# 只读，不等待；要等结论用 ops-local/memoh-ci 的 wait-status.sh / wait-github.sh。
# 给分支名时只取该分支的 run；给 sha 时列出全部并标出 headBranch（同一个 sha 推到两个分支会混进别的分支的 run）。
set -euo pipefail
ref="${1:?分支或 sha}"
repo="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
branch=""
if git show-ref --verify -q "refs/remotes/origin/$ref" || git ls-remote --exit-code -q origin "refs/heads/$ref" >/dev/null 2>&1; then
  git fetch -q origin "$ref"
  sha="$(git rev-parse "origin/$ref")"
  branch="$ref"
else
  sha="$(git rev-parse "$ref")"
fi
echo "提交 $(git log -1 --format='%h %s' "$sha")"
echo "-- commit status（检查机）"
gh api "repos/$repo/commits/$sha/status" --jq '.statuses[] | "  \(.context)  \(.state)  \(.description // "")"'
echo "-- GitHub Actions"
runs="$(gh run list --commit "$sha" ${branch:+--branch "$branch"} --json name,status,conclusion,headBranch --jq '.[] | "  \(.name)  \(.status)/\(.conclusion)  [\(.headBranch)]"')"
[ -z "$branch" ] || echo "  只取分支 $branch 的 run"
printf '%s\n' "$runs"
if [ -z "$branch" ] && [ "$(printf '%s\n' "$runs" | grep -o '\[[^]]*\]$' | sort -u | wc -l)" -gt 1 ]; then
  echo "  注意：这个提交在多个分支都有 run（方括号里是 headBranch），别把别的分支的红算到当前分支上"
fi
# gh run list --commit 只认完整 sha；一次推多个提交时只有最新那个有 run

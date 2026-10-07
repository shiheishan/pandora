#!/usr/bin/env bash
# 看一个任务分支头（或任意提交）的 CI 结论：检查机回写的 commit status 加 GitHub Actions 各 workflow。
# 用法：ci-status.sh <分支名或 sha>   例：ci-status.sh feat/panel-redesign-w2pay
# 只读，不等待；要等结论用 ops-local/memoh-ci 的 wait-status.sh / wait-github.sh。
set -euo pipefail
ref="${1:?分支或 sha}"
repo="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
if git show-ref --verify -q "refs/remotes/origin/$ref" || git ls-remote --exit-code -q origin "refs/heads/$ref" >/dev/null 2>&1; then
  git fetch -q origin "$ref"
  sha="$(git rev-parse "origin/$ref")"
else
  sha="$(git rev-parse "$ref")"
fi
echo "提交 $(git log -1 --format='%h %s' "$sha")"
echo "-- commit status（检查机）"
gh api "repos/$repo/commits/$sha/status" --jq '.statuses[] | "  \(.context)  \(.state)  \(.description // "")"'
echo "-- GitHub Actions"
gh run list --commit "$sha" --json name,status,conclusion --jq '.[] | "  \(.name)  \(.status)/\(.conclusion)"'
# gh run list --commit 只认完整 sha；一次推多个提交时只有最新那个有 run

#!/usr/bin/env bash
# 从主线开一个任务 worktree：../pandora-<名字>，分支 feat/panel-redesign-<名字>。
# 用法：new-worktree.sh <名字> [基点，默认 feat/panel-redesign]
set -euo pipefail
name="${1:?用法: new-worktree.sh <名字> [基点]}"
base="${2:-feat/panel-redesign}"
root="$(git rev-parse --show-toplevel)"
main="$(cd "$(git -C "$root" rev-parse --git-common-dir)/.." && pwd)"
dir="$(dirname "$main")/pandora-$name"
branch="feat/panel-redesign-$name"

[[ "$name" =~ ^[a-z0-9-]+$ ]] || { echo "名字只用小写字母、数字、连字符" >&2; exit 2; }
[ ! -e "$dir" ] || { echo "已存在: $dir" >&2; exit 1; }
git -C "$main" rev-parse --verify -q "$branch" >/dev/null && { echo "分支已存在: $branch" >&2; exit 1; }

git -C "$main" worktree add -q "$dir" -b "$branch" "$base"
mkdir -p "$dir/.claude"
git -C "$dir" check-ignore -q .claude/brief.md || { echo "警告：.claude/brief.md 没被忽略" >&2; exit 1; }
echo "worktree: $dir"
echo "branch:   $branch @ $(git -C "$dir" rev-parse --short HEAD)"

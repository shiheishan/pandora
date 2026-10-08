#!/usr/bin/env bash
# 机器删掉之后撤登记：unregister.sh <别名>... 去掉 ~/.ssh/config 的 Host 块、总表一行、ops-local/vpc-hosts.tsv 一行，
# ssh-keygen -R 公网地址，AGENTS.md 顶部注明已删（目录保留作记录），再重生成私有 gitleaks 规则。~/.ssh/config 先备份到 /tmp 外的同目录。
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
root="$(dirname "$(git -C "$here" rev-parse --path-format=absolute --git-common-dir)")"
day="$(date -u +%F)"
for a in "$@"; do
  ip="$(ssh -G "$a" 2>/dev/null | awk '$1=="hostname"{print $2; exit}')"
  cp "$HOME/.ssh/config" "$HOME/.ssh/config.bak-unregister"
  python3 - "$a" "$HOME/.ssh/config" "$HOME/ai/servers/README.md" "$root/ops-local/vpc-hosts.tsv" "$HOME/ai/servers/$a/AGENTS.md" "$day" <<'PY'
import re, sys, os
a, cfg, table, vpc, agents, day = sys.argv[1:]
s = open(cfg).read()
open(cfg, "w").write(re.sub(rf"\n*Host {re.escape(a)}\n(?:[ \t]+.*\n?)*", "\n", s))
L = [l for l in open(table, encoding="utf-8").read().split("\n") if not l.startswith(f"| [{a}]")]
open(table, "w", encoding="utf-8").write("\n".join(L))
if os.path.exists(vpc):
    L = [l for l in open(vpc).read().split("\n") if not l.startswith(a + "\t")]
    open(vpc, "w").write("\n".join(L))
if os.path.exists(agents):
    s = open(agents, encoding="utf-8").read()
    if not s.startswith("> **已于"):
        open(agents, "w", encoding="utf-8").write(f"> **已于 {day} 删除，只作记录。**\n\n" + s)
PY
  [[ "$ip" =~ ^[0-9.]+$ ]] && ssh-keygen -R "$ip" >/dev/null 2>&1 || true
  echo "已撤登记 $a（$ip）"
done
gen="$root/ops-local/gitleaks/gen-private.sh"; [ -x "$gen" ] && bash "$gen" >/dev/null || true

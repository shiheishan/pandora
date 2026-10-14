#!/usr/bin/env bash
# 给已有测试机挂 VPC（不重启，约 20 秒后 enp8s0 自动有地址，cloud-init 已持久化）：vultr-attach-vpc.sh <别名>...
# 改的是用户的机器：用户在对话里同意后再跑。挂完把内网地址补进总表、AGENTS.md、ops-local/vpc-hosts.tsv。
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"; . "$here/vultr-lib.sh"
root="$(dirname "$(git -C "$here" rev-parse --path-format=absolute --git-common-dir)")"; . "$root/ops-local/vultr/env"
tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
jq -n --arg v "$VULTR_VPC_ID" '{vpc_id:$v}' > "$tmp"
for a in "$@"; do
  read -r id _ _ _ < <(instance_of "$a")
  vapi POST "/instances/$id/vpcs/attach" "$tmp" >/dev/null
  pip=""; for _ in $(seq 1 12); do
    pip="$(vapi GET "/instances/$id/vpcs" | jq -r '.vpcs[0].ip_address // empty')"
    [ -n "$pip" ] && ssh -n -o BatchMode=yes "$a" "ip -o -4 addr | grep -q ' $pip/'" 2>/dev/null && break; sleep 5
  done
  [ -n "$pip" ] || { echo "$a：一分钟内没拿到内网地址" >&2; continue; }
  python3 - "$HOME/ai/servers/README.md" "$a" "$pip" <<'PY'
import sys
p, a, ip = sys.argv[1:]
L = open(p, encoding="utf-8").read().split("\n")
for i, l in enumerate(L):
    if l.startswith(f"| [{a}]") and "内网" not in l:
        c = l.split(" | "); c[1] += f"（内网 {ip}）"; L[i] = " | ".join(c)
open(p, "w", encoding="utf-8").write("\n".join(L))
PY
  grep -q '内网' "$HOME/ai/servers/$a/AGENTS.md" || printf '\n- 内网 IP（VPC，%s 挂上，网卡 enp8s0）：%s。测试流量走内网。\n' "$(date -u +%F)" "$pip" >> "$HOME/ai/servers/$a/AGENTS.md"
  grep -q "^$a	" "$root/ops-local/vpc-hosts.tsv" 2>/dev/null || printf '%s\t%s\t%s\n' "$a" "$pip" "$(date -u +%F)" >> "$root/ops-local/vpc-hosts.tsv"
  echo "$a：内网 $pip"
done

#!/usr/bin/env bash
# 登记一台一次性测试机：首次连接、~/.ssh/config、~/ai/servers/<别名>/、总表一行、机器上 /root/README.md、装 chrony。
# 用法：register.sh [--vpc <内网IP>] <别名> <IP> <套餐> <用途一句话> [日期，默认今天 UTC]
#   用途只写一句话、不带日期：总表里脚本自己补「（一次性，日期）」；误带了结尾的「（一次性…）」会先去掉，免得写两遍。
#   --vpc：开机勾了同机房 VPC 时给内网地址。写进 ~/ai/servers/<别名>/AGENTS.md、总表的 IP 列和 ops-local/vpc-hosts.tsv；
#          ~/.ssh/config 仍走公网地址（ssh 从本机连）。测试流量用内网地址，见 node-accept skill。
#   结果目录占位 <目录> 取环境变量 RESULTS_DIR（默认 vultr-test2）。
#   例：register.sh --vpc <内网IP> vultr-sgp-pt-node5 <IP> vc2-1c-1gb "pandora 真节点验证，接 panel2"
# 用户发来 IP 即视为同意登记（根 CLAUDE.md）。IP 只落在 ~/.ssh/config 与 ~/ai/servers/，不进仓库。
# 必须用 bash 跑（脚本用了 bash 语法，不要在 zsh 里 source）。
set -euo pipefail
vpc=""
if [ "${1:-}" = --vpc ]; then vpc="${2:?--vpc 后面要跟内网地址}"; shift 2; fi
alias="${1:?别名}"; ip="${2:?IP}"; plan="${3:?套餐}"; purpose="${4:?用途}"; day="${5:-$(date -u +%F)}"
[[ "$alias" =~ ^[a-z0-9-]+$ ]] || { echo "别名只用小写字母、数字、连字符：$alias" >&2; exit 2; }
[[ "$ip" =~ ^[0-9.]+$|: ]] || { echo "IP 不像 IP：$ip" >&2; exit 2; }
[ -z "$vpc" ] || [[ "$vpc" =~ ^(10\.|172\.(1[6-9]|2[0-9]|3[01])\.|192\.168\.)[0-9.]+$ ]] || { echo "内网地址不像 RFC1918 私网地址：$vpc" >&2; exit 2; }
# 用途不带日期：去掉调用方误加在结尾的「（一次性…）」，日期由下面的总表行统一补
purpose="${purpose%（一次性*）}"
vpc_text="${vpc:-无（没勾 VPC）}"
here="$(cd "$(dirname "$0")/.." && pwd)"
servers="$HOME/ai/servers"; dir="$servers/$alias"
[ -e "$dir" ] && { echo "已存在：$dir（删过的旧机目录保留作记录，换个序号）" >&2; exit 1; }
grep -qE "^Host[[:space:]]+$alias\$" "$HOME/.ssh/config" && { echo "~/.ssh/config 已有 Host $alias" >&2; exit 1; }

# 1. 首次连接（同 IP 重装过会换主机密钥，先清掉旧的）；刚开机常 banner 超时，重试 3 次
ssh-keygen -R "$ip" >/dev/null 2>&1 || true
for i in 1 2 3; do
  ssh -o StrictHostKeyChecking=accept-new -o BatchMode=yes -o ConnectTimeout=15 "root@$ip" true && break
  [ "$i" = 3 ] && { echo "连不上 root@$ip（1Password agent 锁着？机器没起来？）" >&2; exit 1; }
  sleep 15
done

# 2. ~/.ssh/config
printf '\nHost %s\n    HostName %s\n    User root\n' "$alias" "$ip" >> "$HOME/.ssh/config"

# 3. ~/ai/servers/<别名>/
mkdir -p "$dir/backups" "$dir/tools"
echo '@AGENTS.md' > "$dir/CLAUDE.md"
sed -e "s|<别名>|$alias|g" -e "s|<IP>|$ip|g" -e "s|<套餐>|$plan|g" -e "s|<日期>|$day|g" \
    -e "s|<一句话：pandora 的哪项测试、装什么、和哪台配合>|$purpose|g" -e "s|<目录>|${RESULTS_DIR:-vultr-test2}|g" \
    -e "s|<内网IP>|$vpc_text|g" \
    "$here/templates/AGENTS.md" > "$dir/AGENTS.md"
if grep -q '<[^>]*>' "$dir/AGENTS.md"; then echo "注意：AGENTS.md 还有没替换的占位：$(grep -o '<[^>]*>' "$dir/AGENTS.md" | sort -u | tr '\n' ' ')"; fi

# 4. 总表加一行（接在最后一个表格行后面）
python3 - "$servers/README.md" "$alias" "$ip" "$purpose" "$day" "$vpc" <<'PY'
import sys
p, alias, ip, purpose, day, vpc = sys.argv[1:]
lines = open(p, encoding="utf-8").read().split("\n")
last = max(i for i, l in enumerate(lines) if l.startswith("| ["))
addr = f"{ip}（内网 {vpc}）" if vpc else ip
row = f"| [{alias}]({alias}/AGENTS.md) | {addr} | Vultr / 新加坡 | {purpose}（一次性，{day}） | `ssh {alias}`，只认公钥（Vultr 注入） |"
lines.insert(last + 1, row)
open(p, "w", encoding="utf-8").write("\n".join(lines))
PY

# 4b. 内网地址另记一份到主目录 ops-local（被 git 忽略），压测脚本从这里生成 ips.env
main_root="$(dirname "$(git -C "$here" rev-parse --path-format=absolute --git-common-dir 2>/dev/null)")"
if [ -n "$vpc" ]; then
  if [ -d "$main_root/ops-local" ]; then
    printf '%s\t%s\t%s\n' "$alias" "$vpc" "$day" >> "$main_root/ops-local/vpc-hosts.tsv"
  else
    echo "提醒：没找到 $main_root/ops-local，内网地址没记进 vpc-hosts.tsv"
  fi
fi

# 5. 机器上：chrony、/root/README.md（只写位置不写值）
ssh -o BatchMode=yes "$alias" bash -s -- "$alias" "$day" "$plan" "$purpose" "$vpc" <<'REMOTE'
set -e
alias="$1"; day="$2"; plan="$3"; purpose="$4"; vpc="${5:-}"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq >/dev/null && apt-get install -y -qq chrony >/dev/null
cat > /root/README.md <<R
# $alias（一次性测试机）

- 用途：$purpose
- 开机：$day，Vultr 新加坡 $plan，$(. /etc/os-release; echo "$PRETTY_NAME")。用完由用户在 Vultr 控制台删除。
- 已装：chrony。之后装的东西、容器与端口、结果目录、口令文件位置（只写位置不写值）都补在这里。
R
if [ -n "$vpc" ]; then
  nic="$(ip -o -4 addr show 2>/dev/null | awk -v a="$vpc" 'index($4, a"/")==1 {print $2; exit}')"
  echo "- 内网（VPC）：$vpc，网卡 ${nic:-未配置}。测试流量走这个地址。" >> /root/README.md
  [ -n "$nic" ] || echo "注意：机器上还没有 $vpc 这个地址（VPC 网卡没起来？到 Vultr 控制台看该机的 VPC 设置）"
fi
echo "chrony: $(systemctl is-active chrony)；ufw: $(ufw status 2>/dev/null | head -1 || echo 无)；$(nproc) 核 $(free -m | awk '/Mem:/{print $2}')MB"
REMOTE
echo "已登记 $alias：~/.ssh/config、$dir、总表、机器 /root/README.md${vpc:+、ops-local/vpc-hosts.tsv（内网 $vpc）}"
# 新 IP 进私有 gitleaks 规则（提交前拦真实 IP），见根 CLAUDE.md「红线：仓库公开」
gen="$(git -C "$here" rev-parse --show-toplevel 2>/dev/null)/ops-local/gitleaks/gen-private.sh"
if [[ -x "$gen" ]]; then bash "$gen"; else echo "提醒：没找到 $gen，私有 gitleaks 规则没更新"; fi

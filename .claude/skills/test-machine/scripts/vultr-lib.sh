# 被 vultr-*.sh source。别名 → 实例 id：按 ~/.ssh/config 里的公网地址对 main_ip（老机器的 Vultr 标签不是别名）。
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
vapi() { bash "$here/vultr.sh" "$@"; }
alias_ip() { ssh -G "$1" 2>/dev/null | awk '$1=="hostname"{print $2; exit}'; }
instance_of() { # 输出：id label main_ip plan
  local ip; ip="$(alias_ip "$1")"
  [[ "$ip" =~ ^[0-9.]+$ ]] || { echo "~/.ssh/config 里没有 $1" >&2; return 1; }
  vapi GET '/instances?per_page=500' | jq -r --arg ip "$ip" '.instances[] | select(.main_ip==$ip) | "\(.id) \(.label) \(.main_ip) \(.plan)"' | grep . \
    || { echo "账户里没有公网地址为 $ip 的实例" >&2; return 1; }
}

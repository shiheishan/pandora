#!/usr/bin/env bash
# 在 Debian 测试机上装与 CI 同小版本的 Go 与 Node 22（官方包，核 sha256），外加 git。
# 用法（在本机）：ssh <别名> 'GO_SERIES=1.26 NODE_MAJOR=22 bash -s' < install-toolchain.sh
set -euo pipefail
GO_SERIES="${GO_SERIES:-1.26}"
NODE_MAJOR="${NODE_MAJOR:-22}"
export DEBIAN_FRONTEND=noninteractive
# 全新镜像的包索引是空的或过期的，不先 update 会找不到包
apt-get update -qq >/dev/null
apt-get install -y -qq curl ca-certificates git xz-utils python3 >/dev/null
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT; cd "$work"

read -r go_ver go_file go_sum < <(curl -fsSL 'https://go.dev/dl/?mode=json&include=all' | python3 -c "
import json,sys
d=json.load(sys.stdin)
v=[r for r in d if r['version'].startswith('go${GO_SERIES}.')][0]
f=[x for x in v['files'] if x['os']=='linux' and x['arch']=='amd64' and x['kind']=='archive'][0]
print(v['version'], f['filename'], f['sha256'])")
curl -fsSLo "$go_file" "https://go.dev/dl/$go_file"
echo "$go_sum  $go_file" | sha256sum -c - >/dev/null
rm -rf /usr/local/go && tar -C /usr/local -xzf "$go_file"

node_ver="$(curl -fsSL https://nodejs.org/dist/index.json | python3 -c "
import json,sys
print([r['version'] for r in json.load(sys.stdin) if r['version'].startswith('v${NODE_MAJOR}.')][0])")"
node_file="node-$node_ver-linux-x64.tar.xz"
curl -fsSLO "https://nodejs.org/dist/$node_ver/SHASUMS256.txt"
curl -fsSLO "https://nodejs.org/dist/$node_ver/$node_file"
grep " $node_file\$" SHASUMS256.txt | sha256sum -c - >/dev/null
rm -rf /usr/local/node && mkdir -p /usr/local/node && tar -C /usr/local/node --strip-components=1 -xJf "$node_file"

grep -q '/usr/local/go/bin' /root/.profile 2>/dev/null || echo 'export PATH=/usr/local/go/bin:/usr/local/node/bin:$PATH' >> /root/.profile
echo "go $go_ver ($( /usr/local/go/bin/go version | awk '{print $3}' )), node $(/usr/local/node/bin/node --version), $(git --version)"

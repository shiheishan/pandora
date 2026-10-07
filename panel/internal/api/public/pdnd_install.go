package public

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/platform/config"
)

// Pandora NativeCore 的一键安装分发。
//
// # 为什么不用 GitHub Release
//
// 代码仓库是私有的，私有仓库的 Release 附件必须带 token 才能下载。把 token
// 写进安装脚本等于把仓库读权限散给每一台落地机，一旦某台机器被拿下，整个
// 代码库跟着泄。二进制由面板自己分发，落地机只需要面板地址，不需要任何
// GitHub 凭据。
//
// # 路由位置
//
// 挂在 /pdnd 下，是字面量路由，不会被订阅那条 /{prefix}/{token} 通配吃掉
// （chi 里字面量优先于通配符）。

// pdndDistDir 是二进制的存放目录（config 的 PdndDistDir）。
//
// 之所以允许用环境变量覆盖：面板可能装在不同的路径下，而这个目录要跟着
// 发布产物走，不适合写死在配置表里——配置表是运营改的，这个是部署时定的。
func (h *handlers) pdndDistDir() string {
	return h.d.Cfg.PdndDistDir
}

// pdndPanelBaseURL 推导落地机该回连的面板地址。
//
// 面板挂在反代后面，r.TLS 和 r.Host 反映不出用户实际访问的入口，所以优先
// 信反代给的 X-Forwarded-*。推错的后果很直接：脚本装到机器上，连的是内网
// 地址，接不进来。
func pdndPanelBaseURL(cfgURL string, production bool) (string, error) {
	env := "development"
	if production {
		env = "production"
	}
	return (&config.Config{Env: env, PublicBaseURL: cfgURL}).CanonicalPublicOrigin()
}

// pdndInstallScript 返回落地机执行的安装脚本。
//
// 脚本自己不含任何密钥：node_id 和 token 由管理员在调用时用参数传进来，
// 这样脚本本身可以公开缓存，泄漏了也不构成凭据泄漏。
func (h *handlers) pdndInstallScript(w http.ResponseWriter, r *http.Request) {
	panelURL, err := h.d.Cfg.CanonicalPublicOrigin()
	if err != nil {
		http.Error(w, "installer origin is not configured safely", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, strings.ReplaceAll(pdndInstallTemplate, "@@PANEL@@", panelURL))
}

// pdndBinary 提供发布产物。文件名固定为 pandora-native-linux-{amd64,arm64}，
// 不接受任意路径——这是个公开端点，拼路径就是任意文件读取。
func (h *handlers) pdndBinary(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !pdndAllowedArtifact(name) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(h.pdndDistDir(), name)
	file, err := os.Open(path)
	if err != nil {
		h.d.Log.Warn("pdnd 发布产物不可读", "artifact", name, "err", err)
		http.Error(w, "artifact unavailable", http.StatusNotFound)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	http.ServeContent(w, r, name, info.ModTime(), file)
}

// pdndChecksum 返回单个产物的 SHA256。
//
// 安装脚本下载完要自己核对：面板走 HTTPS，但中间还有 CDN 和落地机本地的
// 缓存，校验和是唯一能证明「拿到的就是我们发的那个」的东西。
func (h *handlers) pdndChecksum(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !pdndAllowedArtifact(name) {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(filepath.Join(h.pdndDistDir(), name))
	if err != nil {
		http.Error(w, "artifact unavailable", http.StatusNotFound)
		return
	}
	defer file.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		http.Error(w, "checksum failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum.Sum(nil)), name)
}

// pdndAllowedArtifact 是产物白名单。只认这两个确切的名字。
func pdndAllowedArtifact(name string) bool {
	switch name {
	case "pandora-native-linux-amd64", "pandora-native-linux-arm64":
		return true
	}
	return false
}

// pdndSysctlFunction 是安装脚本里调系统参数的那段 shell 函数，单独成段好在测试里
// 只跑它（不需要 root、不碰本机）。
//
// 依据是节点容量实测（10 万连接、1c1g 单位数据）：重启风暴时 conntrack 峰值 31 万，
// 超过 Debian 默认的 262144，1c1g 镜像默认只有 8192、约 4000 条连接就静默丢包；
// TIME_WAIT 顶到 32768；QUIC 需要 7.5MB 以上的收发缓冲。写成持久的 sysctl.d
// drop-in，只调大不调小：机器上已有更大的值按原值写回。conntrack 模块晚于
// systemd-sysctl 加载时，udev 会对 net.netfilter 前缀再应用一次 sysctl.d；
// hashsize 只能走模块参数，另写一份 modprobe.d。
const pdndSysctlFunction = `
pandora_tune_sysctl() {
  SYSCTL_DIR="${PANDORA_SYSCTL_DIR:-/etc/sysctl.d}"
  MODPROBE_DIR="${PANDORA_MODPROBE_DIR:-/etc/modprobe.d}"
  HASHSIZE_PARAM="${PANDORA_CONNTRACK_HASHSIZE_PATH:-/sys/module/nf_conntrack/parameters/hashsize}"
  if ! command -v sysctl >/dev/null 2>&1; then
    echo "==> 跳过系统参数：找不到 sysctl" >&2
    return 0
  fi
  mkdir -p "$SYSCTL_DIR" "$MODPROBE_DIR"
  DROPIN="${SYSCTL_DIR}/90-pandora-native.conf"
  echo "# 由 pandora-native 安装器生成：只调大不调小，重装时按当时的取值重算。" > "${DROPIN}.new"
  for pair in net.netfilter.nf_conntrack_max=524288 net.ipv4.tcp_max_tw_buckets=262144 \
      net.core.somaxconn=65535 net.ipv4.tcp_max_syn_backlog=65535 \
      net.core.rmem_max=16777216 net.core.wmem_max=16777216 net.core.netdev_max_backlog=16384; do
    key="${pair%%=*}"
    want="${pair#*=}"
    cur="$(sysctl -n "$key" 2>/dev/null || true)"
    case "$cur" in ''|*[!0-9]*) cur=0 ;; esac
    if [ "$cur" -gt "$want" ]; then want="$cur"; fi
    echo "$key = $want" >> "${DROPIN}.new"
  done
  # 本地端口范围只放宽不收窄。
  range="$(sysctl -n net.ipv4.ip_local_port_range 2>/dev/null || true)"
  low="$(echo "$range" | awk '{print $1}')"
  high="$(echo "$range" | awk '{print $2}')"
  case "$low" in ''|*[!0-9]*) low=1024 ;; esac
  case "$high" in ''|*[!0-9]*) high=65535 ;; esac
  if [ "$low" -gt 1024 ]; then low=1024; fi
  if [ "$high" -lt 65535 ]; then high=65535; fi
  echo "net.ipv4.ip_local_port_range = $low $high" >> "${DROPIN}.new"
  # tcp_tw_reuse：1 是对外连接也复用；默认 2 只管回环。
  echo "net.ipv4.tcp_tw_reuse = 1" >> "${DROPIN}.new"
  chmod 0644 "${DROPIN}.new"
  mv -f "${DROPIN}.new" "$DROPIN"
  hashsize=131072
  cur="$(cat "$HASHSIZE_PARAM" 2>/dev/null || true)"
  case "$cur" in ''|*[!0-9]*) cur=0 ;; esac
  if [ "$cur" -gt "$hashsize" ]; then hashsize="$cur"; fi
  echo "options nf_conntrack hashsize=${hashsize}" > "${MODPROBE_DIR}/pandora-native.conf"
  chmod 0644 "${MODPROBE_DIR}/pandora-native.conf"
  if [ "$cur" -gt 0 ] && [ "$cur" -lt "$hashsize" ] && [ -w "$HASHSIZE_PARAM" ]; then
    echo "$hashsize" > "$HASHSIZE_PARAM" 2>/dev/null || true
  fi
  if ! sysctl -e -p "$DROPIN" >/dev/null 2>&1; then
    echo "==> 部分系统参数没能立即生效（容器或只读 /proc/sys），已写入 ${DROPIN}，重启后生效" >&2
  fi
  echo "==> 系统参数已写入 ${DROPIN}"
}
`

// pdndInstallTemplate 是安装脚本本体。@@PANEL@@ 在下发时替换成面板地址，
// 这样落地机上的人不用再手填一次域名——填错域名的排查成本比这行替换高得多。
const pdndInstallTemplate = `#!/bin/sh
# Pandora NativeCore 一键安装。
#
#   curl -fsSL @@PANEL@@/pdnd/install.sh | sh -s -- \
#     --token <bootstrap token> --name <server/node name>
#
# 幂等：重复执行会覆盖配置并重启服务，不会重复添加 systemd 单元。
set -eu

PANEL="@@PANEL@@"
NODE_ID=""
TOKEN=""
TOKEN_FILE=""
NODE_NAME=""
RUNTIME_TOKEN=""
SIGNED_REQUIRED=false
NODE_TYPE="vless"
INSTALL_DIR="${PANDORA_INSTALL_DIR:-/usr/local/bin}"
CONFIG_DIR="${PANDORA_CONFIG_DIR:-/etc/pandora-native}"
UNIT_DIR="${PANDORA_UNIT_DIR:-/etc/systemd/system}"
SERVICE="pandora-native"
` + pdndSysctlFunction + `
while [ $# -gt 0 ]; do
  case "$1" in
    --token) TOKEN="$2"; shift 2 ;;
    --token-file) TOKEN_FILE="$2"; shift 2 ;;
    --name) NODE_NAME="$2"; shift 2 ;;
    --node-id) NODE_ID="$2"; shift 2 ;;
    --node-type) NODE_TYPE="$2"; shift 2 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
done

[ -z "$TOKEN" ] || [ -z "$TOKEN_FILE" ] || { echo "--token 与 --token-file 不能同时使用" >&2; exit 2; }
if [ -n "$TOKEN_FILE" ]; then
  [ -f "$TOKEN_FILE" ] && [ -r "$TOKEN_FILE" ] || { echo "token file 不可读" >&2; exit 2; }
  TOKEN="$(cat "$TOKEN_FILE")"
fi
[ -n "$TOKEN" ]   || { echo "缺少 --token 或 --token-file" >&2; exit 2; }
case "$PANEL" in
  https://*) CURL_PROTO='=https' ;;
  http://127.0.0.1:*|http://localhost:*|http://\[::1\]:*) CURL_PROTO='=http' ;;
  *) echo "panel origin must use HTTPS" >&2; exit 2 ;;
esac
if [ -z "$NODE_NAME" ] && [ -z "$NODE_ID" ]; then
  echo "缺少 --name 或 --node-id" >&2
  exit 2
fi

[ "$(id -u)" = "0" ] || { echo "需要 root 权限" >&2; exit 1; }

for required in getent groupadd useradd chown chmod install; do
  command -v "$required" >/dev/null 2>&1 || { echo "missing dependency: $required" >&2; exit 1; }
done
if ! getent group pandora >/dev/null 2>&1; then
  groupadd --system pandora
fi
if ! getent passwd pandora >/dev/null 2>&1; then
  useradd --system --gid pandora --home-dir /var/lib/pandora-native \
    --create-home --shell /usr/sbin/nologin pandora
fi

case "$(uname -m)" in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "不支持的架构: $(uname -m)（仅 amd64 / arm64）" >&2; exit 1 ;;
esac
ARTIFACT="pandora-native-linux-${ARCH}"

echo "==> 下载 ${ARTIFACT}"
TMP="$(mktemp -d)"
COMMITTED=0
ENROLLMENT_PENDING=0
SERVER_COMMITTED=0
HAD_BINARY=0
HAD_CONFIG=0
HAD_IDENTITY=0
HAD_JOURNAL=0
HAD_UNIT=0
WAS_ACTIVE=0
WAS_ENABLED=0
rollback_install() {
  echo "==> install failed; restoring previous Pandora NativeCore release" >&2
  if [ "$HAD_BINARY" = 1 ]; then
    install -m 0755 "$TMP/previous.binary" "${INSTALL_DIR}/${SERVICE}"
  else
    rm -f "${INSTALL_DIR}/${SERVICE}"
  fi
  if [ "$HAD_CONFIG" = 1 ]; then
    install -o root -g pandora -m 0640 "$TMP/previous.config" "${CONFIG_DIR}/config.json"
  else
    rm -f "${CONFIG_DIR}/config.json"
  fi
  if [ "$HAD_IDENTITY" = 1 ]; then
    install -o root -g pandora -m 0640 "$TMP/previous.identity" "${CONFIG_DIR}/identity.json"
  else
    rm -f "${CONFIG_DIR}/identity.json"
  fi
  if [ "$HAD_JOURNAL" = 1 ]; then
    install -o root -g root -m 0600 "$TMP/previous.enrollment" "${CONFIG_DIR}/identity.json.enrollment.pending.json"
  else
    rm -f "${CONFIG_DIR}/identity.json.enrollment.pending.json"
  fi
  if [ "$HAD_UNIT" = 1 ]; then
    install -m 0644 "$TMP/previous.unit" "${UNIT_DIR}/${SERVICE}.service"
  else
    rm -f "${UNIT_DIR}/${SERVICE}.service"
  fi
  systemctl daemon-reload >/dev/null 2>&1 || true
  if [ "$WAS_ACTIVE" = 1 ] && [ "$HAD_UNIT" = 1 ]; then
    systemctl restart "${SERVICE}" >/dev/null 2>&1 || true
  else
    systemctl stop "${SERVICE}" >/dev/null 2>&1 || true
  fi
  if [ "$WAS_ENABLED" = 1 ] && [ "$HAD_UNIT" = 1 ]; then
    systemctl enable "${SERVICE}" >/dev/null 2>&1 || true
  else
    systemctl disable "${SERVICE}" >/dev/null 2>&1 || true
  fi
}
finish_install() {
  rc=$?
  trap - EXIT HUP INT TERM
  if [ "$rc" -ne 0 ] && [ "$COMMITTED" -ne 1 ]; then
    if [ "$SERVER_COMMITTED" = 1 ] || { [ -f "${CONFIG_DIR}/identity.json.enrollment.pending.json" ] && grep -q '"commit_attempted"[[:space:]]*:[[:space:]]*true' "${CONFIG_DIR}/identity.json.enrollment.pending.json"; }; then
      echo "==> server committed enrollment; preserving staged files for roll-forward recovery" >&2
    else
      if [ "$ENROLLMENT_PENDING" = 1 ]; then
        "${INSTALL_DIR}/${SERVICE}" enrollment abort --identity "${CONFIG_DIR}/identity.json" \
          --reason "installer failed before commit" >/dev/null 2>&1 || true
      fi
      rollback_install
    fi
  fi
  rm -rf "$TMP"
  exit "$rc"
}
trap finish_install EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM
curl --fail --silent --show-error --proto "$CURL_PROTO" --max-redirs 0 \
  "${PANEL}/pdnd/bin/${ARTIFACT}" -o "${TMP}/${ARTIFACT}"

# 校验和：HTTPS 之外再确认一次拿到的就是面板发的那个。
echo "==> 校验完整性"
EXPECTED="$(curl --fail --silent --show-error --proto "$CURL_PROTO" --max-redirs 0 \
  "${PANEL}/pdnd/sha256/${ARTIFACT}" | awk '{print $1}')"
ACTUAL="$(sha256sum "${TMP}/${ARTIFACT}" | awk '{print $1}')"
if [ "$EXPECTED" != "$ACTUAL" ]; then
  echo "校验和不匹配：期望 ${EXPECTED}，实际 ${ACTUAL}" >&2
  exit 1
fi

echo "==> 安装到 ${INSTALL_DIR}/${SERVICE}"
if [ -f "${INSTALL_DIR}/${SERVICE}" ]; then
  HAD_BINARY=1
  cp -p "${INSTALL_DIR}/${SERVICE}" "$TMP/previous.binary"
fi
if [ -f "${CONFIG_DIR}/config.json" ]; then
  HAD_CONFIG=1
  cp -p "${CONFIG_DIR}/config.json" "$TMP/previous.config"
fi
if [ -f "${CONFIG_DIR}/identity.json" ]; then
  HAD_IDENTITY=1
  cp -p "${CONFIG_DIR}/identity.json" "$TMP/previous.identity"
fi
if [ -f "${CONFIG_DIR}/identity.json.enrollment.pending.json" ]; then
  HAD_JOURNAL=1
  cp -p "${CONFIG_DIR}/identity.json.enrollment.pending.json" "$TMP/previous.enrollment"
fi
if [ -f "${UNIT_DIR}/${SERVICE}.service" ]; then
  HAD_UNIT=1
  cp -p "${UNIT_DIR}/${SERVICE}.service" "$TMP/previous.unit"
fi
if systemctl is-active --quiet "${SERVICE}" 2>/dev/null; then
  WAS_ACTIVE=1
fi
if systemctl is-enabled --quiet "${SERVICE}" 2>/dev/null; then
  WAS_ENABLED=1
fi
install -m 0755 "${TMP}/${ARTIFACT}" "${INSTALL_DIR}/${SERVICE}.new"
mv -f "${INSTALL_DIR}/${SERVICE}.new" "${INSTALL_DIR}/${SERVICE}"

install -d -o root -g pandora -m 0750 "$CONFIG_DIR"
if [ -z "$NODE_ID" ]; then
  echo "==> 注册 Pandora NativeCore 身份"
  if [ "$HAD_IDENTITY" = 1 ]; then
    echo "==> reusing the existing Pandora NativeCore identity"
  else
    printf '%s' "$TOKEN" > "$TMP/bootstrap.token"
    chmod 0600 "$TMP/bootstrap.token"
    "${INSTALL_DIR}/${SERVICE}" enrollment begin --server "$PANEL" --token-file "$TMP/bootstrap.token" \
      --name "$NODE_NAME" --identity "${CONFIG_DIR}/identity.json"
    ENROLLMENT_PENDING=1
  fi
  IDENTITY_SOURCE="${CONFIG_DIR}/identity.json"
  if [ "$ENROLLMENT_PENDING" = 1 ]; then
    IDENTITY_SOURCE="${CONFIG_DIR}/identity.json.enrollment.pending.json"
  fi
  NODE_ID="$(sed -n 's/.*"node_id": *"\([^"]*\)".*/\1/p' "$IDENTITY_SOURCE")"
  RUNTIME_TOKEN="$(sed -n 's/.*"runtime_token": *"\([^"]*\)".*/\1/p' "$IDENTITY_SOURCE")"
  [ -n "$NODE_ID" ] || { echo "bootstrap 未返回 node_id" >&2; exit 1; }
  [ -n "$RUNTIME_TOKEN" ] || { echo "bootstrap 未返回 runtime_token" >&2; exit 1; }
  SIGNED_REQUIRED=true
else
  # 兼容接入：此时 --token 已经是 UniProxy 运行令牌。
  RUNTIME_TOKEN="$TOKEN"
fi

echo "==> 写入配置 ${CONFIG_DIR}/config.json"
install -d -o root -g pandora -m 0750 "$CONFIG_DIR"
# 配置里有节点令牌，只给 root 读。
umask 077
cat > "${CONFIG_DIR}/config.json.new" <<JSON
{
  "panel": {
    "url": "${PANEL}",
    "identity_path": "${CONFIG_DIR}/identity.json",
    "signed_required": ${SIGNED_REQUIRED},
    "timeout_seconds": 10
  },
  "nodes": [
    {
      "node_id": "${NODE_ID}",
      "node_type": "${NODE_TYPE}",
      "token": "${RUNTIME_TOKEN}",
      "identity_path": "${CONFIG_DIR}/identity.json"
    }
  ]
}
JSON
chown root:pandora "${CONFIG_DIR}/config.json.new"
chmod 0640 "${CONFIG_DIR}/config.json.new"
mv -f "${CONFIG_DIR}/config.json.new" "${CONFIG_DIR}/config.json"
[ ! -f "${CONFIG_DIR}/identity.json" ] || { chown root:pandora "${CONFIG_DIR}/identity.json"; chmod 0640 "${CONFIG_DIR}/identity.json"; }
[ ! -f "${CONFIG_DIR}/identity.json.enrollment.pending.json" ] || {
  chown root:root "${CONFIG_DIR}/identity.json.enrollment.pending.json"
  chmod 0600 "${CONFIG_DIR}/identity.json.enrollment.pending.json"
}
umask 022

echo "==> 注册 systemd 服务"
mkdir -p "${UNIT_DIR}"
cat > "${UNIT_DIR}/${SERVICE}.service.new" <<UNIT
[Unit]
Description=Pandora NativeCore node agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=pandora
Group=pandora
ExecStart=${INSTALL_DIR}/${SERVICE} -c ${CONFIG_DIR}/config.json
Restart=on-failure
RestartSec=5s
KillSignal=SIGTERM
TimeoutStopSec=30s
NoNewPrivileges=true
UMask=0077
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ProtectClock=true
ProtectHostname=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictNamespaces=true
SystemCallArchitectures=native
RestrictSUIDSGID=true
RestrictRealtime=true
LockPersonality=true
MemoryDenyWriteExecute=true
StateDirectory=pandora-native
LogsDirectory=pandora-native
ReadWritePaths=/var/lib/pandora-native /var/log/pandora-native
LimitNOFILE=1048576
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
UNIT
chmod 0644 "${UNIT_DIR}/${SERVICE}.service.new"
mv -f "${UNIT_DIR}/${SERVICE}.service.new" "${UNIT_DIR}/${SERVICE}.service"

if [ "$ENROLLMENT_PENDING" = 1 ]; then
  echo "==> preflighting and committing the node enrollment"
  PREFLIGHT_SHA="$("${INSTALL_DIR}/${SERVICE}" validate-install --config "${CONFIG_DIR}/config.json" \
    --identity "${CONFIG_DIR}/identity.json" --unit "${UNIT_DIR}/${SERVICE}.service" | sha256sum | awk '{print $1}')"
  BINARY_SHA="$(sha256sum "${INSTALL_DIR}/${SERVICE}" | awk '{print $1}')"
  CONFIG_SHA="$(sha256sum "${CONFIG_DIR}/config.json" | awk '{print $1}')"
  UNIT_SHA="$(sha256sum "${UNIT_DIR}/${SERVICE}.service" | awk '{print $1}')"
  if "${INSTALL_DIR}/${SERVICE}" enrollment commit --identity "${CONFIG_DIR}/identity.json" \
      --binary-sha256 "$BINARY_SHA" --config-sha256 "$CONFIG_SHA" \
      --unit-sha256 "$UNIT_SHA" --preflight-sha256 "$PREFLIGHT_SHA"; then
    SERVER_COMMITTED=1
  else
    echo "==> commit response uncertain; resolving enrollment status" >&2
    STATUS_OUTPUT="$("${INSTALL_DIR}/${SERVICE}" enrollment status --identity "${CONFIG_DIR}/identity.json" 2>&1 || true)"
    printf '%s\n' "$STATUS_OUTPUT" >&2
    if printf '%s\n' "$STATUS_OUTPUT" | grep -qx 'enrollment state: committed'; then
      SERVER_COMMITTED=1
      [ -f "${CONFIG_DIR}/identity.json" ] || exit 1
    else
      exit 1
    fi
  fi
  chown root:pandora "${CONFIG_DIR}/identity.json"
  chmod 0640 "${CONFIG_DIR}/identity.json"
fi

echo "==> 调整系统参数（conntrack、监听队列、端口范围、收发缓冲）"
pandora_tune_sysctl || echo "==> 系统参数调整失败，不影响安装，可稍后手动处理" >&2

if [ "$SIGNED_REQUIRED" = true ]; then
  echo "==> verifying the signed node identity with the control plane"
  "${INSTALL_DIR}/${SERVICE}" verify-identity \
    --identity "${CONFIG_DIR}/identity.json" --node-id "$NODE_ID" --expected-server "$PANEL"
fi

systemctl daemon-reload
systemctl enable "${SERVICE}" >/dev/null
systemctl restart "${SERVICE}"

echo "==> 等待就绪"
i=0
while [ $i -lt 15 ]; do
  if systemctl is-active --quiet "${SERVICE}"; then
    rm -f "${CONFIG_DIR}/identity.json.enrollment.pending.json"
    COMMITTED=1
    echo "安装完成。服务已运行。"
    echo "  日志：journalctl -u ${SERVICE} -f"
    exit 0
  fi
  i=$((i + 1))
  sleep 1
done

echo "服务未能进入运行状态，最近日志：" >&2
journalctl -u "${SERVICE}" -n 30 --no-pager >&2 || true
exit 1
`

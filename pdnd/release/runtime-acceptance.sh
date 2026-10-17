#!/usr/bin/env bash
set -Eeuo pipefail

# Exercise the actual release binary without touching system paths or services.
# Usage: runtime-acceptance.sh <pandora-native-binary> <stage-dir>
#
# Against a loopback mock gRPC panel (release/acceptancepanel):
#   1. `enroll` with a wrong CA fingerprint must fail before the token is sent
#      (the panel sees no enrollment);
#   2. `enroll` with the panel's real fingerprint must write the identity dir
#      (ca.pem cert.pem key.pem server_id, key 0600, no leftover enroll.json);
#   3. two cold starts: the first must show a heartbeat with host metrics, a
#      health_passed config ack and an ok users ack; the second starts from the
#      disk cache, so its Hello reports the applied versions and the panel does
#      not resend them;
#   4. stop the panel and cold start once more: the node must serve from its
#      disk cache.
# SIGTERM and port release are checked every time.
#
# The mock panel is built from this checkout with `go`; on a host without Go,
# cross-build it elsewhere and pass it in:
#   GOOS=linux GOARCH=amd64 go build -o acceptancepanel ./release/acceptancepanel
#   ACCEPTANCE_PANEL=./acceptancepanel bash ./release/runtime-acceptance.sh ...

BINARY="${1:?usage: runtime-acceptance.sh <binary> <stage-dir>}"
STAGE="${2:?usage: runtime-acceptance.sh <binary> <stage-dir>}"
BINARY="$(cd -- "$(dirname -- "${BINARY}")" && pwd)/$(basename -- "${BINARY}")"
mkdir -p "${STAGE}"
STAGE="$(cd -- "${STAGE}" && pwd)"
MODULE_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

cleanup() {
  for pid in "${node_pid:-}" "${panel_pid:-}"; do
    if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
      kill -TERM "${pid}" 2>/dev/null || true
      wait "${pid}" 2>/dev/null || true
    fi
  done
}
trap cleanup EXIT

PANEL_BIN="${ACCEPTANCE_PANEL:-}"
if [[ -z "${PANEL_BIN}" ]]; then
  command -v go >/dev/null || { echo "go not found; build release/acceptancepanel elsewhere and set ACCEPTANCE_PANEL" >&2; exit 1; }
  PANEL_BIN="${STAGE}/acceptancepanel"
  (cd "${MODULE_DIR}" && go build -mod=readonly -o "${PANEL_BIN}" ./release/acceptancepanel)
fi

port_open() {
  python3 - "$1" <<'PY'
import socket,sys
s=socket.socket(); s.settimeout(.1)
try: s.connect(('127.0.0.1',int(sys.argv[1])))
except OSError: raise SystemExit(1)
finally: s.close()
PY
}

# state_ok <state-file> <python-expression over s and b (baseline)>
state_ok() {
  python3 - "$1" "$2" "${baseline}" <<'PY'
import json,sys
try: s=json.load(open(sys.argv[1]))
except (OSError, ValueError): raise SystemExit(1)
b=json.loads(sys.argv[3])
raise SystemExit(0 if eval(sys.argv[2]) else 1)
PY
}

run_once() {
  local mode="$1" attempt="$2" dir="$3" node_port="$4" expect="$5"
  baseline="$(cat "${dir}/state.json")"
  "${BINARY}" -c "${dir}/config.json" >"${dir}/node-${attempt}.log" 2>&1 &
  node_pid=$!
  local ready=0
  for _ in $(seq 1 100); do
    if ! kill -0 "${node_pid}" 2>/dev/null; then
      wait "${node_pid}" || true
      echo "${mode}: node exited before readiness on attempt ${attempt}" >&2
      return 1
    fi
    if port_open "${node_port}"; then ready=1; break; fi
    sleep .05
  done
  [[ "${ready}" == 1 ]] || { echo "${mode}: node port never became ready" >&2; return 1; }
  # 端口就绪只说明配置到了；还要等面板侧看到这一轮该有的上报。health_passed
  # 在切换后稳定 5 秒才报，所以给 15 秒。
  local observed=0
  for _ in $(seq 1 150); do
    if state_ok "${dir}/state.json" "${expect}"; then observed=1; break; fi
    sleep .1
  done
  [[ "${observed}" == 1 ]] || {
    echo "${mode}: panel did not observe the expected reports on attempt ${attempt}: $(cat "${dir}/state.json")" >&2
    return 1
  }
  kill -TERM "${node_pid}"
  for _ in $(seq 1 100); do
    kill -0 "${node_pid}" 2>/dev/null || break
    sleep .05
  done
  if kill -0 "${node_pid}" 2>/dev/null; then
    echo "${mode}: node did not stop after SIGTERM" >&2
    return 1
  fi
  wait "${node_pid}"
  node_pid=""
  python3 - "${node_port}" <<'PY'
import socket,sys
# 探活连接在节点侧留下 TIME_WAIT；开 SO_REUSEADDR 只容忍它，仍在监听时照样 EADDRINUSE
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1',int(sys.argv[1]))); s.close()
PY
}

# run_offline：面板已停，节点冷启动必须用落盘缓存把入站起来，SIGTERM 后让出端口。
run_offline() {
  local mode="$1" dir="$2" node_port="$3"
  local cached
  for cached in config.json users.json; do
    local found
    found="$(find "${dir}/cache" -name "${cached}" -type f | head -n 1)"
    [[ -n "${found}" ]] || { echo "${mode}: no ${cached} in the disk cache after cold starts" >&2; return 1; }
    [[ "$(stat -c %a "${found}" 2>/dev/null || stat -f %Lp "${found}")" == 600 ]] || {
      echo "${mode}: cached ${cached} is not mode 0600" >&2; return 1; }
  done
  "${BINARY}" -c "${dir}/config.json" >"${dir}/node-offline.log" 2>&1 &
  node_pid=$!
  local ready=0
  for _ in $(seq 1 100); do
    if ! kill -0 "${node_pid}" 2>/dev/null; then
      wait "${node_pid}" || true
      echo "${mode}: node exited while the panel was down" >&2
      return 1
    fi
    if port_open "${node_port}"; then ready=1; break; fi
    sleep .05
  done
  [[ "${ready}" == 1 ]] || {
    echo "${mode}: node did not serve from its disk cache with the panel down" >&2
    cat "${dir}/node-offline.log" >&2
    return 1
  }
  kill -TERM "${node_pid}"
  for _ in $(seq 1 100); do
    kill -0 "${node_pid}" 2>/dev/null || break
    sleep .05
  done
  if kill -0 "${node_pid}" 2>/dev/null; then
    echo "${mode}: node did not stop after SIGTERM (panel down)" >&2
    return 1
  fi
  wait "${node_pid}"
  node_pid=""
  python3 - "${node_port}" <<'PY'
import socket,sys
# 探活连接在节点侧留下 TIME_WAIT；开 SO_REUSEADDR 只容忍它，仍在监听时照样 EADDRINUSE
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1',int(sys.argv[1]))); s.close()
PY
}

mode=grpc
dir="${STAGE}/grpc"
rm -rf "${dir}"
mkdir -p "${dir}"
read -r panel_port node_port < <(python3 - <<'PY'
import socket
ports=[]
for _ in range(2):
    s=socket.socket(); s.bind(('127.0.0.1', 0))
    ports.append(s.getsockname()[1]); s.close()
print(*ports)
PY
)
(umask 077 && python3 -c 'import secrets; print(secrets.token_urlsafe(32))' > "${dir}/token")

"${PANEL_BIN}" -listen "127.0.0.1:${panel_port}" -node-port "${node_port}" -state "${dir}/state.json" \
  -token-file "${dir}/token" -ca-fingerprint-out "${dir}/ca.sha256" >"${dir}/panel.log" 2>&1 &
panel_pid=$!
for _ in $(seq 1 100); do
  if port_open "${panel_port}" && [[ -f "${dir}/state.json" && -s "${dir}/ca.sha256" ]]; then break; fi
  sleep .05
done
port_open "${panel_port}" || { echo "${mode}: mock panel did not come up" >&2; cat "${dir}/panel.log" >&2; exit 1; }

# 指纹不对：握手就该失败，令牌不发出去，面板一次接入都没看到。
zero_fp="sha256:$(printf '0%.0s' $(seq 1 64))"
if "${BINARY}" enroll --panel "127.0.0.1:${panel_port}" --ca-sha256 "${zero_fp}" \
    --token-file "${dir}/token" --identity-dir "${dir}/identity-bad" >"${dir}/enroll-bad.log" 2>&1; then
  echo "${mode}: enroll with a wrong CA fingerprint succeeded" >&2
  exit 1
fi
baseline='{}'
state_ok "${dir}/state.json" "s['enrolls'] == 0" || {
  echo "${mode}: the token reached the panel despite a wrong fingerprint: $(cat "${dir}/state.json")" >&2; exit 1; }

"${BINARY}" enroll --panel "127.0.0.1:${panel_port}" --ca-sha256 "$(cat "${dir}/ca.sha256")" \
  --token-file "${dir}/token" --identity-dir "${dir}/identity" >"${dir}/enroll.log" 2>&1 || {
  echo "${mode}: enroll failed" >&2; cat "${dir}/enroll.log" >&2; exit 1; }
for f in ca.pem cert.pem key.pem server_id; do
  [[ -f "${dir}/identity/${f}" ]] || { echo "${mode}: identity dir lacks ${f}" >&2; exit 1; }
done
[[ "$(stat -c %a "${dir}/identity/key.pem" 2>/dev/null || stat -f %Lp "${dir}/identity/key.pem")" == 600 ]] || {
  echo "${mode}: key.pem is not mode 0600" >&2; exit 1; }
[[ ! -e "${dir}/identity/enroll.json" ]] || { echo "${mode}: enroll.json left behind" >&2; exit 1; }

cat > "${dir}/config.json" <<JSON
{"log_level":"info","cache_dir":"${dir}/cache","panel":{"address":"127.0.0.1:${panel_port}","identity_dir":"${dir}/identity"}}
JSON

first="s['heartbeats_with_metrics'] > b['heartbeats_with_metrics']"
first+=" and 'CONFIG_PHASE_HEALTH_PASSED' in s['config_phases'][len(b['config_phases']):]"
first+=" and s['users_acks_ok'] > b['users_acks_ok'] and s['bad_client_certs'] == 0 and s['missing_request_ids'] == 0"
# 第二次冷启动先用落盘缓存装好入站，Hello 报出已应用的版本，面板不再重发。
second="s['hellos_with_applied_config'] > b['hellos_with_applied_config']"
second+=" and s['heartbeats_with_metrics'] > b['heartbeats_with_metrics'] and s['bad_client_certs'] == 0"
run_once "${mode}" 1 "${dir}" "${node_port}" "${first}"
run_once "${mode}" 2 "${dir}" "${node_port}" "${second}"
observed="$(cat "${dir}/state.json")"
kill -TERM "${panel_pid}" 2>/dev/null || true
wait "${panel_pid}" 2>/dev/null || true
panel_pid=""
# 面板停了再冷启动一次：入站必须靠落盘缓存照常起来。
run_offline "${mode}" "${dir}" "${node_port}"

printf '{"mode_passed":"grpc","enroll":"ok","cold_starts":2,"offline_cold_starts":1,"panel_observed":%s}\n' "${observed}"
printf '{"status":"ok","modes":["grpc"],"cold_starts":2,"offline_cold_starts":1,"sigterm":"clean","port_released":true}\n'

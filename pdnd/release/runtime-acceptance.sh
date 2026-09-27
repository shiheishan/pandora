#!/usr/bin/env bash
set -Eeuo pipefail

# Exercise the actual release binary without touching system paths or services.
# Usage: runtime-acceptance.sh <pandora-native-binary> <stage-dir>
#
# Runs two panel access modes against a loopback mock panel, two cold starts
# each, with SIGTERM and port release checked every time:
#   signed - what the installer writes after enrollment: identity file plus
#            signed_required=true. Config comes from a signed effective
#            release; heartbeats (with host metrics) and config reports go
#            over the signed channel.
#   compat - no identity, UniProxy token only (installer path without an
#            enrollment token). Config and /status go over UniProxy.
#
# The mock panel is Go (release/acceptancepanel) because the signed channel
# needs Ed25519. It is built from this checkout with `go`; on a host without
# Go, cross-build it elsewhere and pass it in:
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
  # 端口就绪只说明配置到了；还要等面板侧看到这一轮该有的上报。签名模式要等
  # health_passed，节点在应用后稳定 5 秒才报，所以给 15 秒。
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
s=socket.socket(); s.bind(('127.0.0.1',int(sys.argv[1]))); s.close()
PY
}

run_mode() {
  local mode="$1" dir="${STAGE}/$1"
  mkdir -p "${dir}"
  rm -f "${dir}/state.json" "${dir}/identity.json"
  local panel_port node_port
  read -r panel_port node_port < <(python3 - <<'PY'
import socket
ports=[]
for _ in range(2):
    s=socket.socket(); s.bind(('127.0.0.1', 0))
    ports.append(s.getsockname()[1]); s.close()
print(*ports)
PY
)
  local panel_args=(-listen "127.0.0.1:${panel_port}" -node-port "${node_port}" -state "${dir}/state.json")
  local node_id="runtime-acceptance" signed_required=false expect
  if [[ "${mode}" == signed ]]; then
    node_id="$(python3 -c 'import uuid; print(uuid.uuid4())')"
    signed_required=true
    panel_args+=(-identity "${dir}/identity.json" -node-id "${node_id}")
    expect="s['heartbeats_with_metrics'] > b['heartbeats_with_metrics'] and 'health_passed' in s['report_phases'][len(b['report_phases']):]"
    expect+=" and s['bad_signatures'] == 0 and s['uniproxy_config_hits'] == 0 and s['uniproxy_status_reports'] == 0"
  else
    expect="s['uniproxy_config_hits'] > b['uniproxy_config_hits'] and s['uniproxy_status_reports'] > b['uniproxy_status_reports']"
  fi

  "${PANEL_BIN}" "${panel_args[@]}" >"${dir}/panel.log" 2>&1 &
  panel_pid=$!
  for _ in $(seq 1 100); do
    if port_open "${panel_port}" && [[ -f "${dir}/state.json" ]]; then break; fi
    sleep .05
  done

  # 兼容模式也显式给一个不存在的身份路径：落到缺省 /var/lib/... 的话，在装过
  # 节点的机器上会读到真身份，验的就不是兼容通道了。
  cat > "${dir}/config.json" <<JSON
{"log_level":"info","panel":{"url":"http://127.0.0.1:${panel_port}","timeout_seconds":2,"identity_path":"${dir}/identity.json","signed_required":${signed_required}},"nodes":[{"node_id":"${node_id}","node_type":"socks","token":"acceptance-only"}]}
JSON

  run_once "${mode}" 1 "${dir}" "${node_port}" "${expect}"
  run_once "${mode}" 2 "${dir}" "${node_port}" "${expect}"
  kill -TERM "${panel_pid}" 2>/dev/null || true
  wait "${panel_pid}" 2>/dev/null || true
  panel_pid=""
}

run_mode signed
run_mode compat
printf '{"status":"ok","modes":["signed","compat"],"cold_starts_per_mode":2,"sigterm":"clean","port_released":true}\n'

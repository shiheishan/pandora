#!/usr/bin/env bash
set -Eeuo pipefail

# Exercise the actual release binary without touching system paths or services.
# Usage: runtime-acceptance.sh <pandora-native-binary> <stage-dir>

BINARY="${1:?usage: runtime-acceptance.sh <binary> <stage-dir>}"
STAGE="${2:?usage: runtime-acceptance.sh <binary> <stage-dir>}"
BINARY="$(cd -- "$(dirname -- "${BINARY}")" && pwd)/$(basename -- "${BINARY}")"
mkdir -p "${STAGE}"
STAGE="$(cd -- "${STAGE}" && pwd)"

cleanup() {
  for pid in "${node_pid:-}" "${panel_pid:-}"; do
    if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
      kill -TERM "${pid}" 2>/dev/null || true
      wait "${pid}" 2>/dev/null || true
    fi
  done
}
trap cleanup EXIT

read -r panel_port node_port < <(python3 - <<'PY'
import socket
ports=[]
for kind in (socket.SOCK_STREAM, socket.SOCK_STREAM):
    s=socket.socket(socket.AF_INET, kind)
    s.bind(('127.0.0.1', 0))
    ports.append(s.getsockname()[1])
    s.close()
print(*ports)
PY
)

cat > "${STAGE}/panel.py" <<'PY'
import json, os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_): pass
    def do_GET(self):
        if self.path.startswith('/api/v1/server/UniProxy/config'):
            body=json.dumps({'protocol':'socks','server_port':int(os.environ['NODE_PORT']),
                             'base_config':{'pull_interval':1,'push_interval':1}}).encode()
            self.send_response(200); self.send_header('Content-Type','application/json')
            self.send_header('ETag','"runtime-v1"'); self.send_header('Content-Length',str(len(body)))
            self.end_headers(); self.wfile.write(body); return
        if self.path.startswith('/api/v1/server/UniProxy/user'):
            body=b'{"users":[]}'
            self.send_response(200); self.send_header('Content-Type','application/json')
            self.send_header('Content-Length',str(len(body))); self.end_headers(); self.wfile.write(body); return
        self.send_response(204); self.end_headers()
    def do_POST(self):
        length=int(self.headers.get('Content-Length','0')); self.rfile.read(length)
        self.send_response(200); self.send_header('Content-Length','0'); self.end_headers()

ThreadingHTTPServer(('127.0.0.1', int(os.environ['PANEL_PORT'])), Handler).serve_forever()
PY

cat > "${STAGE}/config.json" <<JSON
{"log_level":"info","panel":{"url":"http://127.0.0.1:${panel_port}","timeout_seconds":2},"nodes":[{"node_id":"runtime-acceptance","node_type":"socks","token":"acceptance-only"}]}
JSON

PANEL_PORT="${panel_port}" NODE_PORT="${node_port}" python3 "${STAGE}/panel.py" >"${STAGE}/panel.log" 2>&1 &
panel_pid=$!
for _ in $(seq 1 100); do
  if python3 - "${panel_port}" <<'PY'
import socket,sys
s=socket.socket(); s.settimeout(.1)
try: s.connect(('127.0.0.1',int(sys.argv[1])))
except OSError: raise SystemExit(1)
finally: s.close()
PY
  then break; fi
  sleep .05
done

run_once() {
  local attempt="$1"
  "${BINARY}" -c "${STAGE}/config.json" >"${STAGE}/node-${attempt}.log" 2>&1 &
  node_pid=$!
  local ready=0
  for _ in $(seq 1 100); do
    if ! kill -0 "${node_pid}" 2>/dev/null; then
      wait "${node_pid}" || true
      echo "node exited before readiness on attempt ${attempt}" >&2
      return 1
    fi
    if python3 - "${node_port}" <<'PY'
import socket,sys
s=socket.socket(); s.settimeout(.1)
try: s.connect(('127.0.0.1',int(sys.argv[1])))
except OSError: raise SystemExit(1)
finally: s.close()
PY
    then ready=1; break; fi
    sleep .05
  done
  [[ "${ready}" == 1 ]] || { echo "node port never became ready" >&2; return 1; }
  kill -TERM "${node_pid}"
  for _ in $(seq 1 100); do
    kill -0 "${node_pid}" 2>/dev/null || break
    sleep .05
  done
  if kill -0 "${node_pid}" 2>/dev/null; then
    echo "node did not stop after SIGTERM" >&2
    return 1
  fi
  wait "${node_pid}"
  node_pid=""
  python3 - "${node_port}" <<'PY'
import socket,sys
s=socket.socket(); s.bind(('127.0.0.1',int(sys.argv[1]))); s.close()
PY
}

run_once 1
run_once 2
printf '{"status":"ok","cold_starts":2,"sigterm":"clean","port_released":true,"port":%s}\n' "${node_port}"

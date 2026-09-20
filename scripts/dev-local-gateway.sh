#!/usr/bin/env bash
# dev-local-gateway.sh brings up a local Registry and Gateway on loopback
# ports, issuing whatever certs and bootstrap tokens the Agent needs to
# connect, so a developer testing Agent<->Gateway connectivity never hand-runs
# the registry/gateway CLI steps themselves. It is idempotent: rerunning it
# after a partial or full success skips whatever is already done and only
# starts what's missing.
#
# dev-local-gateway.sh 在回环地址上拉起本地 Registry 与 Gateway，把 Agent 连接
# 所需的证书和 bootstrap token 都签发好，开发者验证 Agent<->Gateway 连通性时
# 不用再手敲 registry/gateway 的 CLI 步骤。脚本是幂等的：重跑时已经就绪的部分
# 会被跳过，只补齐缺的那部分。
#
# Usage:
#   scripts/dev-local-gateway.sh          # bring everything up
#   scripts/dev-local-gateway.sh down     # stop the background Registry/Gateway this script started
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."
ROOT="$(pwd)"

REGISTRY_DIR="service/aiServeWeaveRegistry"
GATEWAY_DIR="service/aiServeWeaveGateway"
AGENT_DIR="service/aiServeWeaveAgent"

REGISTRY_ADDR="127.0.0.1:19090"
REGISTRY_METRICS_ADDR="127.0.0.1:19091"
GATEWAY_TUNNEL_ADDR="127.0.0.1:18443"
GATEWAY_HTTP_ADDR="127.0.0.1:18080"
GATEWAY_METRICS_ADDR="127.0.0.1:19093"

DATA_DIR="$REGISTRY_DIR/data/registry"
CERTS_DIR="$REGISTRY_DIR/certs"
ADMIN_TOKEN_FILE="$DATA_DIR/admin-token"
CA_FILE="$DATA_DIR/ca/ca-cert.pem"

PID_DIR=".dev-local-gateway"
REGISTRY_PID_FILE="$PID_DIR/registry.pid"
GATEWAY_PID_FILE="$PID_DIR/gateway.pid"
REGISTRY_LOG="$PID_DIR/registry.log"
GATEWAY_LOG="$PID_DIR/gateway.log"

# Read node_id straight out of config.yaml instead of hardcoding it a second
# time here, so the two can't silently drift apart.
#
# 直接从 config.yaml 里读 node_id，不在这里重复写一遍硬编码值，避免两边悄悄不一致。
AGENT_NODE_ID="$(sed -n 's/^[[:space:]]*node_id:[[:space:]]*\([^[:space:]#]*\).*/\1/p' "$AGENT_DIR/config.yaml" | head -1)"
AGENT_DATA_DIR="$AGENT_DIR/data"
AGENT_CERT_FILE="$AGENT_DATA_DIR/agent-cert.pem"
AGENT_KEY_FILE="$AGENT_DATA_DIR/agent-key.pem"
AGENT_TOKEN_FILE="$AGENT_DATA_DIR/bootstrap-token"

pid_alive() {
  [[ -f "$1" ]] && kill -0 "$(cat "$1")" 2>/dev/null
}

# require_port_free fails fast with the occupying process's name, instead of
# letting `go run` surface a bare "bind: address already in use" that doesn't
# say who's holding the port or what to do about it.
#
# require_port_free 在端口被占时直接失败，并报出占用者的进程名，取代
# 让 `go run` 抛出一句看不出是谁占用、也不知道怎么处理的 "bind: address
# already in use"。
require_port_free() {
  local addr="$1" pid_file="$2" port="${1##*:}"
  # A port already held by a process this script itself started is fine;
  # that's the "already running, skip" case the caller handles separately.
  if pid_alive "$pid_file"; then
    return 0
  fi
  local holder
  holder="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null | head -1 || true)"
  if [[ -n "$holder" ]]; then
    local holder_name
    holder_name="$(ps -p "$holder" -o comm= 2>/dev/null || echo "pid $holder")"
    echo "端口 ${addr} 已被占用（${holder_name}），改一下脚本里的 ${3} 变量换个端口再重跑。" >&2
    exit 1
  fi
}

if [[ "${1:-}" == "down" ]]; then
  for pid_file in "$REGISTRY_PID_FILE" "$GATEWAY_PID_FILE"; do
    if pid_alive "$pid_file"; then
      echo "stopping $(cat "$pid_file")"
      kill "$(cat "$pid_file")"
    fi
    rm -f "$pid_file"
  done
  exit 0
fi

mkdir -p "$PID_DIR" "$DATA_DIR" "$AGENT_DATA_DIR"

# --- Registry -----------------------------------------------------------
if pid_alive "$REGISTRY_PID_FILE"; then
  echo "registry: already running (pid $(cat "$REGISTRY_PID_FILE"))"
else
  require_port_free "$REGISTRY_ADDR" "$REGISTRY_PID_FILE" REGISTRY_ADDR
  require_port_free "$REGISTRY_METRICS_ADDR" "$REGISTRY_PID_FILE" REGISTRY_METRICS_ADDR
  if [[ ! -f "$ADMIN_TOKEN_FILE" ]]; then
    openssl rand -hex 32 > "$ADMIN_TOKEN_FILE"
    chmod 600 "$ADMIN_TOKEN_FILE"
  fi
  echo "registry: starting on $REGISTRY_ADDR"
  (cd "$REGISTRY_DIR" && exec nohup go run . \
    -data-dir ./data/registry -addr "$REGISTRY_ADDR" -metrics-addr "$REGISTRY_METRICS_ADDR" \
    -admin-token-file ./data/registry/admin-token \
    > "$ROOT/$REGISTRY_LOG" 2>&1) &
  echo $! > "$REGISTRY_PID_FILE"
fi

echo -n "registry: waiting for CA "
for _ in $(seq 1 30); do
  [[ -f "$CA_FILE" ]] && break
  echo -n "."
  sleep 1
done
echo
if [[ ! -f "$CA_FILE" ]]; then
  echo "registry: CA never appeared, check $REGISTRY_LOG" >&2
  exit 1
fi
echo "registry: ready ($CA_FILE)"

# --- Gateway server certificate ------------------------------------------
if [[ -f "$CERTS_DIR/server-cert.pem" && -f "$CERTS_DIR/server-key.pem" ]]; then
  echo "gateway cert: already issued"
else
  echo "gateway cert: issuing"
  (cd "$REGISTRY_DIR" && go run . -data-dir ./data/registry -issue-server-cert \
    -tls-host 127.0.0.1,localhost -out-dir ./certs)
fi

# --- Gateway --------------------------------------------------------------
if pid_alive "$GATEWAY_PID_FILE"; then
  echo "gateway: already running (pid $(cat "$GATEWAY_PID_FILE"))"
else
  require_port_free "$GATEWAY_TUNNEL_ADDR" "$GATEWAY_PID_FILE" GATEWAY_TUNNEL_ADDR
  require_port_free "$GATEWAY_HTTP_ADDR" "$GATEWAY_PID_FILE" GATEWAY_HTTP_ADDR
  require_port_free "$GATEWAY_METRICS_ADDR" "$GATEWAY_PID_FILE" GATEWAY_METRICS_ADDR
  echo "gateway: starting on $GATEWAY_TUNNEL_ADDR (tunnel) / $GATEWAY_HTTP_ADDR (http)"
  (cd "$GATEWAY_DIR" && exec nohup go run . \
    -addr "$GATEWAY_HTTP_ADDR" \
    -tunnel-addr "$GATEWAY_TUNNEL_ADDR" \
    -tls-cert "$ROOT/$CERTS_DIR/server-cert.pem" \
    -tls-key "$ROOT/$CERTS_DIR/server-key.pem" \
    -client-ca "$ROOT/$CA_FILE" \
    -registry-addr "$REGISTRY_ADDR" \
    -registry-ca "$ROOT/$CA_FILE" \
    -metrics-addr "$GATEWAY_METRICS_ADDR" \
    > "$ROOT/$GATEWAY_LOG" 2>&1) &
  echo $! > "$GATEWAY_PID_FILE"
  sleep 2
fi

# --- Agent bootstrap token --------------------------------------------
if [[ -f "$AGENT_CERT_FILE" && -f "$AGENT_KEY_FILE" ]]; then
  echo "agent identity: already issued ($AGENT_CERT_FILE), no token needed"
else
  if [[ -z "$AGENT_NODE_ID" ]]; then
    echo "agent identity: config.yaml has no node_id set; can't bind the token, so a fresh Registry will reject the strict-approval check. Set gateway.node_id in $AGENT_DIR/config.yaml and rerun." >&2
    exit 1
  fi
  echo "agent identity: minting a fresh bootstrap token bound to node_id=$AGENT_NODE_ID (60m TTL)"
  # Binding the token to node_id skips the Registry's strict pre-approval
  # check (see the Registry README's "严格审批" section) — minting the token
  # itself, with the admin token, is the authorization.
  #
  # 绑定 node_id 的 token 会跳过 Registry 的严格预审批检查（见 Registry
  # README「严格审批」一节）——用 admin token 铸造这枚 token 这个动作本身就是授权。
  (cd "$REGISTRY_DIR" && go run . -data-dir ./data/registry -admin-token-file ./data/registry/admin-token \
    -mint-token -ttl 60m -bind-node-id "$AGENT_NODE_ID" -registry-addr "$REGISTRY_ADDR") > "$AGENT_TOKEN_FILE"
  chmod 600 "$AGENT_TOKEN_FILE"
  echo "agent identity: token written to $AGENT_TOKEN_FILE"
fi

cat <<EOF

环境已就绪：
  Registry  : $REGISTRY_ADDR  (log: $REGISTRY_LOG)
  Gateway   : tunnel $GATEWAY_TUNNEL_ADDR / http $GATEWAY_HTTP_ADDR  (log: $GATEWAY_LOG)

现在只需要：
  cd $AGENT_DIR && go run .

停止本脚本起的 Registry/Gateway：
  scripts/dev-local-gateway.sh down
EOF

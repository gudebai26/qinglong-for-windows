#!/bin/bash
# Contract diff harness for the user/auth milestone (M2).
#
# Runs the same request sequence against the Node panel and the Go panel, both
# starting from an identical, freshly bootstrapped data directory, and compares
# status codes plus response bodies (tokens, timestamps and secrets normalised).
#
# Usage:
#   QL_NODE_DIR="F:/WorkBuddy AI/qinglong/qinglong" bash scripts/contract-diff/run-diff.sh
#
# Environment:
#   QL_NODE_DIR  Node panel checkout (needs static/build/app.js and node_modules)
#   QL_GO_BIN    Go binary (default: <repo>/qinglong-go.exe)
#   WORK_DIR     scratch directory (default: <repo>/.tmp/contract-diff)
set -u

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
REPO_DIR=$(cd "$SCRIPT_DIR/../.." && pwd)
NODE_DIR=${QL_NODE_DIR:-"F:/WorkBuddy AI/qinglong/qinglong"}
GO_BIN=${QL_GO_BIN:-"$REPO_DIR/qinglong-go.exe"}
WORK_DIR=${WORK_DIR:-"$REPO_DIR/.tmp/contract-diff"}
BASE="$WORK_DIR/base"

NODE_PORT=5931
NODE_GRPC=5932
GO_PORT=5933

mkdir -p "$WORK_DIR"

wait_health() {
  for _ in $(seq 1 80); do
    if [ "$(curl -s -o /dev/null -w "%{http_code}" "$1/api/health" || true)" = "200" ]; then
      return 0
    fi
    sleep 0.5
  done
  return 1
}

stop_port() {
  for pid in $(netstat -ano | grep -E 'LISTENING' | grep ":$1 " | awk '{print $5}' | sort -u); do
    if [ "$pid" != "0" ] && [ "$pid" != "" ]; then
      taskkill //F //PID "$pid" >/dev/null 2>&1
    fi
  done
}

# Stops the Node panel through the PID recorded by launch.js. The panel is a
# cluster primary, so /T is required to take the workers down as well.
stop_node_panel() {
  if [ -f "$WORK_DIR/node.pid" ]; then
    taskkill //F //T //PID "$(cat "$WORK_DIR/node.pid")" >/dev/null 2>&1
    rm -f "$WORK_DIR/node.pid"
  fi
  stop_port "$1"
}

if [ ! -f "$BASE/db/keyv.sqlite" ]; then
  echo "== bootstrap base data with the Node panel =="
  rm -rf "$BASE"; mkdir -p "$BASE/db" "$BASE/config"
  ( cd "$NODE_DIR" && QL_DIR="$NODE_DIR" QL_DATA_DIR="$BASE" BACK_PORT=5941 GRPC_PORT=5942 \
      node "$SCRIPT_DIR/launch.js" static/build/app.js "$WORK_DIR/node.pid" \
      > "$WORK_DIR/base.log" 2>&1 ) &
  wait_health http://127.0.0.1:5941 || { echo "base bootstrap failed"; tail -20 "$WORK_DIR/base.log"; exit 1; }
  stop_node_panel 5941
  sleep 1
fi

echo "== node =="
rm -rf "$WORK_DIR/run-node"; cp -r "$BASE" "$WORK_DIR/run-node"
( cd "$NODE_DIR" && QL_DIR="$NODE_DIR" QL_DATA_DIR="$WORK_DIR/run-node" \
    BACK_PORT=$NODE_PORT GRPC_PORT=$NODE_GRPC \
    node "$SCRIPT_DIR/launch.js" static/build/app.js "$WORK_DIR/node.pid" \
    > "$WORK_DIR/node.log" 2>&1 ) &
wait_health "http://127.0.0.1:$NODE_PORT" || { echo "node did not start"; tail -20 "$WORK_DIR/node.log"; exit 1; }
python "$SCRIPT_DIR/probe.py" "http://127.0.0.1:$NODE_PORT" > "$WORK_DIR/node.json" || exit 1
stop_node_panel $NODE_PORT
sleep 1

echo "== go =="
rm -rf "$WORK_DIR/run-go"; cp -r "$BASE" "$WORK_DIR/run-go"
( cd "$REPO_DIR" && QL_DATA_DIR="$WORK_DIR/run-go" BACK_PORT=$GO_PORT exec "$GO_BIN" > "$WORK_DIR/go.log" 2>&1 ) &
wait_health "http://127.0.0.1:$GO_PORT" || { echo "go did not start"; tail -20 "$WORK_DIR/go.log"; exit 1; }
python "$SCRIPT_DIR/probe.py" "http://127.0.0.1:$GO_PORT" > "$WORK_DIR/go.json" || exit 1
stop_port $GO_PORT

echo "== diff =="
python "$SCRIPT_DIR/compare.py" "$WORK_DIR/node.json" "$WORK_DIR/go.json"

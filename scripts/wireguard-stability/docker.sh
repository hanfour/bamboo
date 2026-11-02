#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-or-later
set -euo pipefail
root="$(cd "$(dirname "$0")/../.." && pwd)"
out="${BAMBOO_KERNEL_OUT:-/private/tmp/bamboo-kernel-stability}"
image="${BAMBOO_KERNEL_IMAGE:-node:22.23.3-bookworm}"
mkdir -p "$out"
if [[ "${1:-}" == build ]]; then
  cd "$root"
  arch="${BAMBOO_KERNEL_ARCH:-$(docker info --format '{{.Architecture}}')}"
  case "$arch" in
    aarch64|arm64) arch=arm64 ;;
    x86_64|amd64) arch=amd64 ;;
    *) echo "Unsupported Docker architecture: $arch" >&2; exit 2 ;;
  esac
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go test -c -o "$out/stability.test" ./clients/cli/cmd/bamboo
  exit
fi
if [[ "${1:-}" != run ]]; then
  echo "Usage: $0 build|run [roaming|restart|all]" >&2
  exit 2
fi
scenario="${2:-all}"
[[ -f "$out/stability.test" ]] || { echo 'Build the test binary first.' >&2; exit 2; }
run_id="bamboo-stability-$(date +%s)-$$"
network="$run_id-net"
containers=()
cleanup() {
  if ((${#containers[@]})); then
    docker rm -f "${containers[@]}" >/dev/null 2>&1 || true
  fi
  docker network rm "$network" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
docker network create --internal "$network" >/dev/null

# run_case <log prefix> <server test> <client test> <client timeout>
# Starts the server-role test, waits for its control port, then runs
# the client-role test to completion. Returns the client's exit code.
run_case() {
  local prefix="$1" server_test="$2" client_test="$3" timeout="$4"
  local server="$run_id-${prefix}server" client="$run_id-${prefix}client"
  containers+=("$server" "$client")
  docker run -d --name "$server" --network "$network" \
    --cap-drop ALL --cap-add NET_ADMIN --security-opt no-new-privileges \
    --read-only --mount "type=bind,src=$out/stability.test,dst=/stability.test,readonly" \
    -e BAMBOO_KERNEL_ROLE=server "$image" /stability.test \
    -test.run "^${server_test}\$" -test.v -test.timeout 180s >/dev/null
  local server_ip
  server_ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$server")"
  docker create --name "$client" --network "$network" \
    --cap-drop ALL --cap-add NET_ADMIN --security-opt no-new-privileges \
    --read-only --mount "type=bind,src=$out/stability.test,dst=/stability.test,readonly" \
    -e BAMBOO_KERNEL_ROLE=client -e "BAMBOO_KERNEL_SERVER=http://$server_ip:8080" \
    "$image" /bin/sh -c "export BAMBOO_KERNEL_CLIENT_ENDPOINT=\"\$(hostname -i | cut -d ' ' -f 1):51820\"; exec /stability.test -test.run '^${client_test}\$' -test.v -test.timeout $timeout" >/dev/null
  local ready=false i
  for ((i=0; i<50; i++)); do
    if docker exec "$server" node -e 'fetch("http://127.0.0.1:8080/public").then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))' >/dev/null 2>&1; then
      ready=true
      break
    fi
    sleep 0.2
  done
  if [[ "$ready" != true ]]; then
    docker logs "$server" > "$out/${prefix}server.log" 2>&1
    cat "$out/${prefix}server.log"
    return 1
  fi
  docker start -a "$client" > "$out/${prefix}client.log" 2>&1 || true
  local status
  status="$(docker inspect -f '{{.State.ExitCode}}' "$client")"
  docker logs "$server" > "$out/${prefix}server.log" 2>&1
  cat "$out/${prefix}client.log"
  return "$status"
}

status=0
if [[ "$scenario" == roaming || "$scenario" == all ]]; then
  run_case "" TestKernelStabilityServer TestKernelStabilityWatchPreservesRoaming 60s || status=$?
fi
# restart: the client plays a VPS whose bamboo restarts; the server
# plays a NAT'd peer that can only answer. The peer advertises an
# off-subnet private, a dead same-subnet and a reachable candidate.
if [[ "$scenario" == restart || "$scenario" == all ]]; then
  run_case restart- TestKernelRestartMac TestKernelRestartVPS 120s || status=$?
fi
exit "$status"

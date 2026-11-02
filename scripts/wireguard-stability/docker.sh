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
  echo "Usage: $0 build|run" >&2
  exit 2
fi
[[ -f "$out/stability.test" ]] || { echo 'Build the test binary first.' >&2; exit 2; }
run_id="bamboo-stability-$(date +%s)-$$"
network="$run_id-net"
server="$run_id-server"
client="$run_id-client"
cleanup() {
  docker rm -f "$client" "$server" >/dev/null 2>&1 || true
  docker network rm "$network" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM
docker network create --internal "$network" >/dev/null
docker run -d --name "$server" --network "$network" --network-alias stability-server \
  --cap-drop ALL --cap-add NET_ADMIN --security-opt no-new-privileges \
  --read-only --mount "type=bind,src=$out/stability.test,dst=/stability.test,readonly" \
  -e BAMBOO_KERNEL_ROLE=server "$image" /stability.test \
  -test.run '^TestKernelStabilityServer$' -test.v -test.timeout 120s >/dev/null
server_ip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$server")"
docker create --name "$client" --network "$network" \
  --cap-drop ALL --cap-add NET_ADMIN --security-opt no-new-privileges \
  --read-only --mount "type=bind,src=$out/stability.test,dst=/stability.test,readonly" \
  -e BAMBOO_KERNEL_ROLE=client -e "BAMBOO_KERNEL_SERVER=http://$server_ip:8080" \
  "$image" /bin/sh -c 'export BAMBOO_KERNEL_CLIENT_ENDPOINT="$(hostname -i | cut -d " " -f 1):51820"; exec /stability.test -test.run "^TestKernelStabilityWatchPreservesRoaming$" -test.v -test.timeout 60s' >/dev/null
ready=false
for ((i=0; i<50; i++)); do
  if docker exec "$server" node -e 'fetch("http://127.0.0.1:8080/public").then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))' >/dev/null 2>&1; then
    ready=true
    break
  fi
  sleep 0.2
done
if [[ "$ready" != true ]]; then
  docker logs "$server" > "$out/server.log" 2>&1
  cat "$out/server.log"
  exit 1
fi
docker start -a "$client" > "$out/client.log" 2>&1 || true
status="$(docker inspect -f '{{.State.ExitCode}}' "$client")"
docker logs "$server" > "$out/server.log" 2>&1
cat "$out/client.log"
exit "$status"

#!/bin/sh
# Train one tenant's Isolation Forest and write anomaly_findings.
# The controller turns those rows into FLAG_ANOMALOUS recommendations.
#
#   CLICKHOUSE_URL=clickhouse://bamboo:dev@127.0.0.1:19000/bamboo \
#     ./scripts/run-tenant.sh <tenant-uuid>
set -eu
if [ "$#" -ne 1 ]; then
  echo "usage: run-tenant.sh <tenant-uuid>" >&2
  exit 2
fi
exec bamboo-ai run --tenant "$1" --since "${BAMBOO_AI_SINCE:-7d}"

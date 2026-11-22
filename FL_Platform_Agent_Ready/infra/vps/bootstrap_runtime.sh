#!/bin/bash
# Run from the application root on the authorized Linux VPS with Docker Compose.
set -euo pipefail
python3 infra/vps/init_secrets.py
python3 infra/vps/init_runtime.py
make tools
docker compose up -d postgres minio
for attempt in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:19000/minio/health/ready >/dev/null; then break; fi
  sleep 2
done
python3 infra/vps/check_s3.py
mkdir -p /opt/fl-platform/presets
docker run --rm -v "$PWD/services/ml-client/src:/src:ro" \
  -v /opt/fl-platform/presets:/presets -e PYTHONPATH=/src \
  fl-platform/ml-base:mvp python -m fl_client.datasets.provision /presets
make build
docker run --rm --network fl-platform_default \
  -v /opt/fl-platform/secrets:/run/secrets:ro \
  -v /opt/fl-platform/presets:/presets:ro -v "$PWD/db/migrations:/migrations:ro" \
  --entrypoint /usr/local/bin/bootstrap fl-platform/control-plane:mvp
docker compose -f compose.yaml -f compose.runtime.yaml up -d

#!/bin/bash
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$root/services/control-plane/gen/go/flplatform/v1" "$root/services/ml-client/src/fl_client/gen"
cd "$root/services/control-plane"
module=github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane
python -m grpc_tools.protoc -I"$root/contracts/proto/v1" \
  --go_out=. --go_opt="module=$module" \
  --go-grpc_out=. --go-grpc_opt="module=$module" \
  --python_out="$root/services/ml-client/src/fl_client/gen" \
  --grpc_python_out="$root/services/ml-client/src/fl_client/gen" fl_platform.proto

#!/bin/bash
set -euo pipefail
work=$(mktemp -d /tmp/fl-proto-smoke.XXXXXX)
cd "$work"
mkdir -p gen/go/flplatform/v1 gen/python
module=github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane
python -m grpc_tools.protoc -I/src/contracts/proto/v1 \
  --python_out=gen/python --grpc_python_out=gen/python \
  --go_out=. --go_opt="module=$module" \
  --go-grpc_out=. --go-grpc_opt="module=$module" \
  --descriptor_set_out=schema.pb --include_imports fl_platform.proto
cat > go.mod <<EOF
module $module
go 1.25.0
require (
  google.golang.org/grpc v1.75.1
  google.golang.org/protobuf v1.36.9
)
EOF
cp /src/contracts/smoke/wirecheck.go .
export PYTHONPATH="$work/gen/python"
python /src/contracts/smoke/check_wire.py prepare
go mod tidy
go test ./...
go run .
python /src/contracts/smoke/check_wire.py verify
python -m grpc_tools.protoc --version
go version
mkdir -p /out
cp schema.pb go.mod go.sum python.bin go.bin schema.sha /out/
pip freeze > /out/python-toolchain.txt
echo 'PASS: generated Go/Python, original fields/RPCs, bidirectional wire and negative cases'

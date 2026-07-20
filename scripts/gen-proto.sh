#!/usr/bin/env bash
# Regenerate Go gRPC code from the protos. Requires protoc, protoc-gen-go, and
# protoc-gen-go-grpc on PATH (go install google.golang.org/protobuf/cmd/protoc-gen-go
# and google.golang.org/grpc/cmd/protoc-gen-go-grpc).
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

# Well-known types (google/protobuf/*.proto) ship in protoc's include/ dir but
# are not on the import path by default for standalone installs; locate them.
WKT_INCLUDE=()
for dir in "$(dirname "$(command -v protoc)")/../include" /usr/local/include /usr/include; do
  if [ -f "$dir/google/protobuf/duration.proto" ]; then
    WKT_INCLUDE=(-I "$dir")
    break
  fi
done

protoc -I . "${WKT_INCLUDE[@]}" \
  --go_out=. --go_opt=module=github.com/bgrewell/loom \
  --go-grpc_out=. --go-grpc_opt=module=github.com/bgrewell/loom \
  proto/loom/v1/control.proto

echo "generated api/loomv1/*.pb.go"

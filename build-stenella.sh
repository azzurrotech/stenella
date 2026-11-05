#!/bin/bash
# build-stenella.sh - Build and run the STENELLA OSINT platform
#
# Stenella runs on top of atp, providing the dashboard, UI, and OSINT functions.
#
# Usage:
#   ./build-stenella.sh           # build and start stenella (starts atp if needed)
#   ./build-stenella.sh 8085    # start stenella on port 8085
#   ./build-stenella.sh atp     # build and start atp first, then stenella
#   ./build-stenella.sh url     # connect to existing atp URL
#
# Automatically detects an open local port if none specified,
# otherwise accepts a URL as argument.

set -e

# Resolve script directory
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Parse arguments: port, atp suboption, or URL
PORT=""
RUN_ATP=""

while [ $# -gt 0 ]; do
  case "$1" in
    atp)
      RUN_ATP="true"
      shift
      ;;
    http://*|https://*)
      # Connect to existing atp URL
      echo ">>> Connecting to existing ATP at: $1"
      export ATP_ADDR="$1"
      shift
      ;;
    --port|-p)
      PORT="$2"
      shift 2
      ;;
    *)
      # Assume it's a port number
      if echo "$1" | grep -qE '^[0-9]+$'; then
        PORT=":$1"
      else
        echo "ERROR: Expected a port number, 'atp' suboption, or URL, got: $1"
        exit 1
      fi
      shift
      ;;
  esac
done

# If atp suboption requested, build and start atp first, then stenella
if [ -n "$RUN_ATP" ]; then
  echo ">>> Building and starting ATP first (required by stenella)..."
  "$SCRIPT_DIR/build-atp.sh"
  if [ $? -ne 0 ]; then
    echo "ERROR: ATP failed to start; cannot start stenella."
    exit 1
  fi
fi

# Set ATP secret
if [ -z "${ATP_SECRET:-}" ]; then
  export ATP_SECRET="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
fi

# Find an open port if not provided
if [ -z "$PORT" ]; then
  for trial_port in 8085 8086 8087 9090 3000; do
    if ! curl -s "http://localhost${trial_port}" > /dev/null 2>&1; then
      PORT=":$trial_port"
      echo ">>> Using available stenella port $trial_port"
      break
    fi
  done
  if [ -z "$PORT" ]; then
    echo ">>> No default port available, relying on Go defaults/env"
    PORT=""
  fi
fi

# Build the Go binary
echo ">>> Building STENELLA binary..."
cd "$SCRIPT_DIR"
go build -o stenella .

echo ">>> Starting STENELLA..."
# Set the stenella listen address and secret
export ATP_SECRET="${ATP_SECRET:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}"
ADDR_FLAG="${PORT:-:8080}"
if [ -n "$ATP_ADDR" ]; then
  echo ">>> Using stenella port ${PORT#:}, connecting to ATP at ${ATP_ADDR}"
  ./"stenella" -addr "$ADDR_FLAG" -root "./data" -secret "${ATP_SECRET}"
else
  ./"stenella" -addr "$ADDR_FLAG" -root "./data" -secret "${ATP_SECRET}"
fi
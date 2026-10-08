#!/usr/bin/env bash
# run-stenella.sh — Run stenella locally with default admin/admin credentials

set -euo pipefail

# Configuration
PORT="${PORT:-8084}"
ROOT="${ROOT:-./data}"
SECRET="${SECRET:-this-is-a-stenella-secret-that-is-at-least-32-characters-long!}"
ADMIN_USER="${ADMIN_USER:-admin}"
ADMIN_PASS="${ADMIN_PASS:-admin}"
LIBS_DIR="${LIBS_DIR:-static}"
PUBLIC_BASE="${PUBLIC_BASE:-}"
PLATFORM_PATH="${PLATFORM_PATH:-/platform}"
TRUST_PROXY="${TRUST_PROXY:-false}"
ALLOW_PRIVATE_FETCH="${ALLOW_PRIVATE_FETCH:-true}"

# Build stenella
echo "Building stenella..."
cd "$(dirname "$0")/stenella"
go build -o stenella .

# Run
echo "Starting stenella on :${PORT}..."
echo "  Admin: ${ADMIN_USER} / ${ADMIN_PASS}"
echo "  Root:  ${ROOT}"
echo ""

exec ./stenella \
  --port "${PORT}" \
  --root "${ROOT}" \
  --secret "${SECRET}" \
  --admin-user "${ADMIN_USER}" \
  --admin-pass "${ADMIN_PASS}" \
  --libs-dir "${LIBS_DIR}" \
  --platform-path "${PLATFORM_PATH}" \
  ${PUBLIC_BASE:+--public-base "${PUBLIC_BASE}"} \
  ${TRUST_PROXY:+--trust-proxy} \
  ${ALLOW_PRIVATE_FETCH:+--allow-private-fetch}
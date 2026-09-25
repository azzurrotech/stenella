#!/bin/bash
set -e

# Generate a valid 32+ byte secret
SECRET="$(tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 32)"

echo "Starting stenella server with secret: $SECRET"
echo "Admin password: testpass"

# Start stenella
trap "echo 'Stopping server...'" EXIT

export STENELLA_SECRET="$SECRET"
export STENELLA_ADMIN_PASSWORD="testpass"
./stenella-azzurro --root=./data --port=8084 --libs-dir=static

# Note: The server will run forever, so this script doesn't need to exit
# Use Ctrl+C to stop it
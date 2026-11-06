#!/bin/bash
# run.sh for stenella - Standalone OSINT platform
#
# Detects if stenella is already running locally and prompts:
#   use local / start new local / use remote
# Default: use any running local instance

set -e

STENELLA_PORT="${STENELLA_PORT:-8085}"

# Check if stenella is already running locally
is_local_running() {
    curl -s "http://127.0.0.1:$STENELLA_PORT/api/health" > /dev/null 2>&1
    return $?
}

# Check if stenella remote is reachable
is_remote_running() {
    curl -s "http://${STENELLA_REMOTE:-127.0.0.1:$STENELLA_PORT}/api/health" > /dev/null 2>&1
    return $?
}

echo "=== stenella Service ==="
echo "Configuration:"
echo "  Local address: 127.0.0.1:$STENELLA_PORT"

SELECTED=""

if is_local_running; then
    echo "✓ Local stenella is already running on 127.0.0.1:$STENELLA_PORT"
    read -p "Use local instance? [Y/n/remote]: " choice
    case "$choice" in
        n|N)
            SELECTED="remote"
            ;;
        *)
            SELECTED="local"
            ;;
    esac
fi

if [ "$SELECTED" = "" ]; then
    if is_remote_running; then
        echo "✓ Remote stenella is reachable at ${STENELLA_REMOTE:-127.0.0.1:$STENELLA_PORT}"
        read -p "Use remote instance? [y/N/local]: " choice
        case "$choice" in
            y|Y)
                SELECTED="remote"
                ;;
            l|L)
                SELECTED="local"
                ;;
            *)
                SELECTED="local"
                ;;
        esac
    else
        echo "✗ No local or remote stenella running. Starting new local instance..."
        SELECTED="local"
    fi
fi

case "$SELECTED" in
    local)
        echo ">>> Starting new local stenella instance..."
        if [ -x "$(command -v stenella)" ]; then
            stenella -port "$STENELLA_PORT" &
            echo "    PID: $!"
            echo "✓ stenella started locally on 127.0.0.1:$STENELLA_PORT"
        else
            echo "✗ stenella binary not found. Please build first: go build ./..."
        fi
        ;;
    remote)
        echo ">>> Using remote stenella at ${STENELLA_REMOTE:-127.0.0.1:$STENELLA_PORT}"
        if [ -z "$STENELLA_REMOTE" ]; then
            export STENELLA_REMOTE="${STENELLA_REMOTE:-127.0.0.1:$STENELLA_PORT}"
            echo "✓ Remote set: $STENELLA_REMOTE"
        fi
        ;;
    *)
        echo ">>> Starting new local stenella instance..."
        if [ -x "$(command -v stenella)" ]; then
            stenella -port "$STENELLA_PORT" &
            echo "    PID: $!"
            echo "✓ stenella started locally on 127.0.0.1:$STENELLA_PORT"
        else
            echo "✗ stenella binary not found. Please build first: go build ./..."
        fi
        ;;
esac

echo ""
echo "stenella service ready on port $STENELLA_PORT"
echo "API endpoints:"
echo "  Health:         http://127.0.0.1:$STENELLA_PORT/api/health"
echo "  Console:        http://127.0.0.1:$STENELLA_PORT/api/console"
echo "  Platform:       http://127.0.0.1:$STENELLA_PORT/api/platform"
echo "  Functions:      http://127.0.0.1:$STENELLA_PORT/api/functions"
echo "  Sub-projects:   http://127.0.0.1:$STENELLA_PORT/api/subprojects"
echo "  Apps:           http://127.0.0.1:$STENELLA_PORT/api/apps"
echo "  Pod:            http://127.0.0.1:$STENELLA_PORT/api/pod"
echo "  Song:           http://127.0.0.1:$STENELLA_PORT/api/song"
echo "  Shepherd:       http://127.0.0.1:$STENELLA_PORT/api/shepherd"
echo "  Headers:        http://127.0.0.1:$STENELLA_PORT/api/headers"
echo "  Firewall:       http://127.0.0.1:$STENELLA_PORT/api/firewall"
echo "  Static:         http://127.0.0.1:$STENELLA_PORT/_song/"

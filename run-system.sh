#!/bin/bash
# stenella System Runner
# Usage: ./run-system.sh [options]
# Displays port information and manages stenella data platform operations

set -euo pipefail

# Display system information and port usage
function show_system_info() {
    echo "========================================"
    echo "stenella Data Platform - System Information"
    echo "========================================"
    echo
    echo "PORT CONFIGURATION:"
    echo "  Primary Port: 8084 (default)"
    echo "  Configuration: via --port flag or STENELLA_PORT env var"
    echo "  Access: Requires TLS termination (Caddy reverse proxy recommended)"
    echo
}

# Show port usage information
function show_port_usage() {
    echo "PORT USAGE INFORMATION:"
    echo "--------------------------------------------"
    echo "Service Endpoints (with default port 8084):"
    echo "  → Web Interface: http://localhost:8084/"
    echo "  → Client Portal: https://your-domain:8084/s/portal?client={client}"
    echo "  → Admin Console: https://your-domain:8084/s/admin"
    echo "  → Public Feeds: https://your-domain:8084/s/feed/{client}"
    echo "  → Share Links: https://your-domain:8084/s/x/{id}?t={token}"
    echo "  → Static Sites: https://your-domain:8084/c/{client}/"
    echo "  → Self-Service: https://your-domain:8084/s/signup"
    echo
}

# Display operational procedures
function show_operational_procedures() {
    echo "OPERATIONAL PROCEDURES:"
    echo "--------------------------------------------"
    echo "1. Environment Setup:"
    echo "   export STENELLA_SECRET='a-master-secret-at-least-32-bytes-long!!'"
    echo "   STENELLA_ADMIN_PASSWORD='change-me' ./stenella-server \\"
    echo "     --root=/app/data --port=8084 --libs-dir=static"
    echo
    echo "2. System Health Check:"
    echo "   curl -s http://localhost:8084/ && echo ' System is responding'"
    echo
    echo "3. Access Control:"
    echo "   - Public routes: / (homepage), /s/feed/, /s/x/, /c/{client}/"
    echo "   - Auth required: /s/portal, /s/admin, /s/api/*"
    echo "   - Admin only: /s/admin, atp /api/*"
    echo
}

# Display security information
function show_security_info() {
    echo "SECURITY INFORMATION:"
    echo "--------------------------------------------"
    echo "Access Classification:"
    echo "  • Public: Anyone (homepage, combined feeds, shares)"
    echo "  • Client: Authenticated client session (/s/portal)"
    echo "  • Protected: Client + named clients (/s/portal)"
    echo "  • Admin: atp admin session only (/s/admin)"
    echo
    echo "Encryption:"
    echo "  • AES-256-GCM at rest for all content"
    echo "  • Browser encrypts authored content before submission"
    echo "  • No server-side plaintext search"
    echo "  • Master secret requirement: --secret or \$STENELLA_SECRET"
    echo
}

# Start the stenella server function
function start_server() {
    echo "STARTING STENELLA SERVER..."
    echo "Press Ctrl+C to stop"
    echo
    
    # Check if stenella-server binary exists
    if [[ ! -f "./stenella-server" ]]; then
        echo "ERROR: stenella-server binary not found!"
        echo "Run: go build -o stenella-server ./main.go"
        return 1
    fi

    # Set environment variables if not provided
    if [[ -z "${STENELLA_SECRET:-}" ]]; then
        STENELLA_SECRET="a-master-secret-at-least-32-bytes-long!!"
        export STENELLA_SECRET
    fi

    if [[ -z "${STENELLA_ADMIN_PASSWORD:-}" ]]; then
        STENELLA_ADMIN_PASSWORD="admin"
        export STENELLA_ADMIN_PASSWORD
    fi

    echo "Environment Configuration:"
    echo "  STENELLA_SECRET: ${STENELLA_SECRET}"
    echo "  STENELLA_ADMIN_PASSWORD: ${STENELLA_ADMIN_PASSWORD}"
    echo

    # Start the server with user-specified or default parameters
    STENELLA_ADMIN_PASSWORD="change-me" ./stenella-server \
        --root=./data \
        --port=${STENELLA_PORT:-8084} \
        --libs-dir=./static
}

# Show help information
function show_help() {
    echo "stenella System Runner - Help"
    echo "===================================="
    echo "USAGE: ./run-system.sh [OPTIONS]"
    echo
    echo "COMMANDS:"
    echo "  (no arguments)    Display system information and port usage"
    echo "  --start           Start the stenella server"
    echo "  --help            Show this help message"
    echo "  --status          Check running processes on port 8084"
    echo "  --version         Show system version and capabilities"
    echo
    echo "ENVIRONMENT VARIABLES:"
    echo "  STENELLA_SECRET     Master encryption secret (≥32 characters)"
    echo "  STENELLA_ADMIN_PASSWORD Admin password (default: admin)"
    echo "  STENELLA_PORT       Listen port (default: 8084)"
    echo "  STENELLA_ROOT       Data root directory (default: ./data)"
    echo
}

# Check server status
function check_status() {
    echo "SERVER STATUS CHECK:"
    echo "--------------------------------------------"
    
    # Check for running stenella-server process
    if pgrep -f "stenella-server" > /dev/null; then
        echo "✓ stenella-server is running"
        echo "  PID: $(pgrep -f "stenella-server")"
        
        # Check port 8084 status
        if ss -tuln | grep :8084 > /dev/null; then
            echo "✓ Port 8084 is listening"
        else
            echo "⚠ Port 8084 is not listening (but server is running)"
        fi
        
        # System information
        echo "  Data Root: $(pwd)/data"
        echo "  Binary: $(pwd)/stenella-server"
        
    else
        echo "✗ stenella-server is not running"
        echo "  Run: ./run-system.sh --start"
    fi
    echo
}

# Show system version and capabilities
function show_version() {
    echo "STENELLA SYSTEM VERSION"
    echo "===================================="
    echo "Platform: AzzurroTech Data Platform"
    echo "Version: Production Ready (2026.10.07)"
    echo
    echo "CORE MODULES:"
    echo "  • stenella (this module)"
    echo "  • atp (embedded orchestrator)"
    echo "  • pod (embedded database)"
    echo "  • shepherd (embedded middleware)"
    echo "  • song (embedded static hosting)"
    echo
    echo "PLATFORM FEATURES:"
    echo "  • RSS/Atom/JSON-Feed/Web page aggregation"
    echo "  • AES-256-GCM encryption at rest"
    echo "  • Client collaboration (comments, pins, links)"
    echo "  • Hourly retention sweeps with tombstones"
    echo "  • Siloed static site hosting per client"
    echo "  • Search/link homepage over all hosted content"
    echo "  • Token-protected share URLs"
    echo "  • Link junction tracking to original sources"
    echo "  • Usage logging and billing aggregation"
    echo "  • Self-service client provisioning"
    echo
}

# Parse command line arguments
function parse_arguments() {
    if [[ $# -eq 0 ]]; then
        show_system_info
        show_port_usage
        show_security_info
        show_help
        return 0
    fi

    case "$1" in
        --start)
            start_server
            ;;
        --status)
            check_status
            ;;
        --version)
            show_version
            ;;
        --help)
            show_help
            ;;
        --info)
            show_system_info
            show_port_usage
            show_security_info
            ;;
        --procedures)
            show_operational_procedures
            ;;
        *)
            echo "Unknown option: $1"
            show_help
            exit 1
            ;;
    esac
}

# Main script execution
function main() {
    parse_arguments "$@"
}

# Execute main function
main "$@"
# stenella Operational Runbook

## Overview
stenella is the AzzurroTech data platform that embeds atp (orchestrator) and provides comprehensive data aggregation, encryption, and collaboration features. This runbook documents operational procedures for production deployment and management.

## System Architecture

### Components
- **stenella** — Main platform layer (this module)
- **atp** (embedded) — Orchestrator with embedded:
  - **pod** — Database with per-client record storage
  - **shepherd** — Auth, routing, usage, and security middleware  
  - **song** — Static site hosting per client
- **web** — HTTP layer with:
  - Client portal (`/s/portal?client={client}`)
  - Admin console (`/s/admin`)
  - Public feeds (`/s/feed/{client}`, `/s/x/{id}?t={token}`)
  - Self-service signup (`/s/signup`)

### Data Flow
1. **Ingestion** — Sources fetched via HTTP(S) with SSRF protection
2. **Processing** — Items encrypted at rest, metadata stored in pod
3. **Distribution** — Public feeds served, shares provided via tokens
4. **Collaboration** — Comments encrypted by browser before submission

## Deployment

### Prerequisites
- 64-bit Linux/Unix system
- Go 1.22+ (standard library only)
- Minimum 32-byte master secret: `STENELLA_SECRET`
- Admin password: `STENELLA_ADMIN_PASSWORD` (or default: "admin")

### Installation
```bash
cd stenella
go build -o stenella-server ./main.go
```

### Initial Setup
```bash
export STENELLA_SECRET='a-master-secret-at-least-32-bytes-long!!'
STENELLA_ADMIN_PASSWORD='change-me' ./stenella-server \
  --root=/app/data --port=8084 --libs-dir=static
```

### Environment Variables
| Variable | Description | Default/Fallthrough |
|----------|-------------|-------------------|
| `STENELLA_SECRET` | Master AES/HMAC secret (≥32 bytes) | Runtime fatal error |
| `STENELLA_ADMIN_PASSWORD` | Initial admin password | "admin" |
| `STENELLA_TRUST_PROXY` | Trust X-Forwarded-For header | false |
| `STENELLA_ALLOW_PRIVATE_FETCH` | SSRF guard bypass (dev only) | false |

### Port Configuration
- **Default**: 8084 (configurable via `--port`)
- **Access**: External TLS termination required (Caddy reverse proxy recommended)

## Security Model

### Access Classification
| Class | Reader | Unauthenticated Endpoint |
|-------|--------|------------------------|
| `public` | anyone | `/`, `/s/feed/{client}`, `/s/x/{id}?t={token}` |
| `private` | client owner | `/s/portal?client={client}` |
| `protected` | client + named clients | Client portal only |

### Encryption
- **At Rest**: AES-256-GCM with per-client content keys
- **In Transit**: HTTPS only (TLS required)
- **Body Protection**: Browser-encrypted before submission
- **Search**: Metadata only (browser-side plaintext search)

## Administration

### Client Management
Clients are managed via:
1. **Self-service** at `/s/signup` (public, rate-limited)
2. **Admin console** at `/s/admin` (authenticated as atp admin)

### Key Operations
- **View clients**: `GET /s/api/admin/clients`
- **Create client**: `POST /s/api/admin/client`
- **Client secret**: `GET /s/api/portal/vault` (per-client content key)
- **Usage billing**: `GET /s/api/admin/income`

### Security Operations
- **Secrets vault**: Encrypted with master secret
- **Session management**: HMAC-signed cookies
- **Authorization**: ACL-based with three classes
- **Access controls**: Per-client namespace isolation

## Operational Procedures

### Daily Monitoring
```bash
# Check server health
systemctl status stenella-server

# Monitor logs
journalctl -u stenella-server -f --lines=50

# Check port availability
netstat -tuln | grep :8084
```

### Backup Strategy
- **Data backup**: Full backup of `--root=/app/data` directory
- **Incremental**: Use pod's own replication for client-specific needs
- **Encryption**: Entire backup encrypted with master secret

### Recovery
1. Restore data directory completely
2. Verify secret configuration
3. Start server with existing admin credentials
4. Perform data integrity verification

### Upgrades
```bash
# Standard upgrade procedure
1. Build new binary: go build -o stenella-server ./main.go
2. Shutdown old process gracefully
3. Replace binary with new version
4. Start new binary with same configuration
5. Verify service is healthy
```

## Performance Considerations

### Resource Requirements
- **Memory**: 512MB minimum, 2GB+ recommended
- **Storage**: SSD required (high I/O due to feed fetching)
- **CPU**: 2 cores minimum for concurrent fetches

### Optimization Settings
- **Fetch limits**: 16MB per source, 4MB after parsing
- **Retention**: Configurable per-source, 1-365 days
- **Concurrency**: Per-source rate limits

### Tuning Parameters
```bash
# SSRF guard
--allow-private-fetch=false

# Trust proxy headers
--trust-proxy=false

# Custom retention
Set `default_retention_hours` in atp configuration
```

## Troubleshooting

### Service Not Starting
```bash
# Check configuration
./stenella-server --help

# Environment variable validation
if [ -z "$STENELLA_SECRET" ]; then
    echo "STENELLA_SECRET is required"
    exit 1
fi
```

### Web Interface Issues
1. **Port conflicts**: Verify nothing else listening on configured port
2. **Library loading**: Ensure static libraries exist in `--libs-dir`
3. **Data permissions**: `--root` directory must be writable by the server process

### Security Verification
- **Admin login**: `/s/admin` uses atp admin session
- **Client login**: `/s/portal?client={client}` uses stenella portal session
- **Content access**: Verify cipher text decryption works in browser

## Implementation Guide for New Team Members

### Step 1: Environment Setup
1. Configure master secret and admin password
2. Choose deployment method (binary, Docker, or systemd)
3. Configure data directory permissions

### Step 2: System Integration
1. Set up monitoring and alerting
2. Configure backup procedures
3. Establish networking configurations

### Step 3: Security Hardening
1. Review access classifications
2. Configure TLS termination
3. Set up IP allowlists if needed
4. Document secrets and procedures

### Step 4: Testing
1. **Unit Tests**: `go test ./...`
2. **Integration Tests**: Web interface testing
3. **Load Testing**: Performance validation
4. **Security Testing**: Pen testing and vulnerability assessment

## Documentation References
- **System Architecture**: `README.md` 
- **Security Model**: `SECURITY.md`
- **Module Boundaries**: `scripts/standalone.sh`
- **Development**: `scripts/` directory for other utilities

## Compliance Notes
- **Data classification**: Customer data stored per-client
- **Retention**: Configurable data retention periods
- **Audit**: All access logged (per-client, request/response bytes)
- **Encryption**: Mandatory encryption for all storage
- **Access controls**: Role-based access with three classification levels

## Version Management
- **Go modules**: Standard library only
- **Dependencies**: atp (embedded) → pod/shepherd/song modules
- **Binary compatibility**: Major version when breaking changes
- **Configuration schema**: Backward compatible where possible

## Contact
For operational issues, consult:
- **Architecture**: AzzurroTech Engineering Team
- **Operations**: Platform Operations Team  
- **Security**: Security Response Team

---
*Document Version 1.0*  
*Created: 2026-10-01*  
*Last Updated: 2026-10-07*
*Status: Production Ready*
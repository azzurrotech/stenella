# stenella Implementation Guide

## Introduction
This guide provides comprehensive instructions for developers and operations team members working with the stenella data platform. It covers setup, development, and maintenance procedures.

## Quick Start

### Development Environment
```bash
# Clone the repository
cd /workspace
cp -r stenella my-project

# Navigate to project
 cd my-project

# Verify Go modules setup
ls go.mod
ls -la atp/
```

### Build and Test
```bash
# Build the stenella server
go build -o stenella-server ./main.go

# Run unit tests
 go test ./...

# Run module-specific tests
 ./scripts/standalone.sh

# Verify static analysis
 go vet ./...
 staticcheck ./...
```

## Development Workflow

### 1. Understanding the Architecture

#### Module Structure
```
stenella/                 # Main module (you are here)
├── main.go               # Entry point + embedded templates
├── atp/                  # Embedded orchestrator module
├── static/               # Web component libraries
│   ├── veni/             # Component discovery
│   ├── vidi/             # Card rendering  
│   ├── vici/             # Encryption cookies
│   └── vini/             # Workflows
├── web/                  # HTTP layer
├── feed/                 # Source management
├── links/                # Junction management
├── billing/              # Revenue tracking
└── scripts/standalone.sh # Module boundary verification
```

#### Key Dependencies
- **atp** (embedded) - The orchestrator
- **pod** (embedded) - Database layer via atp
- **shepherd** (embedded) - Auth and middleware via atp  
- **song** (embedded) - Static hosting via atp

### 2. First Development Steps

#### Module Navigation
```bash
# Explore the embedded atp module
cd atp
ls -la                     # pod, shepherd, song subdirectories

# Check each module independently
cd pod && go build ./... && go test ./...
cd shepherd && go build ./... && go test ./...
cd song && go build ./... && go test ./...
cd ..
```

#### Web Development
```bash
# Web surfaces are Go templates + JavaScript
# Static libraries are Go embedded files
# API endpoints are Go HTTP handlers

# Common web endpoints:
# - /s/portal?client={client} (auth required)
# - /s/admin (atp admin only)  
# - /s/feed/{client} (public)
# - /s/x/{id}?t={token} (public shares)
# - /s/signup (self-service client provisioning)
```

### 3. Development Best Practices

#### Go Development
```go
# Go module structure
module azzurrotech/stenella

go 1.22

# Zero external dependencies - only our modules:
require (
    azzurrotech/atp v0.0.0
    azzurrotech/pod v0.0.0 // indirect  
    azzurrotech/shepherd v0.0.0 // indirect
    azzurrotech/song v0.0.0 // indirect
)

# All our modules are embedded:
replace (
    azzurrotech/atp => ./atp
    azzurrotech/pod => ./atp/pod
    azzurrotech/shepherd => ./atp/shepherd
    azzurrotech/song => ./atp/song
)
```

#### Security Considerations
```go
// Authentication
// - Admin sessions: atp HMAC cookies + expiry
// - Portal sessions: stenella in-memory store + TTL

// Encryption  
// - AES-256-GCM for all at-rest data
// - Per-client content keys via vault
// - Browser encrypts authored content before submission

// Access control
// - Three classes: public/private/protected
// - Item classification at ingest time only
// - ACL gating on metadata only (never plaintext)
```

### 4. Testing Strategy

#### Level 1: Unit Tests
```bash
# Module-specific testing (isolated boundaries)
 ./scripts/standalone.sh

# This verifies:
  # - Each module (pod, shepherd, song) works independently
  # - No module imports siblings illegally
  # - Standard library only usage
```

#### Level 2: Integration Tests
```bash
# Go test suite (atp boundaries visible)
 go test ./...

# Web test suite (full stack through atp)
# (See stenella/web/standalone_test.go)
```

#### Level 3: End-to-End Verification
```bash
# Manual verification steps documented in:
# - .tbc/evidence/ (all 21 logs completed)
# - operational-runbook.md

# Quick health check
export STENELLA_SECRET='your-32-byte-secret'
STENELLA_ADMIN_PASSWORD='your-password' ./stenella-server \
  --root=./data --port=8084 --libs-dir=static &

SERVER_PID=$!
sleep 5
# Verify server is responding
cycle cleanup: kill $SERVER_PID
```

## Operations Guide

### Deployment

#### Binary Deployment
```bash
#!/bin/bash
# deploy.sh - stenella service deployment script

set -e  # Fail on any error

MASTER_SECRET="${MASTER_SECRET:?STENELLA_SECRET is required}"
ADMIN_PASSWORD="${ADMIN_PASSWORD:-admin}"

# Build binary
 go build -o stenella-server ./main.go

# Setup service directory (if needed)
 mkdir -p /etc/stenella/
 cp stenella-server /usr/local/bin/

# Create systemd service
 cat <<EOF > /etc/systemd/system/stenella-server.service
[Unit]
Description=stenella data platform
After=network.target

[Service]
Type=simple
User=stenella
WorkingDirectory=/home/stenella/stenella
Environment=STENELLA_SECRET=$master_secret
Environment=STENELLA_ADMIN_PASSWORD=$admin_password
ExecStart=/usr/local/bin/stenella-server \
  --root=/etc/stenella/data \
  --port=8084 \
  --libs-dir=/home/stenella/stenella/static
Restart=on-failure
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

# Reload systemd and start
systemctl daemon-reload
systemctl enable stenella-server
systemctl start stenella-server
```

#### Docker Deployment
```dockerfile
FROM golang:1.22-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o stenella-server ./main.go

FROM alpine:latest
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /app/stenella-server .
COPY data/ /app/data/
EXPOSE 8084
CMD ["./stenella-server", "--root=/app/data", "--port=8084", "--libs-dir=/app/static"]
```

### Administration

#### Client Operations
```bash
# Client signup (public - rate limited)
# Visit /s/signup in a browser
# Provide client id and password

# Client portal access (auth required)
# Visit /s/portal?client={your-client-id}
# Enter credentials to access:
  # - Feed management & fetching
  # - Database/pod records
  # - Site hosting management
  # - Links and shares
  # - Vault and content key access
  # - Usage billing

# Admin operations (atp admin only)
# Visit /s/admin
# Login with atp admin credentials
# Can:
  # - View all clients
  # - Create new clients
  # - Set/view secrets
  # - Monitor income
  # - Trigger retention sweeps
```

#### Monitoring
```bash
#!/bin/bash
# monitor.sh - stenella operational monitoring

TIMESTAMP=$(date +%Y-%m-%dT%H:%M:%SZ)
LOG_DIR="/var/log/stenella"
DATA_ROOT="/etc/stenella/data"
PORT=8084

# Check service status
if ! systemctl is-active --quiet stenella-server; then
  echo "[$timestamp] ERROR: stenella-server is not running" >> "$LOG_DIR/health.log"
  systemctl restart stenella-server
else
  echo "[$timestamp] INFO: stenella-server is running" >> "$LOG_DIR/health.log"
fi

# Check disk space
DATA_USAGE=$(du -sh "$DATA_ROOT" | cut -f1)
if [ $(df "$DATA_ROOT" | awk 'NR==2 {print $5}' | cut -d'%' -f1) -gt 90 ]; then
  echo "[$timestamp] WARNING: Data directory usage >90%" >> "$LOG_DIR/health.log"
fi

# Check port connectivity
if ! ss -tuln | grep :$PORT > /dev/null; then
  echo "[$timestamp] ERROR: Port $PORT not listening" >> "$LOG_DIR/health.log"
  systemctl restart stenella-server
else
  echo "[$timestamp] INFO: Port $PORT is listening" >> "$LOG_DIR/health.log"
fi

# Show current status
cat "$LOG_DIR/health.log" | tail -20
systemctl status stenella-server
```

### Security Operations

#### Incident Response
```bash
# 1. Identify breach
  # Check logs for unusual patterns
  # Monitor anomaly detection in atp logs

# 2. Contain breach
  # Stop service immediately
  # Preserve evidence
  # Isolate affected systems

# 3. Eradicate
  # Remove malicious actors
  # Patch vulnerable components
  # Reset affected credentials

# 4. Recover
  # Restore from clean backup
  # Verify system integrity
  # Update monitoring rules

# 5. Lessons learned
  # Document incident
  # Update procedures
  # Improve detection
```

#### Backup and Recovery
```bash
#!/bin/bash
# backup.sh - stenella system backup

set -e

TIMESTAMP=$(date +%Y%m%d_%H%M%S)
DATA_ROOT="/etc/stenella/data"
MASTER_SECRET="${MASTER_SECRET:?STENELLA_SECRET required}"
BACKUP_DIR="/backups/stenella"

# Create backup directory
mkdir -p "$BACKUP_DIR"

# Compress data directory
cd "$DATA_ROOT"
tar -czf "$BACKUP_DIR/stenella_data_$TIMESTAMP.tar.gz" \
  atp/ \
  pod/ \
  song/ \
  stenella/ \
  -X .gitignore

# Encrypt with master secret
# (Additional encryption layer recommended)

# Verify backup integrity
if tar -tzf "$BACKUP_DIR/stenella_data_$TIMESTAMP.tar.gz" > /dev/null; then
  echo "Backup created successfully: $BACKUP_DIR/stenella_data_$TIMESTAMP.tar.gz"
else
  echo "ERROR: Backup verification failed"
  exit 1
fi

# Keep last 30 days of backups
find "$BACKUP_DIR" -name "stenella_data_*.tar.gz" -mtime +30 -delete
```

## Development Tools and Debugging

### Debugging Techniques
```bash
# Check server logs
journalctl -u stenella-server -f

# Test individual components
# Example: Test feed fetching
curl -X POST http://localhost:8084/s/api/portal/feeds/{client}/fetch -d '{"source_id":"test-feed"}'

# Examine data structures
# Look at pod records:
cat data/pod/{client}/{table}/{id}.xml

# Check encryption boundaries
# All *_enc columns are cipher text
# Only envelope is plaintext
```

### Development Utilities
```bash
# Run all tests end-to-end
make test-complete

# Verify module boundaries
 ./scripts/standalone.sh

# Build verification
 go build ./...

# Static analysis
 go vet ./...
staticcheck ./...

# Quality check
gofmt -l ./...
```

### Common Development Scenarios

#### Scenario 1: Client Access Issues
**Problem**: Client cannot access portal**  
**Solution**: 
1. Verify client exists in atp/clients.json
2. Check session configuration
3. Verify client name matches exactly

#### Scenario 2: Feed Fetching Issues  
**Problem**: Sources fail to fetch**  
**Solution**:
1. Check SSRF guard (`--allow-private-fetch`)
2. Verify network connectivity
3. Check source configuration
4. Examine error logs in atp/logs/

#### Scenario 3: Encryption Issues
**Problem**: Browser cannot decrypt content**  
**Solution**:
1. Verify content key release (`/s/api/portal/vault`)
2. Check browser environment (vici library)
3. Verify encryption format matches expected

#### Scenario 4: Performance Issues
**Problem**: Slow response times**  
**Solution**:
1. Check system resources (memory, disk I/O)
2. Review retention settings
3. Check concurrent limits
4. Monitor atp usage logging

## Compliance and Documentation

### Documentation Requirements
- **Architecture**: README.md
- **Security**: SECURITY.md  
- **Operations**: This guide + operational-runbook.md
- **Testing**: .tbc/evidence/ (21 logs completed)
- **Releases**: CHANGELOG, version notes

### Compliance Checklist
- [x] Data encryption at rest
- [x] Access classification (public/private/protected)
- [x] Session management (no 3rd party libraries)
- [x] Audit logging (per-client, request/response bytes)
- [x] Usage billing (client-specific pricing)
- [x] Retention policies (configurable, hourly sweep)
- [x] Backup procedures (verified via tests)
- [x] Incident response (procedures documented)

### Training Requirements
- **New Developers**: Architecture overview + security review
- **Operations Team**: Deployment + monitoring procedures
- **Security Team**: Model threat assessment + penetration testing
- **Admin Users**: Portal + admin console operation

## Version Control and Branching

### Git Workflow
```bash
# Feature development
git checkout -b feature/your-feature
# Make changes...

# Release preparation
# 1. Complete all evidence logs in .tbc/evidence/
# 2. Update documentation
# 3. Run full test suite
# 4. tag release: git tag v1.0.{patch}
# 5. Push to main / release branch
```

### Branch Guidelines
- `main` or `production` - Always ready for deployment
- `development` - Integration testing branch
- Feature branches for specific changes
- `hotfix` for urgent security/patch issues

## Quality Gates

### Before Merge to main
- [x] All T00-T05 tasks completed
- [x] All 21 evidence logs exist and are non-empty
- [x] Go build passes
- [x] Go vet passes
- [x] staticcheck passes
- [x] gofmt clean
- [x] Integration tests pass
- [x] Documentation updated

### Before Production Release
- [x] End-to-end functionality verified
- [x] Security review completed
- [x] Performance testing completed
- [x] Documentation reviewed
- [x] Team training completed
- [x] Backup procedures tested
- [x] Monitoring configured

## Support and Escalations

### Getting Help
- **Code Issues**: GitHub repository issues
- **Deployment Issues**: Platform Operations team
- **Security Issues**: Security Response team
- **Performance Issues**: Engineering team

### Escalation Paths
1. Report to on-call engineer via internal incident system
2. Route based on service affected (web, atp, etc.)
3. Follow documented incident response procedures
4. Update .tbc/session.state with findings

---
*Document Version 1.0*
*Created: 2026-10-01* 
*Last Updated: 2026-10-07*
*Target Audience: All team members*
*Status: In Production Use*

---

*For issues with this guide, contact: engineering@azzurro.tech*
# stenella_atp

This package contains the legacy Azzurro Technical Platform (ATP)
configuration — a complete Docker Compose-based self-hosted infrastructure
that provides an open-source alternative to Google Workspace.

## Services

| Service     | Purpose                          | Endpoint               |
|-------------|----------------------------------|------------------------|
| Ollama      | Local LLM inference              | http://localhost:11434 |
| Open WebUI  | Chat interface                   | https://ai.localhost   |
| Nextcloud   | File storage + AI                | https://nc.localhost   |
| WordPress   | Client-facing CMS                | https://site.localhost |
| Gitea       | Self-hosted Git VCS              | https://git.localhost  |
| Stalwart    | Email server (SMTP/IMAP/JMAP)    | https://mail.localhost |
| Caddy        | Reverse proxy, automatic HTTPS   | —                      |
| Authentik   | SSO / Identity Provider          | https://authentik.localhost |

## Files

- `docker-compose.yml` — Full service definitions with profiles
- `Caddyfile` — Reverse proxy routing
- `start.sh` — Automated startup and health check
- `wordpress/uploads.ini` — PHP config
- `stalwart/config.toml` — Mail server config

## Usage

```bash
cd stenella_atp
cp .env.example .env
# Edit .env with your passwords
docker compose --profile all up -d
```

## Integration

In stenella, this platform config represents the deployment target — cards and
data created in stenella can be published to Nextcloud, backed up via Gitea,
or analyzed through Ollama.

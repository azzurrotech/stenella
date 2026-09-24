# stenella embeds atp (which embeds song, pod and shepherd) in-process.
# Everything is standard-library only — there are no external modules, so no
# `go mod download` step (the requires are replaced by local submodule paths).
FROM golang:1.22-alpine

WORKDIR /app

COPY go.mod ./
COPY atp ./atp
COPY static ./static
COPY . .

RUN go build -o stenella-server ./main.go

EXPOSE 8084

# Pass credentials via -e STENELLA_SECRET (>= 32 bytes) and -e
# STENELLA_ADMIN_PASSWORD. Shell form is used so the env vars are substituted
# (Docker's exec form does not expand variables). --libs-dir serves the
# veni/vidi/vici/vini JS libraries from the static/ submodule worktrees.
CMD sh -c 'exec /app/stenella-server --port=8084 --root=/app/data --libs-dir=/app/static --public-base="$STENELLA_PUBLIC_BASE" --secret="$STENELLA_SECRET" --admin-pass="$STENELLA_ADMIN_PASSWORD"'
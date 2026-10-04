# log.unionpots.nyc

The Union Pots work log: a private, phone-first record of every piece, its
stages, metadata and photos. See [`plan.md`](plan.md) for the design.

Go + htmx + SQLite. One binary, one data directory.

## Local development

```sh
cd log
go run ./cmd/log hash-password          # prompts; prints a bcrypt hash
export LOG_PASSWORD_HASH='$2a$10$...'   # single quotes: the hash contains $
export LOG_SESSION_SECRET=$(openssl rand -hex 32)
export LOG_BACKUPS=off                  # or LOG_BACKUP_DIR=./data-backups to try backups locally
go run ./cmd/log serve                  # http://localhost:8080, data in ./data
go test ./...
```

## Configuration

| Variable | Purpose |
|---|---|
| `LOG_ADDR` | Listen address. Default `:8080`. |
| `LOG_DATA_DIR` | Holds `log.db` and `photos/`. Default `./data` (`/data` in the image). |
| `LOG_PASSWORD_HASH` | bcrypt hash from `log hash-password`. Required. |
| `LOG_SESSION_SECRET` | ≥32 random characters. Changing it logs out every device. Required. |
| `SPACES_ENDPOINT` | e.g. `https://nyc3.digitaloceanspaces.com` |
| `SPACES_REGION` | e.g. `nyc3` |
| `SPACES_BUCKET` | Bucket name. |
| `SPACES_KEY`, `SPACES_SECRET` | Spaces access key. |
| `LOG_BACKUPS` | `off` to run without backups. Without this or the `SPACES_*` variables, the app refuses to start. |
| `LOG_BACKUP_DIR` | Back up to a local directory instead of Spaces (testing). |
| `LOG_ALLOW_EMPTY_DB` | `1` to start with an empty database even though backups exist. |

## Commands

```
log serve            run the app; backs up hourly when the DB changed, and on shutdown
log backup           back up now (no-op if unchanged)
log restore          restore newest snapshot + photos into LOG_DATA_DIR
  -at 2026-10-01     newest snapshot on or before a date
  -key db/hourly/…   an exact snapshot
log hash-password    read a password, print its bcrypt hash
```

## Deployment (DigitalOcean VM, Docker Compose)

One-time setup:

1. **Spaces**: create a bucket and an access key scoped to it (read, write
   and delete). Turn on versioning. No lifecycle rule is needed: the app
   deletes hourly backups older than 7 days itself, always keeping the newest
   one, and keeps `db/daily/` forever.
2. **DNS**: an A record for `log.unionpots.nyc` pointing at the VM.
3. **Password**: `go run ./cmd/log hash-password` (or
   `docker run --rm -it jamespfennell/log.unionpots.nyc hash-password`), and a
   session secret from `openssl rand -hex 32`.
4. **Compose service**, in the compose file on the VM (keep it `chmod 600`;
   it holds credentials):

   ```yaml
   services:
     log.unionpots.nyc:
       image: jamespfennell/log.unionpots.nyc:latest
       restart: unless-stopped
       environment:
         # Compose interpolates $, so every $ in the bcrypt hash is doubled.
         LOG_PASSWORD_HASH: "$$2a$$10$$..."
         LOG_SESSION_SECRET: "<64 hex chars>"
         SPACES_ENDPOINT: https://nyc3.digitaloceanspaces.com
         SPACES_REGION: nyc3
         SPACES_BUCKET: unionpots-log
         SPACES_KEY: "<key>"
         SPACES_SECRET: "<secret>"
       volumes:
         - ./data:/data
       ports:
         - "127.0.0.1:8081:8080"
       stop_grace_period: 45s   # time for the final backup on shutdown
   ```

   `docker compose config` shows the resolved values; the hash should come
   out with single `$`s. (An `env_file:` avoids the escaping if you prefer.)
5. **Caddy**: add to the Caddyfile and reload:

   ```caddyfile
   log.unionpots.nyc {
       request_body {
           max_size 50MB
       }
       reverse_proxy localhost:8081
   }
   ```

   If Caddy runs in the same compose project, drop the `ports:` mapping and
   use `reverse_proxy log.unionpots.nyc:8080` instead.

Update (CI pushes `jamespfennell/log.unionpots.nyc:latest` on pushes to
`main`):

```sh
docker compose pull log.unionpots.nyc
docker compose up -d log.unionpots.nyc
docker compose logs -f log.unionpots.nyc   # expect "listening" and "backup uploaded"
```

## Restore

Run [`restore.sh`](restore.sh) from the compose directory; the steps and
what to do if something goes wrong are in
[`restore_playbook.md`](restore_playbook.md). Run it once as a drill before
relying on the log, and again after any change to the backup code.

# log.unionpots.nyc

The Union Pots work log: a private, phone-first record of every piece, its
stages, metadata and photos. See [`plan.md`](plan.md) for the design.

Go + htmx + SQLite. One binary, one data directory.

## Local development

```sh
cd log
go run ./cmd/log serve        # http://localhost:8080, password "potter", data in ./data
go run ./cmd/log serve -demo  # the demo: sample data, password shown on the login page
go test ./...
```

With no flags the app runs with the default password and no backups; a
banner says so on every page. To try backups locally add
`-backup-dir ./data-backups`.

## Configuration

Everything is a flag; there are no environment variables. Run
`log <command> -h` for the full list.

**Storage flags**: shared by every command. They can go before the command
(`log -data-dir /data serve`) or after it (`log serve -data-dir /data`).

| Flag | Purpose |
|---|---|
| `-data-dir` | Holds `log.db`, `photos/` and `session-secret`. Default `./data` (`/data` in the image). |
| `-spaces-endpoint` | e.g. `https://nyc3.digitaloceanspaces.com` |
| `-spaces-region` | e.g. `nyc3` |
| `-spaces-bucket` | Bucket name. |
| `-spaces-key`, `-spaces-secret` | Spaces access key. |
| `-backup-prefix` | Folder inside the bucket for this deployment, e.g. `prod` (default: the bucket's top level). Several deployments can share a bucket; each only sees, restores and prunes its own objects. |
| `-backup-dir` | Back up to a local directory instead of Spaces (testing). |

Backups are on when all five `-spaces-*` flags are set (or `-backup-dir`).
With none of them, backups are off and a banner says "Backups disabled". If
only some are set, or the bucket can't be reached, the app still runs, with
a banner saying what's wrong, and keeps retrying every 10 minutes. It only
refuses to start when that could lose data: when `log.db` is missing and the
bucket can't be checked for backups to restore, or when a schema migration
is due and the backup before it fails.

**`serve` flags:**

| Flag | Purpose |
|---|---|
| `-addr` | Listen address. Default `:8080`. |
| `-password-hash` | bcrypt hash from `log hash-password`. Without it the password is `potter`, with a banner warning about it. |
| `-min-piece-id` | Lowest number given to a new piece automatically (default 1). Numbers already used are never reused, and backfill can still use lower ones. |
| `-allow-empty-db` | Start with an empty database even though backups exist. |
| `-demo` | Demo mode: the password `potter` is shown on the login page, the log resets to sample pieces at startup and every day, and photos can't be added or removed. Can't be combined with backup flags or `-password-hash`. |

**Sessions.** Logins are signed with a random secret the app creates on
first start, `<data-dir>/session-secret`. **Log out everywhere** in the menu
replaces it, logging out every device (including the one you're on); so does
deleting the file and restarting. Changing the password also logs everyone
out. (Not offered in the demo.)

## Commands

```
log serve            run the app; backs up every 10 minutes when the DB changed, and on shutdown
log backup           back up now (no-op if unchanged)
log restore          restore newest snapshot + photos into -data-dir
  -at 2026-10-01     newest snapshot on or before a date
  -key db/snapshots/…  an exact snapshot
log hash-password    read a password, print its bcrypt hash
```

## Deployment (DigitalOcean VM, Docker Compose)

One-time setup:

1. **Spaces**: create a bucket and an access key scoped to it (read, write
   and delete). Turn on versioning. No lifecycle rule is needed: the app
   deletes backups older than 30 days itself, always keeping the newest one.
2. **DNS**: an A record for `log.unionpots.nyc` pointing at the VM.
3. **Password**: `go run ./cmd/log hash-password` (or
   `docker run --rm -it jamespfennell/log.unionpots.nyc hash-password`).
4. **Compose service**, in the compose file on the VM (keep it `chmod 600`;
   it holds credentials). The storage flags go in `entrypoint`, so that
   `docker compose run … restore` (see `restore.sh`) uses them too; `serve`'s
   own flags go in `command`:

   ```yaml
   services:
     log.unionpots.nyc:
       image: jamespfennell/log.unionpots.nyc:latest
       restart: unless-stopped
       entrypoint:
         - /app/log
         - -spaces-endpoint=https://nyc3.digitaloceanspaces.com
         - -spaces-region=nyc3
         - -spaces-bucket=unionpots-log
         - -spaces-key=<key>
         - -spaces-secret=<secret>
       command:
         - serve
         # Compose interpolates $, so every $ in the bcrypt hash is doubled.
         - -password-hash=$$2a$$10$$...
         - -min-piece-id=120   # first automatic piece number
       volumes:
         - ./data:/data
       ports:
         - "127.0.0.1:8081:8080"
       stop_grace_period: 45s   # time for the final backup on shutdown
   ```

   `docker compose config` shows the resolved values; the hash should come
   out with single `$`s.

   A demo is the same image with only `command: [serve, -demo]`, its own
   `./demo-data:/data` volume and another port; no entrypoint flags (it
   isn't backed up).
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

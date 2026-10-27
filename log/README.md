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

Everything is a flag or the backup config file; there are no environment
variables. Run `log <command> -h` for the full list.

**Storage flags**: shared by every command. They can go before the command
(`log -data-dir /data serve`) or after it (`log serve -data-dir /data`).

| Flag | Purpose |
|---|---|
| `-data-dir` | Holds `log.db`, `photos/` and `session-secret`. Default `./data` (`/data` in the image). |
| `-backup-config` | YAML file listing where backups go (below). |
| `-backup-dir` | Back up to a local directory instead (testing). |
| `-spaces-*`, `-backup-prefix` | Deprecated: a single bucket, from before `-backup-config`. Can't be combined with it. |

**Backup config.** Every target gets the same backups: each is a bucket
(`endpoint`, `region`, `bucket`, `key`, `secret`) or a local `dir`, and
`prefix` is an optional folder inside it for this deployment, e.g. `prod`.
Several deployments can share a bucket; each only sees, restores and prunes
its own objects. The file holds credentials: keep it `chmod 600` and out of
git.

```yaml
targets:
  - name: spaces
    endpoint: https://nyc3.digitaloceanspaces.com
    region: nyc3
    bucket: unionpots-log
    key: <key>
    secret: <secret>
    prefix: prod
  - name: r2
    endpoint: https://<account-id>.r2.cloudflarestorage.com
    region: auto
    bucket: unionpots-log
    key: <key>
    secret: <secret>
    prefix: prod
```

Unknown or repeated fields are errors. So is any unquoted value YAML would
read as something other than text, such as `012345` (a number) or `true`:
put those in quotes.

Targets are independent and backed up at the same time. Each has its own
change detection (`backup-state-<name>.json` in the data directory),
pruning and status, and each run gives a target at most 5 minutes, so one
that is down or slow doesn't hold up the others. Photos are synced by
listing what each target holds, so a new target, or one that was down,
catches up by itself: the next run uploads the photos it is missing and a
copy of the database.
The Backups page shows each target, and the banner names any that are
failing.

With no backup configuration, backups are off and a banner says "Backups
disabled". If the configuration is broken, or a target can't be reached, the
app still runs, with a banner saying what's wrong, and keeps retrying every
10 minutes. It only refuses to start when that could lose data: when
`log.db` is missing and the configuration is broken or any target can't be
checked for backups to restore, or when a schema migration is due and the
backup before it reaches no target.

**`serve` flags:**

| Flag | Purpose |
|---|---|
| `-addr` | Listen address. Default `:8080`. |
| `-password-hash` | bcrypt hash from `log hash-password`. Without it the password is `potter`, with a banner warning about it. |
| `-public-base-url` | Where the log links each finished piece's public page, e.g. `https://unionpots.nyc` (so the link is `https://unionpots.nyc/p/97`). Default: this server (`/p/97`). Not with `-demo`. |
| `-allow-empty-db` | Start with an empty database even though backups exist. |
| `-demo` | Demo mode: the password `potter` is shown on the login page, the log resets to sample pieces at startup and every hour, and photos can't be added or removed. Can't be combined with backup flags or `-password-hash`. |

**Sessions.** Logins are signed with a random secret the app creates on
first start, `<data-dir>/session-secret`. **Log out everywhere** on the Admin
page replaces it, logging out every device (including the one you're on); so
does deleting the file and restarting. Changing the password also logs everyone
out. (Not offered in the demo.)

## Commands

```
log serve            run the app; backs up every 10 minutes when the DB changed, and on shutdown
log backup           back up now (no-op if unchanged)
log restore          restore the newest snapshot in any target + photos into -data-dir
  -from r2           only look in this target
  -at 2026-10-01     newest snapshot on or before a date
  -key db/snapshots/…  an exact snapshot
log hash-password    read a password, print its bcrypt hash
```

## Deployment (DigitalOcean VM, Docker Compose)

One-time setup:

1. **Buckets**: for each target, create a bucket and an access key scoped to
   it (read, write and delete), and turn on versioning where the provider
   has it. No lifecycle rule is needed: the app deletes backups older than
   30 days itself, always keeping the newest one. List them in a backup
   config file next to the compose file (see Configuration).
2. **DNS**: an A record for `log.unionpots.nyc` pointing at the VM.
3. **Password**: `go run ./cmd/log hash-password` (or
   `docker run --rm -it jamespfennell/log.unionpots.nyc hash-password`).
4. **Compose service**, in the compose file on the VM. The storage flags go
   in `entrypoint`, so that `docker compose run … restore` (see
   `restore.sh`) uses them too; `serve`'s own flags go in `command`. The
   backup config is mounted outside `./data`, since a restore moves that
   aside:

   ```yaml
   services:
     log.unionpots.nyc:
       image: jamespfennell/log.unionpots.nyc:latest
       restart: unless-stopped
       entrypoint:
         - /app/log
         - -backup-config=/config/backups.yml
       command:
         - serve
         # Compose interpolates $, so every $ in the bcrypt hash is doubled.
         - -password-hash=$$2a$$10$$...
       volumes:
         - ./data:/data
         - ./backups.yml:/config/backups.yml:ro
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

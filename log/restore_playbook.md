# Restore playbook

How to restore log.unionpots.nyc from the backups in Spaces. Use it when the
VM or its disk is lost, the database is damaged, or you need to go back to an
earlier state. Run it once as a drill, so the first real restore isn't the
first try.

Run everything on the VM, from the directory containing the compose file.

## 1. Stop the app

```sh
docker compose down log
```

This stops and removes only the log container. The final backup runs on
shutdown. Plain `docker compose down` would also stop everything else in the
project (Caddy, the website). On Compose older than v2.20, use
`docker compose rm -s -f log` instead.

## 2. Move the current data aside

```sh
sudo mv /srv/log.unionpots.nyc /srv/log.unionpots.nyc.old
```

Never delete it first: it's your fallback if the restore goes wrong.
`restore` refuses to run while `log.db` exists, so this step is required.

## 3. Restore

```sh
docker compose run --rm log restore
```

This uses the service's image, environment and volume. It downloads the
newest snapshot, checks its integrity, writes `/srv/log.unionpots.nyc/log.db`,
then downloads all photos.

To restore an older snapshot instead:

```sh
docker compose run --rm log restore -at 2026-10-01      # newest on or before a date
docker compose run --rm log restore -key db/hourly/2026-10-03T14-00-00Z.db.gz
```

To see which snapshots exist (hourly for the last 7 days, plus one per day
forever):

```sh
aws s3 ls --endpoint-url https://nyc3.digitaloceanspaces.com \
  s3://unionpots-log/db/ --recursive
```

## 4. Start the app

```sh
docker compose up -d log
```

Use `up -d`, not `start`: `down` removed the container, and `up` recreates
it.

## 5. Check

```sh
docker compose logs log
```

The logs should show `listening`. Then open https://log.unionpots.nyc and
check a recent piece. If it all looks right, delete the old data:

```sh
sudo rm -rf /srv/log.unionpots.nyc.old
```

## If something goes wrong

- **The restore fails:** nothing has been lost. Put the old data back and
  start again:

  ```sh
  sudo rm -rf /srv/log.unionpots.nyc
  sudo mv /srv/log.unionpots.nyc.old /srv/log.unionpots.nyc
  docker compose up -d log
  ```

- **The app won't start and says "backups exist … run `log restore`":** the
  data directory is empty or missing (e.g. a wrong volume path). Fix the
  path, or do step 3.
- **"integrity check failed":** that snapshot is damaged. Restore an earlier
  one with `-at` or `-key`.

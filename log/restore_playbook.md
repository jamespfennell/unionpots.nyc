# Restore playbook

How to restore log.unionpots.nyc from its backup buckets. Use it when the
VM or its disk is lost, the database is damaged, or you need to go back to an
earlier state. Run it once as a drill, so the first real restore isn't the
first try.

Everything runs on the VM, from the directory containing the compose file.
The compose service is `log.unionpots.nyc` and its data lives in `./data`.

## The short version

```sh
./restore.sh                      # newest snapshot in any bucket
./restore.sh -at 2026-10-01       # newest snapshot on or before a date
./restore.sh -key db/snapshots/2026-10-03T14-00-00Z.db.gz
./restore.sh -from r2             # only look in one bucket (any of the above)
```

[`restore.sh`](restore.sh) asks for confirmation, then does the steps below.
If the restore fails it puts the old data back and starts the app on it.

## Choosing a snapshot

`restore` looks in every bucket in the backup config and takes the newest
matching snapshot from whichever holds it; photos come from whichever bucket
has each one. Stopping the app makes a final backup, so the newest snapshot
is the state the app was just in. That is what you want for a drill or a damaged
database: a damaged database fails the integrity check and isn't uploaded,
so the newest snapshot is still the last good one.

**To go back in time** (e.g. to undo a mistake), pass `-at` or `-key`.
`-at` with *today's* date also matches the backup just made, so to rewind to
earlier today use `-key` (it restores from the first bucket holding that
key). The Backups page (menu) shows, for each bucket, how many copies are
stored and the oldest and newest; copies are taken every 10 minutes when
something changed and kept for 30 days (the newest is never deleted). To
list them:

```sh
aws s3 ls --endpoint-url https://nyc3.digitaloceanspaces.com \
  s3://unionpots-log/db/ --recursive        # with -backup-prefix=prod: s3://unionpots-log/prod/db/
```

## What the script does

1. **Stop the app:** `docker compose down log.unionpots.nyc`. This stops and
   removes only this service, not the rest of the project. (Compose older
   than v2.20: `docker compose rm -s -f log.unionpots.nyc`.)
2. **Move the current data aside:** `./data` becomes
   `./data.old.<timestamp>`. Never delete it first: it's the fallback.
   `restore` refuses to run while `log.db` exists, so this step is required.
3. **Restore:** `docker compose run --rm log.unionpots.nyc restore [-at …|-key …|-from …]`.
   This uses the service's image, entrypoint (with `-backup-config`) and
   volumes. It downloads the
   snapshot, checks its integrity, writes `./data/log.db`, then downloads all
   photos.
4. **Start the app:** `docker compose up -d log.unionpots.nyc`. Use `up -d`,
   not `start`, because `down` removed the container.
5. **Check:** the logs should show `listening`. Open
   https://log.unionpots.nyc and check a recent piece, then delete the old
   copy: `sudo rm -rf ./data.old.<timestamp>`. The files were written by the
   container as root, so this may need `sudo`.

## If something goes wrong

- **The restore fails:** the script puts the old data back and starts the
  app on it. Nothing has been lost. Try an earlier snapshot.
- **"listing backups … unreachable":** a bucket can't be reached, so
  `restore` can't tell whether it holds the newest snapshot, and stops.
  If that bucket is really down, restore from another with `-from <name>`.
- **"integrity check failed":** that snapshot is damaged. Restore an earlier
  one with `-at` or `-key`.
- **The app won't start and says "backups exist … run `log restore`":** the
  data directory is empty or missing (e.g. a wrong volume path in the compose
  file). Fix the path, or run the restore.
- **Doing it by hand:** the steps above, in order.

  To undo a restore you don't want:

  ```sh
  docker compose down log.unionpots.nyc
  sudo rm -rf ./data && mv ./data.old.<timestamp> ./data
  docker compose up -d log.unionpots.nyc
  ```

#!/usr/bin/env bash
# Restore log.unionpots.nyc from its backups in Spaces. See restore_playbook.md.
#
# Run from the directory containing the compose file.
#   ./restore.sh                      newest snapshot
#   ./restore.sh -at 2026-10-01       newest snapshot on or before a date
#   ./restore.sh -key db/snapshots/2026-10-03T14-00-00Z.db.gz
set -euo pipefail

SERVICE=log.unionpots.nyc
DATA=./data
OLD="./data.old.$(date +%Y%m%d-%H%M%S)"   # timestamped so an earlier .old is never overwritten

die() { echo "error: $*" >&2; exit 1; }

docker compose config --services | grep -qx "$SERVICE" \
  || die "no service \"$SERVICE\" here; run this from the compose directory"

echo "This will:"
echo "  1. stop $SERVICE (a final backup runs on shutdown)"
echo "  2. move $DATA to $OLD"
echo "  3. restore from backup into $DATA ${*:+(options: $*)}"
echo "  4. start $SERVICE"
read -r -p "Continue? [y/N] " answer
[[ "$answer" == [yY] ]] || die "aborted"

docker compose down "$SERVICE"

if [[ -e "$DATA" ]]; then
  mv "$DATA" "$OLD"
  echo "Moved current data to $OLD"
fi
mkdir -p "$DATA"

if ! docker compose run --rm "$SERVICE" restore "$@"; then
  echo "Restore failed; putting the old data back." >&2
  rm -rf "$DATA"
  [[ -e "$OLD" ]] && mv "$OLD" "$DATA"
  docker compose up -d "$SERVICE"
  die "restore failed; $SERVICE is running on its previous data"
fi

docker compose up -d "$SERVICE"
sleep 2
docker compose logs --tail 20 "$SERVICE"

echo
echo "Restored. Check https://log.unionpots.nyc, then remove the old data:"
echo "  rm -rf $OLD"

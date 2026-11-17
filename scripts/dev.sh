#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export PGDATA="$root/.local/pgdata"
export PGPORT="${DEV_PG_PORT:-55439}"
export PGHOST=127.0.0.1
export PGUSER=signaldesk

for executable in initdb pg_ctl createdb psql; do
  if ! command -v "$executable" >/dev/null; then
    printf 'Missing %s. Install PostgreSQL 15+ and add its bin directory to PATH.\n' "$executable" >&2
    exit 1
  fi
done

mkdir -p "$root/.local"
if [[ ! -s "$PGDATA/PG_VERSION" ]]; then
  initdb -D "$PGDATA" -A trust --username=signaldesk --no-locale --encoding=UTF8
fi
if ! pg_ctl -D "$PGDATA" status >/dev/null 2>&1; then
  pg_ctl -D "$PGDATA" -l "$root/.local/postgres.log" -o "-h 127.0.0.1 -p $PGPORT" -w start
fi
if [[ "$(psql -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='signaldesk_dev'")" != '1' ]]; then
  createdb signaldesk_dev
fi
printf 'Go server: http://127.0.0.1:8080\nPostgreSQL files: %s\n' "$PGDATA"
printf 'Enter this local database URL in first-run setup:\npostgres://signaldesk@127.0.0.1:%s/signaldesk_dev?sslmode=disable\n' "$PGPORT"
exec "$root/bin/signaldesk" -local -data-dir "$root/.local/config" "$@"

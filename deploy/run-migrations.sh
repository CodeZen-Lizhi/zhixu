#!/bin/sh
set -eu

until pg_isready -h "$POSTGRES_HOST" -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null 2>&1; do
  sleep 1
done

for migration in /migrations/*.sql; do
  [ -f "$migration" ] || continue
  # M1 applies only idempotent Up sections. Goose owns migration history in M3.
  awk '/^-- \+goose Up$/{up=1; next} /^-- \+goose Down$/{up=0} up{print}' "$migration" \
    | PGPASSWORD="$POSTGRES_PASSWORD" psql -v ON_ERROR_STOP=1 \
      -h "$POSTGRES_HOST" -U "$POSTGRES_USER" -d "$POSTGRES_DB"
done

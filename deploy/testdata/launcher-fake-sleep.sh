#!/bin/sh

if [ "${1:-}" = "0.5" ]; then
  exit 0
fi

exec /bin/sleep "$@"

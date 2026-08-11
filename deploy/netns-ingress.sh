#!/bin/sh

set -eu

if [ "${1:-}" = "--worker-sentinel" ]; then
  while true; do
    {
      printf 'HTTP/1.1 200 OK\r\nContent-Length: 3\r\nConnection: close\r\n\r\n'
      cat /app/anchor-health/index.html
    } | nc -l -p 18082 -s 127.0.0.1
  done
fi

# The namespace owner is the only process allowed to install the bridge-peer
# filter. Do not open the ingress listener until the filter succeeded.
XTABLES_LOCKFILE=/run/xtables.lock /app/loopback-firewall.sh

exec setpriv --reuid 10001 --regid 10001 --clear-groups --nnp \
  --inh-caps -all --ambient-caps -all -- \
  socat TCP-LISTEN:8080,fork,reuseaddr TCP:127.0.0.1:8081

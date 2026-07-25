#!/bin/sh

set -eu

readonly port=8080
route="$(ip -4 route show default | awk 'NR == 1 { print $3 " " $5 }')"
gateway="${route%% *}"
interface="${route#* }"

if [ -z "${gateway}" ] || [ -z "${interface}" ] || [ "${gateway}" = "${interface}" ]; then
  echo "loopback firewall could not determine the Docker bridge gateway" >&2
  exit 1
fi

# Docker's host-port forwarding arrives from the bridge gateway. Preserve
# loopback health checks, then reject every other bridge peer before socat
# forwards to the API's loopback listener.
iptables -N ZHIXU_LOOPBACK_PROXY 2>/dev/null || true
iptables -F ZHIXU_LOOPBACK_PROXY
iptables -C INPUT -p tcp --dport "${port}" -j ZHIXU_LOOPBACK_PROXY 2>/dev/null \
  || iptables -I INPUT 1 -p tcp --dport "${port}" -j ZHIXU_LOOPBACK_PROXY
iptables -A ZHIXU_LOOPBACK_PROXY -i lo -j ACCEPT
iptables -A ZHIXU_LOOPBACK_PROXY -i "${interface}" -s "${gateway}" -j ACCEPT
iptables -A ZHIXU_LOOPBACK_PROXY -j DROP

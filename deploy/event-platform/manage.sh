#!/bin/sh
set -eu
cd "$(dirname "$0")"
gateway=${GATEWAY:-apisix}
case "$gateway" in apisix) old=nginx ;; nginx) old=apisix ;; *) echo 'GATEWAY must be apisix or nginx' >&2; exit 2 ;; esac
case "${1:-}" in
  render) python3 render.py --cert-dir "${TLS_DIR:?TLS_DIR required}" ;;
  migrate) docker compose --profile maintenance run --rm migrate ;;
  up) docker compose --profile "$old" stop "$old"
      docker compose --profile "$gateway" --profile observability up -d --build
      docker compose --profile "$gateway" --profile observability up -d --force-recreate "$gateway" ;;
  config) docker compose --profile "$gateway" --profile observability config --quiet ;;
  stop) docker compose --profile apisix --profile nginx --profile observability stop ;;
  *) echo 'usage: GATEWAY=apisix|nginx ./manage.sh render|config|migrate|up|stop' >&2; exit 2 ;;
esac

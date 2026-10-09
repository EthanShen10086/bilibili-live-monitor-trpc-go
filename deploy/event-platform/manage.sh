#!/bin/sh
set -eu
cd "$(dirname "$0")"
gateway=${GATEWAY:-apisix}
cache=${CACHE:-redis}
case "$cache" in memory) cache_profile= ;; redis) cache_profile=cache ;; *) echo 'CACHE must be memory or redis' >&2; exit 2 ;; esac
set -- "${1:-}"
action=$1
if [ -n "$cache_profile" ]; then
  set -- docker compose --profile cache
else
  export EVENT_REDIS_URL=
  set -- docker compose
fi
case "$gateway" in apisix) old=nginx ;; nginx) old=apisix ;; *) echo 'GATEWAY must be apisix or nginx' >&2; exit 2 ;; esac
case "$action" in
  render) python3 render.py --cert-dir "${TLS_DIR:?TLS_DIR required}" ;;
  migrate) "$@" --profile maintenance run --rm migrate ;;
  up) "$@" --profile "$old" stop "$old"
      if [ "$cache" = redis ]; then "$@" up -d --wait redis; fi
      if [ "$cache" = memory ]; then "$@" --profile cache stop redis; fi
      "$@" --profile "$gateway" --profile observability up -d --build
      "$@" --profile "$gateway" --profile observability up -d --force-recreate "$gateway" ;;
  config) "$@" --profile "$gateway" --profile observability config --quiet ;;
  stop) "$@" --profile apisix --profile nginx --profile observability stop ;;
  *) echo 'usage: GATEWAY=apisix|nginx ./manage.sh render|config|migrate|up|stop' >&2; exit 2 ;;
esac

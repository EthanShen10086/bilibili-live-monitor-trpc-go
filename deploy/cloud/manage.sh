#!/bin/sh
set -eu
cd "$(dirname "$0")"
MODE=${1:-}
ACTION=${2:-}
case "$MODE" in
  light) ;;
  platform) ;;
  *) printf '%s\n' 'Usage: manage.sh light|platform prepare|check|migrate|up|down|logs|workers-up|nginx-check|nginx-up'; exit 1 ;;
esac
case "$ACTION" in
  prepare)
    [ ! -e config.yaml ] || { printf '%s\n' 'Existing config.yaml kept; review changes manually.'; exit 1; }
    cp "config.$MODE.yaml" config.yaml
    if [ "$MODE" = platform ]; then
      sed 's/role: both/role: sender/' config.platform.yaml > config.sender.yaml
      sed -e 's/19028/19030/' -e 's/19029/19031/' ../../trpc_go.yaml > trpc.sender.yaml
    fi
    [ -e .env ] || cp .env.example .env
    chmod 600 .env
    printf '%s\n' 'Edit config.yaml and .env before starting. Nginx remains optional.'
    exit 0 ;;
esac
[ -f .env ] && [ -f config.yaml ] || { printf '%s\n' 'Run prepare and fill credentials first.'; exit 1; }
[ "$(stat -c %a .env)" = 600 ] || { printf '%s\n' '.env must have mode 600'; exit 1; }
# Require the chosen deployment profile to match the configured storage.
if [ "$MODE" = platform ]; then
  grep -q 'storage: postgres' config.yaml || { printf '%s\n' 'Expected PostgreSQL config'; exit 1; }
  set -- docker compose --profile platform -f compose.yaml -f platform.compose.yaml
else
  grep -q 'storage: sqlite' config.yaml || { printf '%s\n' 'Expected SQLite config'; exit 1; }
  set -- docker compose -f compose.yaml
fi
case "$ACTION" in
  check) "$@" config --quiet ;;
  migrate)
    [ "$MODE" = platform ] || { printf '%s\n' 'migrate requires platform mode'; exit 1; }
    "$@" up -d --wait postgres redis
    "$@" build monitor
    "$@" run --rm --no-deps --entrypoint /app/monitor monitor --root /app platform-migrate ;;
  up) "$@" config --quiet; "$@" up -d --build ;;
  workers-up)
    [ "$MODE" = platform ] || { printf '%s\n' 'workers require platform profile'; exit 1; }
    "$@" --profile workers up -d --build ;;
  down)
    if [ "$MODE" = platform ]; then "$@" --profile workers --profile nginx down; else "$@" --profile nginx down; fi ;; # volumes retained; never use -v for routine updates
  logs) "$@" logs --tail=100 monitor ;;
  nginx-check|nginx-up)
    [ -s tls/fullchain.pem ] && [ -s tls/privkey.pem ] && [ -s auth/status.htpasswd ] || { printf '%s\n' 'Prepare TLS certificate/key and basic-auth file first.'; exit 1; }
    "$@" --profile nginx run --rm --no-deps nginx nginx -t
    [ "$ACTION" = nginx-check ] || "$@" --profile nginx up -d nginx ;;
  *) printf '%s\n' 'Unknown action'; exit 1 ;;
esac

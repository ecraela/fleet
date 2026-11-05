#!/bin/sh
set -eu
umask 077

service_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
state_dir="$service_root/deploy/local/state.local"
export HAPROXY_UID="$(id -u)" HAPROXY_GID="$(id -g)"

compose() {
  if [ -f "$state_dir/compose.env" ]; then
    docker compose --project-directory "$service_root" --env-file "$state_dir/compose.env" "$@"
  else
    docker compose --project-directory "$service_root" "$@"
  fi
}

case "${1:-up}" in
  up)
    mkdir -p "$state_dir" "$state_dir/agent"
    chmod 700 "$state_dir" "$state_dir/agent"
    compose build manager
    if [ ! -f "$state_dir/admin-token" ]; then
      compose run --rm --no-deps init
    fi
    python3 "$service_root/scripts/demo.py" prepare "$state_dir"
    compose up -d postgres victoriametrics manager ingest demo-backend haproxy
    python3 "$service_root/scripts/demo.py" enroll "$state_dir"
    compose up -d agent
    python3 "$service_root/scripts/demo.py" smoke "$state_dir"
    ;;
  smoke)
    python3 "$service_root/scripts/demo.py" smoke "$state_dir"
    ;;
  console)
    if [ -z "${HAPROXY_CONVERGE_ROOT:-}" ] || [ ! -f "$HAPROXY_CONVERGE_ROOT/compose.haproxy.yaml" ]; then
      echo "Set HAPROXY_CONVERGE_ROOT to a Converge checkout for this optional integration." >&2
      exit 2
    fi
    "$0" up
    python3 "$service_root/scripts/demo.py" prepare-browser "$state_dir"
    compose -f "$service_root/compose.yaml" -f "$service_root/compose.console.yaml" up -d manager
    docker compose --project-directory "$HAPROXY_CONVERGE_ROOT" -f "$HAPROXY_CONVERGE_ROOT/compose.yaml" -f "$HAPROXY_CONVERGE_ROOT/compose.haproxy.yaml" up -d --build api console
    echo "Converge console: ${HAPROXY_BROWSER_ORIGIN:-http://localhost:5173}/app/ (shared HAProxy login enabled)."
    ;;
  outage)
    python3 "$service_root/scripts/demo.py" outage "$state_dir"
    ;;
  down)
    compose down
    ;;
  logs)
    compose logs --tail=100 manager ingest agent haproxy
    ;;
  status)
    compose ps
    ;;
  *)
    echo "Usage: $0 {up|console|smoke|outage|down|logs|status}" >&2
    exit 2
    ;;
esac

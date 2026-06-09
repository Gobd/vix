#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PLIST_SRC="$SCRIPT_DIR/dev.vixd.plist"
PLIST_DST="$HOME/Library/LaunchAgents/dev.vixd.plist"
LABEL="dev.vixd"
REPO="$(cd "$SCRIPT_DIR/../.." && pwd)"

usage() {
    echo "Usage: $0 <install|uninstall|restart|status|logs>"
    echo ""
    echo "  install    Copy plist, load service, start vixd at login"
    echo "  uninstall  Unload and remove the service"
    echo "  restart    Stop and restart the running daemon"
    echo "  status     Show whether vixd is running and its PID"
    echo "  logs       Tail /tmp/vixd.log"
    exit 1
}

cmd="${1:-}"
[ -z "$cmd" ] && usage

case "$cmd" in
REPO="$(cd "$(dirname "$0")/../.." && pwd)"

install)
    USER_PATH="$($SHELL -l -c 'echo $PATH' 2>/dev/null || echo "$PATH")"
    sed -e "s|__HOME__|$HOME|g" \
        -e "s|__VIX_REPO__|$REPO|g" \
        -e "s|__PATH__|$USER_PATH|g" \
        "$PLIST_SRC" > "$PLIST_DST"
    launchctl unload "$PLIST_DST" 2>/dev/null || true
    launchctl load -w "$PLIST_DST"
    echo "vixd service installed and started."
    echo "Logs: tail -f /tmp/vixd.log"
    ;;
uninstall)
    launchctl unload -w "$PLIST_DST" 2>/dev/null || true
    rm -f "$PLIST_DST"
    echo "vixd service removed."
    ;;
restart)
    launchctl stop "$LABEL" 2>/dev/null || true
    launchctl start "$LABEL"
    echo "vixd restarted."
    ;;
status)
    if launchctl list "$LABEL" &>/dev/null; then
        pid=$(launchctl list "$LABEL" | awk 'NR==2{print $1}')
        if [ "$pid" != "-" ] && [ -n "$pid" ]; then
            echo "vixd is running (PID $pid)"
        else
            exit_code=$(launchctl list "$LABEL" | awk 'NR==2{print $2}')
            echo "vixd is stopped (last exit code: $exit_code)"
        fi
    else
        echo "vixd service is not installed"
    fi
    ;;
logs)
    tail -f /tmp/vixd.log
    ;;
*)
    usage
    ;;
esac

#!/system/bin/sh

BUSYBOX=/system/bin/busybox
ROOT=${MEDIA_SERVER_ROOT:-/data/server}
BIN="$ROOT/bin/media-server"
RUN_DIR="$ROOT/run"
LOG_DIR="$ROOT/logs"
PID_FILE="$RUN_DIR/media-server.pid"
LOG_FILE="$LOG_DIR/media-server.log"
PORT=${MEDIA_SERVER_PORT:-8088}

"$BUSYBOX" mkdir -p "$RUN_DIR" "$LOG_DIR" "$ROOT/bin" 2>/dev/null

is_running() {
    pid="$1"
    [ -n "$pid" ] || return 1
    "$BUSYBOX" kill -0 "$pid" 2>/dev/null
}

read_pid() {
    [ -f "$PID_FILE" ] || return 1
    "$BUSYBOX" cat "$PID_FILE" 2>/dev/null
}

start_server() {
    pid=$(read_pid)
    if is_running "$pid"; then
        echo "media-server already running (pid $pid, port $PORT)"
        return 0
    fi
    "$BUSYBOX" rm -f "$PID_FILE"

    if [ ! -x "$BIN" ]; then
        echo "media-server binary not found or not executable: $BIN" >&2
        return 1
    fi

    echo "[$($BUSYBOX date '+%Y-%m-%d %H:%M:%S')] starting media-server on port $PORT" >> "$LOG_FILE"
    MEDIA_SERVER_PORT="$PORT" \
    MEDIA_MOVIE_ROOT="/mnt/sda/sda1/电影电视剧" \
    MEDIA_SERIES_ROOT="/mnt/sda/sda1/番剧" \
    "$BUSYBOX" nohup "$BIN" >> "$LOG_FILE" 2>&1 </dev/null &
    pid=$!
    echo "$pid" > "$PID_FILE"
    "$BUSYBOX" sleep 1
    if is_running "$pid"; then
        echo "media-server started (pid $pid, port $PORT)"
        return 0
    fi
    echo "media-server failed to start; see $LOG_FILE" >&2
    "$BUSYBOX" rm -f "$PID_FILE"
    return 1
}

stop_server() {
    pid=$(read_pid)
    if ! is_running "$pid"; then
        "$BUSYBOX" rm -f "$PID_FILE"
        echo "media-server is not running"
        return 0
    fi
    "$BUSYBOX" kill "$pid" 2>/dev/null
    "$BUSYBOX" sleep 1
    if is_running "$pid"; then
        echo "media-server did not stop (pid $pid)" >&2
        return 1
    fi
    "$BUSYBOX" rm -f "$PID_FILE"
    echo "media-server stopped"
}

status_server() {
    pid=$(read_pid)
    if is_running "$pid"; then
        echo "media-server running (pid $pid, port $PORT)"
        return 0
    fi
    "$BUSYBOX" rm -f "$PID_FILE"
    echo "media-server stopped"
    return 1
}

refresh_library() {
    "$BUSYBOX" wget -T 10 -O - --post-data='' "http://127.0.0.1:$PORT/api/refresh" 2>/dev/null
}

case "$1" in
    start)
        start_server
        ;;
    stop)
        stop_server
        ;;
    restart)
        stop_server
        start_server
        ;;
    status)
        status_server
        ;;
    logs)
        "$BUSYBOX" tail -n 100 "$LOG_FILE" 2>/dev/null
        ;;
    refresh)
        refresh_library
        ;;
    *)
        echo "usage: mediactl {start|stop|restart|status|logs|refresh}" >&2
        exit 2
        ;;
esac

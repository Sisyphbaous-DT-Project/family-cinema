#!/system/bin/sh

# pansou 控制脚本：盘搜（网盘资源搜索，8888 端口）独立于媒体服务（8088），
# 片库页的「网盘搜索」直接调它。配置在 /data/server/pansou/pansou.env（改完 restart 生效）。

BUSYBOX=/system/bin/busybox
BIN=/data/server/bin/pansou
HOME_DIR=/data/server/pansou
ENV_FILE=$HOME_DIR/pansou.env
PID_FILE=/data/server/run/pansou.pid
LOG_FILE=/data/server/logs/pansou.log

is_running() {
    pid="$1"
    [ -n "$pid" ] || return 1
    "$BUSYBOX" kill -0 "$pid" 2>/dev/null
}

read_pid() {
    [ -f "$PID_FILE" ] || return 1
    "$BUSYBOX" cat "$PID_FILE" 2>/dev/null
}

start_pansou() {
    pid=$(read_pid)
    if is_running "$pid"; then
        echo "pansou already running (pid $pid)"
        return 0
    fi
    "$BUSYBOX" rm -f "$PID_FILE"
    if [ ! -x "$BIN" ]; then
        echo "pansou binary not found: $BIN" >&2
        return 1
    fi
    if [ ! -f "$ENV_FILE" ]; then
        echo "pansou config not found: $ENV_FILE" >&2
        return 1
    fi
    "$BUSYBOX" mkdir -p "$HOME_DIR/cache" /data/server/run /data/server/logs
    # 日志超过 5MB 就从头记（盘搜的日志没什么要长期留的）
    if [ -f "$LOG_FILE" ] && [ "$("$BUSYBOX" stat -c %s "$LOG_FILE")" -gt 5242880 ]; then
        "$BUSYBOX" mv -f "$LOG_FILE" "$LOG_FILE.old"
    fi
    (
        cd "$HOME_DIR" || exit 1
        . "$ENV_FILE"
        exec "$BUSYBOX" nohup "$BIN" >> "$LOG_FILE" 2>&1 </dev/null
    ) &
    pid=$!
    echo "$pid" > "$PID_FILE"
    "$BUSYBOX" sleep 2
    if is_running "$pid"; then
        echo "pansou started (pid $pid)"
        return 0
    fi
    echo "pansou failed to start; see $LOG_FILE" >&2
    "$BUSYBOX" rm -f "$PID_FILE"
    return 1
}

stop_pansou() {
    pid=$(read_pid)
    if ! is_running "$pid"; then
        "$BUSYBOX" rm -f "$PID_FILE"
        echo "pansou is not running"
        return 0
    fi
    "$BUSYBOX" kill "$pid" 2>/dev/null
    "$BUSYBOX" sleep 1
    if is_running "$pid"; then
        echo "pansou did not stop (pid $pid)" >&2
        return 1
    fi
    "$BUSYBOX" rm -f "$PID_FILE"
    echo "pansou stopped"
}

case "$1" in
    start)   start_pansou ;;
    stop)    stop_pansou ;;
    restart) stop_pansou; start_pansou ;;
    status)
        pid=$(read_pid)
        if is_running "$pid"; then
            echo "pansou running (pid $pid, port 8888)"
        else
            "$BUSYBOX" rm -f "$PID_FILE"
            echo "pansou stopped"
        fi
        ;;
    logs)    "$BUSYBOX" tail -n "${2:-50}" "$LOG_FILE" ;;
    *) echo "usage: pansouctl {start|stop|restart|status|logs [N]}" >&2; exit 2 ;;
esac

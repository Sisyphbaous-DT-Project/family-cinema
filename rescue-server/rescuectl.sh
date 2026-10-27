#!/system/bin/sh

# rescue-server 控制脚本：救援服务（8089）独立于媒体服务（8088），
# 媒体服务挂掉时它必须活着，手机才能打开工具箱做修复。

BUSYBOX=/system/bin/busybox
BIN=/data/server/bin/rescue-server
PID_FILE=/data/server/run/rescue-server.pid

is_running() {
    pid="$1"
    [ -n "$pid" ] || return 1
    "$BUSYBOX" kill -0 "$pid" 2>/dev/null
}

read_pid() {
    [ -f "$PID_FILE" ] || return 1
    "$BUSYBOX" cat "$PID_FILE" 2>/dev/null
}

start_rescue() {
    pid=$(read_pid)
    if is_running "$pid"; then
        echo "rescue-server already running (pid $pid)"
        return 0
    fi
    "$BUSYBOX" rm -f "$PID_FILE"
    if [ ! -x "$BIN" ]; then
        echo "rescue-server binary not found: $BIN" >&2
        return 1
    fi
    "$BUSYBOX" nohup "$BIN" >> /data/server/logs/rescue-server.log 2>&1 </dev/null &
    pid=$!
    echo "$pid" > "$PID_FILE"
    "$BUSYBOX" sleep 1
    if is_running "$pid"; then
        echo "rescue-server started (pid $pid)"
        return 0
    fi
    echo "rescue-server failed to start; see /data/server/logs/rescue-server.log" >&2
    "$BUSYBOX" rm -f "$PID_FILE"
    return 1
}

stop_rescue() {
    pid=$(read_pid)
    if ! is_running "$pid"; then
        "$BUSYBOX" rm -f "$PID_FILE"
        echo "rescue-server is not running"
        return 0
    fi
    "$BUSYBOX" kill "$pid" 2>/dev/null
    "$BUSYBOX" sleep 1
    if is_running "$pid"; then
        echo "rescue-server did not stop (pid $pid)" >&2
        return 1
    fi
    "$BUSYBOX" rm -f "$PID_FILE"
    echo "rescue-server stopped"
}

case "$1" in
    start)   start_rescue ;;
    stop)    stop_rescue ;;
    restart) stop_rescue; start_rescue ;;
    status)
        pid=$(read_pid)
        if is_running "$pid"; then
            echo "rescue-server running (pid $pid, port 8089)"
        else
            "$BUSYBOX" rm -f "$PID_FILE"
            echo "rescue-server stopped"
        fi
        ;;
    *) echo "usage: rescuectl {start|stop|restart|status}" >&2; exit 2 ;;
esac

#!/bin/bash
# 两架构共享的启动入口；只处理首次配置后的进程交接，不管理业务配置。
set -u
# 私有运行文件使用 077，主程序仍沿用平台传入的掩码。
APP_UMASK=$(umask)
umask 077

fail() {
    echo "$*" >&2
    if [[ -n "${TRIM_TEMP_LOGFILE:-}" ]]; then
        printf '%s\n' "$*" >> "$TRIM_TEMP_LOGFILE"
    fi
    return 1
}

if [[ "${TRIM_APPDEST:-}" != /* || ! -d "$TRIM_APPDEST" ||
      "${TRIM_PKGVAR:-}" != /* || "$TRIM_PKGVAR" == / || ! -d "$TRIM_PKGVAR" || ! -w "$TRIM_PKGVAR" ]]; then
    fail 'QMediaSync 运行目录不可用，请检查应用安装和目录权限。'
    exit 1
fi

SERVICE_SCRIPT="$TRIM_APPDEST/scripts/fnos-service.sh"
SERVICE_PID_FILE="$TRIM_PKGVAR/service.pid"
APP_PID_FILE="$TRIM_PKGVAR/app.pid"
STOP_FILE="$TRIM_PKGVAR/.service-stopping"
REQUEST_FILE="$TRIM_PKGVAR/.initial-config-restart"
LOG_FILE="$TRIM_PKGVAR/info.log"

# starttime 可区分退出后被系统复用的 PID；僵尸进程不算运行中。
process_token() {
    local stat
    local -a fields
    [[ "$1" =~ ^[1-9][0-9]*$ ]] || return 1
    IFS= read -r stat 2>/dev/null < "/proc/$1/stat" || return 1
    stat=${stat##*) }
    read -r -a fields <<< "$stat"
    [[ "${fields[0]}" != Z && "${fields[0]}" != X ]] || return 1
    printf '%s\n' "${fields[19]}"
}

matches_process() {
    local token
    token=$(process_token "$1") && [[ "$token" == "$2" ]]
}

read_process() {
    local extra
    [[ -f "$1" && ! -L "$1" ]] || return 1
    read -r PROCESS_PID PROCESS_TOKEN extra < "$1" || return 1
    [[ -z "$extra" && "$PROCESS_TOKEN" =~ ^[0-9]+$ ]] || return 1
    matches_process "$PROCESS_PID" "$PROCESS_TOKEN"
}

write_process() {
    local token
    token=$(process_token "$2") || return 1
    printf '%s %s\n' "$2" "$token" > "$1.$$" && mv -f -- "$1.$$" "$1"
}

status_process() {
    read_process "$SERVICE_PID_FILE" && read_process "$APP_PID_FILE"
}

run_service() {
    local child_pid='' child_token='' stopped=0 restarted=0 result=1 request=''
    signal_stop() {
        stopped=1
        : > "$STOP_FILE"
        rm -f -- "$REQUEST_FILE"
        if [[ -n "$child_pid" ]] && matches_process "$child_pid" "$child_token"; then
            kill -TERM "$child_pid" 2>/dev/null || true
        fi
    }
    cleanup() {
        rm -f -- "$REQUEST_FILE" "$APP_PID_FILE" "$SERVICE_PID_FILE"
    }
    trap signal_stop TERM INT HUP
    trap cleanup EXIT
    write_process "$SERVICE_PID_FILE" "$$" || return 1
    cd -- "$TRIM_APPDEST" || return 1
    export QMS_INITIAL_CONFIG_RESTART_FILE="$REQUEST_FILE"

    while [[ "$stopped" == 0 && ! -e "$STOP_FILE" ]]; do
        (umask "$APP_UMASK"; exec ./QMediaSync --guid "${TRIM_UID:-}" --fnos true) &
        child_pid=$!
        child_token=$(process_token "$child_pid") || { wait "$child_pid"; return 1; }
        if ! write_process "$APP_PID_FILE" "$child_pid"; then
            kill -TERM "$child_pid" 2>/dev/null || true
            wait "$child_pid"
            return 1
        fi
        if [[ "$stopped" == 1 || -e "$STOP_FILE" ]]; then
            kill -TERM "$child_pid" 2>/dev/null || true
        fi
        # wait 被 TERM 打断时继续等待子进程，停止命令负责最终超时处理。
        while true; do
            wait "$child_pid"
            result=$?
            matches_process "$child_pid" "$child_token" || break
        done
        rm -f -- "$APP_PID_FILE"
        [[ "$stopped" == 0 && ! -e "$STOP_FILE" ]] || break
        request=''
        if [[ -f "$REQUEST_FILE" && ! -L "$REQUEST_FILE" ]]; then
            request=$(cat -- "$REQUEST_FILE")
        fi
        rm -f -- "$REQUEST_FILE"
        if [[ "$restarted" == 0 && "$request" == "$child_pid" ]]; then
            restarted=1
            continue
        fi
        break
    done
    return "$result"
}

start_process() {
    if status_process; then
        return 0
    fi
    # 旧状态仍有活进程时不再启动第二份实例。
    if read_process "$SERVICE_PID_FILE" || read_process "$APP_PID_FILE"; then
        fail 'QMediaSync 尚未完全停止，请稍后重试。'
        return 1
    fi
    [[ -x "$TRIM_APPDEST/QMediaSync" ]] || { fail '找不到可运行的 QMediaSync，请重新安装应用。'; return 1; }
    rm -f -- "$STOP_FILE" "$REQUEST_FILE" "$SERVICE_PID_FILE" "$APP_PID_FILE" || return 1
    (umask "$APP_UMASK"; exec nohup /bin/bash "$SERVICE_SCRIPT" run) >> "$LOG_FILE" 2>&1 < /dev/null 9>&- &
    local wrapper=$!
    sleep 1
    if status_process && [[ "$(head -n 1 "$SERVICE_PID_FILE")" == "$wrapper "* ]]; then
        return 0
    fi
    kill -TERM "$wrapper" 2>/dev/null || true
    fail 'QMediaSync 启动失败，请查看应用日志后重试。'
    return 1
}

stop_process() {
    local wrapper='' wrapper_token='' child='' child_token='' count=0
    # 先阻止交接，保证配置保存与平台停止同时发生时，以停止为准。
    : > "$STOP_FILE" || return 1
    rm -f -- "$REQUEST_FILE" || return 1
    if read_process "$SERVICE_PID_FILE"; then
        wrapper=$PROCESS_PID
        wrapper_token=$PROCESS_TOKEN
        kill -TERM "$wrapper" 2>/dev/null || true
    fi
    if read_process "$APP_PID_FILE"; then
        child=$PROCESS_PID
        child_token=$PROCESS_TOKEN
        kill -TERM "$child" 2>/dev/null || true
    fi
    while matches_process "$wrapper" "$wrapper_token" || matches_process "$child" "$child_token"; do
        if (( count >= 90 )); then
            fail 'QMediaSync 停止超时，将强制结束。请检查应用日志。'
            matches_process "$child" "$child_token" && kill -KILL "$child" 2>/dev/null
            matches_process "$wrapper" "$wrapper_token" && kill -KILL "$wrapper" 2>/dev/null
            break
        fi
        sleep 1
        ((count += 1))
        if read_process "$APP_PID_FILE" && [[ "$PROCESS_PID" != "$child" ]]; then
            child=$PROCESS_PID
            child_token=$PROCESS_TOKEN
            kill -TERM "$child" 2>/dev/null || true
        fi
    done
    rm -f -- "$REQUEST_FILE" "$SERVICE_PID_FILE" "$APP_PID_FILE"
}

case "${1:-}" in
    run) run_service ;;
    start|stop|status)
        exec 9> "$TRIM_PKGVAR/.service-control.lock" || exit 1
        flock -x 9 || exit 1
        case "$1" in
            start) start_process ;;
            stop) stop_process ;;
            status) status_process || exit 3 ;;
        esac
        ;;
    *) exit 1 ;;
esac

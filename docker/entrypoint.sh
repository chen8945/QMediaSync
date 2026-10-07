#!/bin/sh


echo "=== 启动 QMS (Docker 模式) ==="

# 设置环境变量
export DOCKER=1
# 只由支持纯重启的实际运行入口声明，旧入口即使在线更新二进制也不会误报能力。
export QMS_DOCKER_RESTART=1

if [ -n "$GPID" ]; then
    echo "使用GPID: $GPID"
    export GPID
else
    echo "未设置GPID环境变量，使用默认值"
fi

if [ -n "$GUID" ]; then
    echo "使用GUID: $GUID"
    export GUID
else
    echo "未设置GUID环境变量，使用默认值"
fi

# 创建用户和组
group_exists() {
    GROUP_VALUE="$1"
    if command -v getent >/dev/null 2>&1; then
        getent group "$GROUP_VALUE" >/dev/null 2>&1
        return $?
    fi

    awk -F: -v group_value="$GROUP_VALUE" '$1 == group_value || $3 == group_value { found = 1 } END { exit found ? 0 : 1 }' /etc/group
}

setup_user_and_group() {
    if [ -n "$GPID" ]; then
        if ! group_exists "$GPID"; then
            echo "组 $GPID 不存在，创建组..."
            addgroup -g "$GPID" "$GPID" 2>/dev/null || addgroup "$GPID"
            echo "组 $GPID 创建完成"
        else
            echo "组 $GPID 已存在"
        fi
    fi

    if [ -n "$GUID" ]; then
        if ! id "$GUID" >/dev/null 2>&1; then
            echo "用户 $GUID 不存在，创建用户..."
            USER_GROUP=""
            if [ -n "$GPID" ]; then
                USER_GROUP="-G $GPID"
            fi
            adduser -u "$GUID" $USER_GROUP -D "$GUID" 2>/dev/null || adduser -D "$GUID"
            echo "用户 $GUID 创建完成"
        else
            echo "用户 $GUID 已存在"
        fi
    fi
}

setup_user_and_group

check_and_update_ownership() {
    USER_FILE="/app/config/.USER"
    
    CURRENT_GUID="${GUID:-0}"
    CURRENT_GPID="${GPID:-0}"
    CURRENT_ID="${CURRENT_GUID}:${CURRENT_GPID}"
    
    if [ -d "/app/config" ]; then
        if [ -f "$USER_FILE" ]; then
            SAVED_ID=$(cat "$USER_FILE")
            if [ "$SAVED_ID" != "$CURRENT_ID" ]; then
                echo "检测到GUID:GPID变化 ($SAVED_ID -> $CURRENT_ID)，更新目录所有者..."
                chown -R "$CURRENT_GUID:$CURRENT_GPID" /app/config
                echo "所有者更新完成"
                echo "$CURRENT_ID" > "$USER_FILE"
            else
                echo "GUID:GPID未变化 ($CURRENT_ID)"
            fi
        else
            echo "首次记录GUID:GPID: $CURRENT_ID"
            echo "$CURRENT_ID" > "$USER_FILE"
            chown "$CURRENT_GUID:$CURRENT_GPID" "$USER_FILE" 2>/dev/null
            chown -R "$CURRENT_GUID:$CURRENT_GPID" /app/config
             echo "检测到GUID:GPID变化 ($SAVED_ID -> $CURRENT_ID)，更新目录所有者完成"
        fi
    else
        echo "警告: /app/config目录不存在"
    fi
}

check_and_update_ownership

# 启动文件监视
echo "启动文件更新监视器..."
/app/scripts/watch_update.sh &
WATCH_PID=$!
cd /app

handle_signal() {
    echo "收到关闭信号，转发给主进程..."
    if [ -n "$MAIN_PID" ] && kill -0 "$MAIN_PID" >/dev/null 2>&1; then
        kill -TERM "$MAIN_PID"
        wait "$MAIN_PID"
    fi
    if [ -n "$WATCH_PID" ] && kill -0 "$WATCH_PID" >/dev/null 2>&1; then
        kill -TERM "$WATCH_PID"
        wait "$WATCH_PID"
    fi
    exit 0
}

trap 'handle_signal' INT TERM

# 在独立子 shell 中启用失败即退出；替换失败按相反顺序恢复已处理文件。
apply_update() (
    set -e
    moved=""
    rollback_update() {
        status=$?
        if [ "$status" -ne 0 ]; then
            echo "安装更新失败，恢复旧版本..." >&2
            for name in $moved; do
                if ! rm -rf "/app/$name" || ! mv "/app/old/$name" "/app/$name"; then
                    echo "恢复 $name 失败，旧文件保留在 /app/old" >&2
                fi
            done
        fi
        exit "$status"
    }
    trap rollback_update EXIT

    rm -rf /app/update
    mkdir /app/update
    tar -zxf /app/qms.update.tar.gz -C /app/update
    test -f /app/update/QMediaSync
    test -d /app/update/web_statics
    test -f /app/update/scripts/docker-entrypoint.sh
    test -f /app/update/scripts/watch_update.sh
    chmod +x /app/update/QMediaSync /app/update/scripts/*.sh
    rm -rf /app/old
    mkdir /app/old
    for name in QMediaSync web_statics scripts; do
        mv "/app/$name" "/app/old/$name"
        moved="$name $moved"
        mv "/app/update/$name" "/app/$name"
    done
)

# 主循环，确保可以多次更新
while true; do
    # 上一轮或容器上次运行留下的信号不能导致无意重启。
    rm -f /app/config/.restart-request || exit 1
    # 启动主进程，支持GPID和GUID环境变量
    if [ -n "$GUID" ] && [ "$GUID" != "0" ]; then
        echo "使用GUID=$GUID 启动主程序"
        if id "$GUID" >/dev/null 2>&1; then
            echo "切换到用户 $GUID 并启动主程序"
            su-exec "$GUID" /app/QMediaSync --guid "$GUID" &
        else
            echo "用户 $GUID 不存在，直接启动主程序"
            /app/QMediaSync &
        fi
    else
        echo "GUID为0或未设置，使用默认参数启动主程序"
        /app/QMediaSync &
    fi
    MAIN_PID=$!
    echo "主进程ID: $MAIN_PID"

    # 等待主进程退出
    wait $MAIN_PID
    echo "主进程已退出，检查更新包和重启请求..."

    # 如果主进程退出，检查是否有更新
    if [ -f "/app/qms.update.tar.gz" ]; then
        echo "主进程退出，检测到新版本，执行更新..."
        apply_update
        update_status=$?
        # 无论安装结果如何都不重复消费这个包；清理失败只告警。
        rm -f /app/qms.update.tar.gz || echo "清理更新压缩包失败"
        rm -rf /app/update || echo "清理更新目录失败"
        if [ "$update_status" -eq 0 ]; then
            echo "更新完成，准备重启主进程..."
        else
            echo "更新失败，已尝试恢复旧版本，准备重启主进程..." >&2
        fi
        # 继续循环，重启主进程
    elif [ -f /app/config/.restart-request ] && [ "$(cat /app/config/.restart-request)" = "$MAIN_PID" ]; then
        rm -f /app/config/.restart-request || exit 1
        echo "收到重启请求，准备重新启动主进程..."
    else
        echo "未检测到更新包或有效的重启请求，退出容器..."
        exit 0
    fi
done

#!/bin/sh


# 安装 inotify-tools (需要在 Dockerfile 中安装)
if ! command -v inotifywait >/dev/null 2>&1; then
    echo "请安装 inotify-tools"
    exit 1
fi

# 监视新版本文件
inotifywait -m -e create -e moved_to --format "%f" /app |
while IFS= read -r FILE; do
    if [ "$FILE" = "qms.update.tar.gz" ]; then
        echo "检测到完整更新包，准备重启..."
        # 给主进程发送重启信号
        pkill -SIGTERM QMediaSync 2>/dev/null || echo "等待主进程自然重启"
    fi
done

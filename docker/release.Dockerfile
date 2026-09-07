FROM alpine:3.20

ARG TARGETARCH
ARG TARGETOS

ENV TZ=Asia/Shanghai \
    PATH=/app:$PATH

RUN apk add --no-cache ca-certificates tzdata inotify-tools su-exec && \
    mkdir -p /app/scripts && \
    chmod 777 /app

WORKDIR /app
COPY --chmod=0755 temp_build/QMediaSync_linux_${TARGETARCH}_exe ./QMediaSync
COPY backend/web_statics ./web_statics/
COPY --chmod=0755 docker/entrypoint.sh ./scripts/docker-entrypoint.sh
COPY --chmod=0755 docker/watch-update.sh ./scripts/watch_update.sh
COPY backend/assets/db_config.html ./web_statics/
COPY backend/icon.ico .

VOLUME ["/app/config", "/media"]
EXPOSE 12333 8095 8094
CMD ["/app/scripts/docker-entrypoint.sh"]

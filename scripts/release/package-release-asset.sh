#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "Usage: $0 <tag> <os> <arch> <binary> <web_statics> <out_dir>" >&2
}

if [ "$#" -ne 6 ]; then
  usage
  exit 1
fi

TAG="$1"
TARGET_OS="$2"
TARGET_ARCH="$3"
BINARY_PATH="$4"
WEB_STATICS_DIR="$5"
OUT_DIR="$6"

DOCKER_ENTRYPOINT="${DOCKER_ENTRYPOINT:-docker/entrypoint.sh}"
DOCKER_WATCH_UPDATE="${DOCKER_WATCH_UPDATE:-docker/watch-update.sh}"
ICON_PATH="${ICON_PATH:-backend/icon.ico}"

if [ ! -f "$BINARY_PATH" ]; then
  echo "Binary not found: $BINARY_PATH" >&2
  exit 1
fi

if [ ! -d "$WEB_STATICS_DIR" ]; then
  echo "web_statics directory not found: $WEB_STATICS_DIR" >&2
  exit 1
fi

mkdir -p "$OUT_DIR"
OUT_DIR="$(cd "$OUT_DIR" && pwd)"

case "$TARGET_ARCH" in
  amd64) ARCHIVE_ARCH="x86_64" ;;
  arm64) ARCHIVE_ARCH="arm64" ;;
  *)
    echo "Unsupported arch: $TARGET_ARCH" >&2
    exit 1
    ;;
esac

case "$TARGET_OS" in
  windows)
    EXECUTABLE_NAME="QMediaSync.exe"
    ARCHIVE_EXT="zip"
    ;;
  linux)
    EXECUTABLE_NAME="QMediaSync"
    ARCHIVE_EXT="tar.gz"
    ;;
  *)
    echo "Unsupported os: $TARGET_OS" >&2
    exit 1
    ;;
esac

ARCHIVE_NAME="QMediaSync_${TARGET_OS}_${ARCHIVE_ARCH}"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT

mkdir -p "$WORK_DIR/$ARCHIVE_NAME"
cp "$BINARY_PATH" "$WORK_DIR/$ARCHIVE_NAME/$EXECUTABLE_NAME"

if [ "$TARGET_OS" = "linux" ]; then
  chmod +x "$WORK_DIR/$ARCHIVE_NAME/$EXECUTABLE_NAME"
fi

cp -R "$WEB_STATICS_DIR" "$WORK_DIR/$ARCHIVE_NAME/web_statics"

# Docker 在线更新复用 Linux 发布包；Windows 使用原生 EXE 更新流程。
if [ "$TARGET_OS" = "linux" ]; then
  mkdir -p "$WORK_DIR/$ARCHIVE_NAME/scripts"
  cp "$DOCKER_ENTRYPOINT" "$WORK_DIR/$ARCHIVE_NAME/scripts/docker-entrypoint.sh"
  cp "$DOCKER_WATCH_UPDATE" "$WORK_DIR/$ARCHIVE_NAME/scripts/watch_update.sh"
fi

if [ "$TARGET_OS" = "windows" ] && [ -f "$ICON_PATH" ]; then
  cp "$ICON_PATH" "$WORK_DIR/$ARCHIVE_NAME/icon.ico"
fi

case "$ARCHIVE_EXT" in
  zip)
    (cd "$WORK_DIR/$ARCHIVE_NAME" && zip -qr "$OUT_DIR/${ARCHIVE_NAME}.zip" .)
    ;;
  tar.gz)
    tar -czf "$OUT_DIR/${ARCHIVE_NAME}.tar.gz" -C "$WORK_DIR" "$ARCHIVE_NAME"
    ;;
esac

echo "Created $OUT_DIR/${ARCHIVE_NAME}.${ARCHIVE_EXT} for $TAG"

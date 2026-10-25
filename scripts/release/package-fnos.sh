#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 5 ]; then
  echo "Usage: $0 <tag> <arch> <binary> <web_statics> <out_dir>" >&2
  exit 1
fi

TAG="$1"
TARGET_ARCH="$2"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

if [[ ! "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ && ! "$TAG" =~ ^[0-9]+\.[0-9]+\.[0-9]+-beta\.[0-9]{14}\.g[0-9a-f]{7}$ ]]; then
  echo "Invalid release tag: $TAG" >&2
  exit 1
fi
case "$TARGET_ARCH" in
  amd64) ASSET_ARCH=x86_64 ;;
  arm64) ASSET_ARCH=arm64 ;;
  *) echo "Unsupported fnOS arch: $TARGET_ARCH" >&2; exit 1 ;;
esac

if ! command -v fnpack >/dev/null 2>&1; then
  if [ "${REQUIRE_FNPACK:-0}" = "1" ]; then
    echo "fnpack is required but was not found" >&2
    exit 1
  fi
  echo "fnpack not found, skipping fnOS package for ${TARGET_ARCH}"
  exit 0
fi

# 后续在临时目录调用 fnpack，输入路径不能再依赖调用方的当前目录。
BINARY_PATH="$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.argv[1]).resolve())' "$3")"
WEB_STATICS_DIR="$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.argv[1]).resolve())' "$4")"
OUT_DIR="$(python3 -c 'import pathlib,sys; print(pathlib.Path(sys.argv[1]).resolve())' "$5")"
FNOS_ROOT="$ROOT/backend/fnos"
FNOS_VERSION="${TAG#v}"

python3 - "$FNOS_ROOT" "$FNOS_VERSION" "$BINARY_PATH" "$WEB_STATICS_DIR" "$TARGET_ARCH" <<'PY_INPUT'
import hashlib
import json
import re
import struct
import sys
from pathlib import Path

root, version, binary, web, arch = sys.argv[1:]
try:
    source_version = version.split("-beta.", 1)[0]
    for target in ("amd64", "arm64"):
        path = Path(root, f"qmediasync-{target}", "manifest")
        values = re.findall(r"(?m)^\s*version[ \t]*=[ \t]*([^\r\n]*)", path.read_text())
        if len(values) != 1 or values[0].strip() != source_version:
            raise ValueError(f"manifest version must match {source_version}: {path}")
    binary_bytes = Path(binary).read_bytes()
    header = binary_bytes[:20]
    machine = 62 if arch == "amd64" else 183
    if len(header) < 20 or header[:6] != b"\x7fELF\x02\x01" or struct.unpack_from("<H", header, 18)[0] != machine:
        raise ValueError(f"Binary is not a Linux {arch} executable: {binary}")
    # 同一次构建生成的版本记录以哈希绑定程序，防止同架构旧产物混入；不读取或输出构建密钥。
    try:
        metadata = json.loads(Path(binary + ".build.json").read_text())
    except (OSError, ValueError):
        raise ValueError("Binary build metadata is missing or invalid (.build.json)") from None
    if not isinstance(metadata, dict) or not isinstance(metadata.get("version"), str):
        raise ValueError("Binary build metadata version is missing or invalid")
    if metadata["version"].removeprefix("v") != version:
        raise ValueError("Binary build metadata version must match the release tag")
    if metadata.get("goos") != "linux" or metadata.get("goarch") != arch:
        raise ValueError("Binary build metadata platform must match the target architecture")
    if metadata.get("sha256") != hashlib.sha256(binary_bytes).hexdigest():
        raise ValueError("Binary SHA-256 differs from build metadata")
    if not Path(web, "index.html").is_file():
        raise ValueError(f"web_statics/index.html not found: {web}")
    if not Path(root, "common/service.sh").is_file():
        raise ValueError("fnOS common/service.sh not found")
except (OSError, ValueError) as error:
    sys.exit(str(error))
PY_INPUT

STAGING="$(mktemp -d "${TMPDIR:-/tmp}/qms-fnos-package-XXXXXXXX")"
trap 'rm -rf "$STAGING"' EXIT
python3 - "$FNOS_ROOT/qmediasync-$TARGET_ARCH" "$STAGING/package" "$FNOS_VERSION" <<'PY_COPY'
import re
import shutil
import sys
from pathlib import Path
shutil.copytree(sys.argv[1], sys.argv[2], ignore=shutil.ignore_patterns("*.fpk"))
if "-beta." in sys.argv[3]:
    # Beta 版本只写入临时副本，源码 manifest 留给正式发版维护。
    manifest = Path(sys.argv[2], "manifest")
    manifest.write_text(re.sub(r"(?m)^([ \t]*version[ \t]*=[ \t]*)[^\r\n]*", rf"\g<1>{sys.argv[3]}", manifest.read_text()))
PY_COPY
APP_DIR="$STAGING/package/app"
rm -f "$STAGING/package/config.yaml" "$APP_DIR/QMediaSync"
rm -rf "$APP_DIR/web_statics"
mkdir -p "$APP_DIR/scripts"
cp "$BINARY_PATH" "$APP_DIR/QMediaSync"
chmod +x "$APP_DIR/QMediaSync"
cp -R "$WEB_STATICS_DIR" "$APP_DIR/web_statics"
cp "$FNOS_ROOT/common/service.sh" "$APP_DIR/scripts/fnos-service.sh"
chmod +x "$APP_DIR/scripts/fnos-service.sh"
if [ -f "$ROOT/backend/assets/db_config.html" ]; then
  cp "$ROOT/backend/assets/db_config.html" "$APP_DIR/web_statics/"
fi

(cd "$STAGING/package" && fnpack build)
FPK="$STAGING/package/qmediasync.fpk"
python3 - "$FPK" "$FNOS_VERSION" "$TARGET_ARCH" "$BINARY_PATH" "$FNOS_ROOT/common/service.sh" "$WEB_STATICS_DIR" <<'PY_PACKAGE'
import hashlib
import re
import sys
import tarfile
from pathlib import Path, PurePosixPath

package, version, arch, binary, service, web = sys.argv[1:]

def files(archive):
    result = {}
    for member in archive.getmembers():
        path = PurePosixPath(member.name)
        name = str(path)
        if path.is_absolute() or ".." in path.parts or name in result:
            raise ValueError(f"Invalid or duplicate package path: {member.name}")
        result[name] = member
    return result

def require(archive, entries, name):
    member = entries.get(name)
    if member is None or not member.isfile():
        raise ValueError(f"Required package file missing: {name}")
    return archive.extractfile(member).read()

try:
    with tarfile.open(package, "r:*") as outer:
        entries = files(outer)
        manifest = require(outer, entries, "manifest").decode()
        for key, expected in (("appname", "qmediasync"), ("version", version), ("platform", "x86" if arch == "amd64" else "arm")):
            values = re.findall(rf"(?m)^{key}[ \t]*=[ \t]*([^\r\n]*)", manifest)
            if len(values) != 1 or values[0].strip() != expected:
                raise ValueError(f"FPK manifest {key} must match {expected}")
        for name in ("config/privilege", "config/resource", "wizard/install", "wizard/uninstall", "ICON.PNG", "ICON_256.PNG"):
            require(outer, entries, name)
        for hook in ("main", "install_init", "install_callback", "upgrade_init", "upgrade_callback", "config_init", "config_callback", "uninstall_init", "uninstall_callback"):
            require(outer, entries, f"cmd/{hook}")
        payload = entries.get("app.tgz")
        if payload is None or not payload.isfile():
            raise ValueError("app.tgz missing from FPK")
        with tarfile.open(fileobj=outer.extractfile(payload), mode="r:gz") as app:
            contents = files(app)
            inputs = [("QMediaSync", Path(binary)), ("scripts/fnos-service.sh", Path(service))]
            inputs.extend((f"web_statics/{source.relative_to(web).as_posix()}", source) for source in Path(web).rglob("*") if source.is_file())
            for name, source in inputs:
                value = require(app, contents, name)
                if hashlib.sha256(value).digest() != hashlib.sha256(Path(source).read_bytes()).digest():
                    raise ValueError(f"FPK payload differs from build input: {name}")
                if name in ("QMediaSync", "scripts/fnos-service.sh") and not contents[name].mode & 0o111:
                    raise ValueError(f"FPK executable permission missing: {name}")
            for name in ("ui/config", "ui/images/icon_64.png", "ui/images/icon_256.png"):
                require(app, contents, name)
            if "config.yaml" in entries or "config.yaml" in contents:
                raise ValueError("Obsolete business config.yaml must not be packaged")
except (OSError, ValueError, tarfile.TarError, UnicodeError) as error:
    sys.exit(f"Invalid fnOS package: {error}")
PY_PACKAGE

mkdir -p "$OUT_DIR"
ASSET_NAME="QMediaSync_fnos_${ASSET_ARCH}.fpk"
if [[ "$FNOS_VERSION" == *-beta.* ]]; then
  ASSET_NAME="QMediaSync_${FNOS_VERSION}_fnos_${ASSET_ARCH}.fpk"
fi
cp "$FPK" "$OUT_DIR/$ASSET_NAME"
echo "Created $OUT_DIR/$ASSET_NAME"

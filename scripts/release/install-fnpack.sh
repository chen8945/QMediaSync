#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "Usage: $0 <install-dir>" >&2
  exit 1
fi

case "$(uname -s)" in
  Linux) FNPACK_HOST_OS=linux ;;
  Darwin) FNPACK_HOST_OS=darwin ;;
  *) echo "Unsupported fnpack build host OS" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) FNPACK_HOST_ARCH=amd64 ;;
  aarch64|arm64) FNPACK_HOST_ARCH=arm64 ;;
  *) echo "Unsupported fnpack build host architecture" >&2; exit 1 ;;
esac

python3 - "$1" "$FNPACK_HOST_OS-$FNPACK_HOST_ARCH" <<'PY'
import hashlib
import html
import os
import re
import subprocess
import sys
import tempfile
import urllib.request
from pathlib import Path

install_dir, host = Path(sys.argv[1]).resolve(), sys.argv[2]
custom_url = os.environ.get("FNPACK_DOWNLOAD_URL", "").strip()
requested_version = os.environ.get("FNPACK_VERSION", "").strip()
page_url = "https://developer.fnnas.com/docs/cli/fnpack/"
stage = "resolve official version"
temporary = None
try:
    if custom_url:
        url = custom_url
        expected_version = None
        safe_url = "custom download URL (redacted)"
    elif requested_version:
        if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", requested_version):
            raise ValueError("FNPACK_VERSION must be a stable major.minor.patch version")
        expected_version = requested_version
        url = f"https://static2.fnnas.com/fnpack/fnpack-{expected_version}-{host}"
        safe_url = url
    else:
        with urllib.request.urlopen(page_url, timeout=30) as response:
            page = html.unescape(response.read().decode("utf-8"))
        candidates = re.findall(
            rf'https://static2\.fnnas\.com/fnpack/fnpack-([0-9]+\.[0-9]+\.[0-9]+)-{re.escape(host)}(?=["\s<>])',
            page,
        )
        if not candidates:
            raise ValueError(f"No official stable fnpack download for {host}")
        expected_version = max(candidates, key=lambda value: tuple(map(int, value.split("."))))
        url = f"https://static2.fnnas.com/fnpack/fnpack-{expected_version}-{host}"
        safe_url = url

    stage = "download fnpack"
    install_dir.mkdir(parents=True, exist_ok=True)
    fd, temporary = tempfile.mkstemp(prefix=".fnpack-", dir=install_dir)
    with os.fdopen(fd, "wb") as stream, urllib.request.urlopen(url, timeout=30) as response:
        stream.write(response.read())
    os.chmod(temporary, 0o755)
    stage = "run fnpack"
    result = subprocess.run([temporary, "--help"], capture_output=True, text=True, timeout=30, check=True)
    match = re.search(r"(?m)^Version ([0-9]+\.[0-9]+\.[0-9]+)\s*$", result.stdout)
    if not match:
        raise ValueError("Downloaded fnpack did not report a stable version")
    actual_version = match.group(1)
    if expected_version and actual_version != expected_version:
        raise ValueError(f"Downloaded fnpack version {actual_version} differs from {expected_version}")
    digest = hashlib.sha256(Path(temporary).read_bytes()).hexdigest()
    os.replace(temporary, install_dir / "fnpack")
    temporary = None
    record = f"version={actual_version}\nurl={safe_url}\nsha256={digest}\n"
    (install_dir / "fnpack-source.txt").write_text(record)
    print(record, end="")
except (OSError, ValueError, subprocess.SubprocessError) as error:
    # 自定义地址可能包含凭据；网络异常中的完整 URL 不进入日志。
    detail = type(error).__name__ if custom_url else str(error)
    sys.exit(f"Failed to {stage}: {detail}")
finally:
    if temporary and os.path.exists(temporary):
        os.unlink(temporary)
PY

"""生成 Beta 共用版本、UTC 构建时间和 Actions 产物保留期。"""

import os
import re
import sys
from datetime import datetime, timezone
from pathlib import Path


def main():
    retention = (
        os.environ.get("INPUT_RETENTION_DAYS")
        or os.environ.get("BETA_ARTIFACT_RETENTION_DAYS")
        or "90"
    )
    if not re.fullmatch(r"[0-9]{1,2}", retention) or not 1 <= int(retention) <= 90:
        raise ValueError("保留天数必须是 1～90 的整数（手动输入 > BETA_ARTIFACT_RETENTION_DAYS > 90）")

    versions = []
    for arch in ("amd64", "arm64"):
        path = Path(f"backend/fnos/qmediasync-{arch}/manifest")
        values = re.findall(r"(?m)^[ \t]*version[ \t]*=[ \t]*([^\r\n]*)", path.read_text(encoding="utf-8"))
        if len(values) != 1 or not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", values[0].strip()):
            raise ValueError(f"manifest 必须包含一个正式版本号：{path}")
        versions.append(values[0].strip())
    if versions[0] != versions[1]:
        raise ValueError("两种架构的 manifest 基础版本必须一致")

    sha = os.environ.get("GITHUB_SHA", "")
    if not re.fullmatch(r"[0-9a-f]{40}", sha):
        raise ValueError("GITHUB_SHA 必须是完整的 Git 提交哈希")
    now = datetime.now(timezone.utc).replace(microsecond=0)
    version = f"{versions[0]}-beta.{now:%Y%m%d%H%M%S}.g{sha[:7]}"
    print(f"version={version}")
    print(f"build_date={now:%Y-%m-%dT%H:%M:%SZ}")
    print(f"retention_days={int(retention)}")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError) as error:
        sys.exit(str(error))

#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

# 从 git 提交日志自动生成指定版本的 changelog（基于 git-cliff + 仓库根目录的 cliff.toml）。
#
# 用法：
#   scripts/release/gen-changelog.sh <tag>
#   scripts/release/gen-changelog.sh --from-existing <tag>
#
# 默认模式：
#   1) 生成本版本的发布说明，写入 .changes/<tag>.md（release 工作流会读取它作为 GitHub Release 正文）
#   2) 把本版本段落插入 CHANGELOG.md 顶部（# Changelog 标题之后），保留历史内容
#
# --from-existing 模式（供发布脚本在用户编辑发布说明后重新同步）：
#   不运行 git-cliff，把已存在的 .changes/<tag>.md 重新插入 CHANGELOG.md。
#   调用方需先把 CHANGELOG.md 还原到未插入本版本段落的状态。
#
# 依赖：默认模式需要 git-cliff（安装：https://git-cliff.org/docs/installation/，
#       例如 `cargo install git-cliff` / `brew install git-cliff` / `npm i -g git-cliff`）

MODE="full"
if [ "${1:-}" = "--from-existing" ]; then
  MODE="from-existing"
  shift
fi

TAG="${1:-}"
if [ -z "$TAG" ]; then
  echo "用法: $0 [--from-existing] <tag>，例如 $0 v0.14.24" >&2
  exit 1
fi

cd "$ROOT"

CHANGELOG="CHANGELOG.md"
NOTES=".changes/${TAG}.md"

ensure_tag_absent() {
  validate_release_tag "$TAG"
  if git rev-parse -q --verify "refs/tags/${TAG}" >/dev/null; then
    die "版本 ${TAG} 的 git tag 已存在"
  fi
}

insert_notes_into_changelog() {
  # 把 $NOTES 内容插入 $CHANGELOG 的 # Changelog 标题之后；没有标题则重建标题并置顶
  local tmp inserted
  tmp="$(mktemp)"
  inserted=0
  while IFS= read -r line || [ -n "$line" ]; do
    printf '%s\n' "$line" >> "$tmp"
    if [ "$inserted" -eq 0 ] && printf '%s' "$line" | grep -q '^# Changelog'; then
      printf '\n' >> "$tmp"
      cat "$NOTES" >> "$tmp"
      inserted=1
    fi
  done < "$CHANGELOG"

  if [ "$inserted" -eq 0 ]; then
    { printf '# Changelog\n\n'; cat "$NOTES"; printf '\n'; cat "$CHANGELOG"; } > "$tmp"
  fi

  mv "$tmp" "$CHANGELOG"
}

if [ "$MODE" = "from-existing" ]; then
  if [ ! -f "$NOTES" ]; then
    die "发布说明不存在: ${NOTES}"
  fi
  if [ ! -s "$NOTES" ]; then
    say_warn "警告：${NOTES} 为空，将在 ${CHANGELOG} 中插入空段落。" >&2
  fi
  ensure_tag_absent
  if grep -Eq "^## \\[?$(escape_regex "$TAG")\\]?([[:space:]]|$)" "$CHANGELOG"; then
    die "${CHANGELOG} 已包含版本 ${TAG} 段落；请先还原 ${CHANGELOG} 后重试"
  fi
  insert_notes_into_changelog
  say_ok "已将 ${NOTES} 同步到 ${CHANGELOG}"
  exit 0
fi

if ! command -v git-cliff >/dev/null 2>&1; then
  say_err "未找到 git-cliff，请先安装：https://git-cliff.org/docs/installation/" >&2
  exit 1
fi

ensure_new_release_version "$TAG" "$CHANGELOG" ".changes"

mkdir -p .changes

# 1) 生成本版本发布说明（仅上一个 tag 至今的未发布区间，按 cliff.toml 分组），不含 # Changelog 头
git-cliff --unreleased --tag "$TAG" --strip header -o "$NOTES"

if [ ! -s "$NOTES" ]; then
  say_warn "警告：自上一个 tag 以来没有符合 conventional commits 规范的提交，$NOTES 为空。" >&2
fi

# 2) 将本版本段落插入 CHANGELOG.md 顶部（# Changelog 之后），保留历史内容
insert_notes_into_changelog

say_ok "已生成 $NOTES 并更新 $CHANGELOG"
echo "请检查内容后提交：git add $CHANGELOG $NOTES && git commit -m \"chore: release $TAG\""

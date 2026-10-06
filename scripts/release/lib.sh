# shellcheck shell=bash

# 终端着色：仅在标准输出为交互终端且未设置 NO_COLOR 时启用；
# 非交互场景（管道、CI、测试）保持纯文本，输出内容不变
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_RESET=$'\033[0m'
  C_TITLE=$'\033[1;36m'   # 阶段标题与输入提示：粗体青
  C_OK=$'\033[32m'        # 成功结果：绿
  C_WARN=$'\033[33m'      # 警示与回滚提示：黄
  C_ERR=$'\033[1;31m'     # 错误与失败：粗体红
else
  C_RESET=""
  C_TITLE=""
  C_OK=""
  C_WARN=""
  C_ERR=""
fi

say_title() { printf '%s%s%s\n' "$C_TITLE" "$*" "$C_RESET"; }
say_ok()    { printf '%s%s%s\n' "$C_OK" "$*" "$C_RESET"; }
say_warn()  { printf '%s%s%s\n' "$C_WARN" "$*" "$C_RESET"; }
say_err()   { printf '%s%s%s\n' "$C_ERR" "$*" "$C_RESET"; }

die() {
  printf '%s错误: %s%s\n' "$C_ERR" "$*" "$C_RESET" >&2
  exit 1
}

require_command() {
  local name="$1"
  if ! command -v "$name" >/dev/null 2>&1; then
    die "未找到命令 ${name}"
  fi
}

escape_regex() {
  printf '%s' "$1" | sed 's/[][(){}.^$*+?|\\]/\\&/g'
}

is_valid_release_tag() {
  # 校验 tag 格式并把版本号写入 TARGET_MAJOR/MINOR/PATCH；失败时返回非 0 而不终止脚本，
  # 供交互菜单在无效输入后重试。失败时不清空 TARGET_*：推导模式下这些值仍与当前 TAG 对应，
  # 成功路径会连同 TAG 一起重新赋值。
  local tag="$1"
  if [[ ! "$tag" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
    return 1
  fi
  # shellcheck disable=SC2034  # TARGET_* 由本目录其他脚本 source 后读取
  TARGET_MAJOR="${BASH_REMATCH[1]}"
  # shellcheck disable=SC2034
  TARGET_MINOR="${BASH_REMATCH[2]}"
  # shellcheck disable=SC2034
  TARGET_PATCH="${BASH_REMATCH[3]}"
  return 0
}

validate_release_tag() {
  local tag="$1"
  if ! is_valid_release_tag "$tag"; then
    die "tag 格式必须是 v<major>.<minor>.<patch>，例如 v0.15.3；大版本请使用 v16.0.0"
  fi
}

validate_release_input() {
  local value="$1"
  case "$value" in
    patch|minor|major)
      return
      ;;
  esac

  validate_release_tag "$value"
}

parse_version_parts() {
  local tag="$1"
  if [[ ! "$tag" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
    return 1
  fi
  printf '%s %s %s\n' "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}"
}

compare_versions() {
  local left_major="$1"
  local left_minor="$2"
  local left_patch="$3"
  local right_major="$4"
  local right_minor="$5"
  local right_patch="$6"

  if (( left_major > right_major )); then
    printf '1\n'
    return
  fi
  if (( left_major < right_major )); then
    printf -- '-1\n'
    return
  fi
  if (( left_minor > right_minor )); then
    printf '1\n'
    return
  fi
  if (( left_minor < right_minor )); then
    printf -- '-1\n'
    return
  fi
  if (( left_patch > right_patch )); then
    printf '1\n'
    return
  fi
  if (( left_patch < right_patch )); then
    printf -- '-1\n'
    return
  fi
  printf '0\n'
}

latest_semver_tag() {
  local tag
  while IFS= read -r tag; do
    if [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
      printf '%s\n' "$tag"
      return
    fi
  done < <(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname)
}

ensure_new_release_version() {
  local tag="$1"
  local changelog="$2"
  local changes_dir="$3"
  local notes="${changes_dir}/${tag}.md"
  local escaped_tag

  validate_release_tag "$tag"

  if git rev-parse -q --verify "refs/tags/${tag}" >/dev/null; then
    die "版本 ${tag} 的 git tag 已存在"
  fi

  if [ -e "$notes" ]; then
    die "版本 ${tag} 的发布说明已存在: ${notes}"
  fi

  escaped_tag="$(escape_regex "$tag")"
  if [ -f "$changelog" ] && grep -Eq "^## \\[?${escaped_tag}\\]?([[:space:]]|$)" "$changelog"; then
    die "版本 ${tag} 已存在于 ${changelog}"
  fi
}

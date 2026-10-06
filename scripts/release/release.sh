#!/usr/bin/env bash
# -E 让 ERR trap 在函数内部生效：执行阶段的失败报告依赖它
set -Eeuo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
# shellcheck source=lib.sh
source "$SCRIPT_DIR/lib.sh"

REMOTE="origin"
MAIN_BRANCH="main"
DEV_BRANCH="dev"
CHANGELOG_FILE="CHANGELOG.md"

RELEASE_INPUT=""
TAG=""
CURRENT_TAG=""
NOTES_FILE=""
TARGET_MAJOR=""
TARGET_MINOR=""
TARGET_PATCH=""
BUMP_KIND="none"      # 相对当前最新版本的递增类型：none|patch|minor|major
DRY_RUN=0
SIMULATE=0
VERSION_DERIVED=0     # 发布版本来自 patch/minor/major 推导（审查菜单提供返回重选）
EDITED_NOTES=0        # 用户手动编辑过发布说明（中止时保留该文件）
START_BRANCH=""

usage() {
  cat <<'EOF'
用法:
  scripts/release/release.sh [--dry-run | --simulate] <tag|patch|minor|major>

示例:
  scripts/release/release.sh v0.15.3
  scripts/release/release.sh patch
  scripts/release/release.sh --dry-run minor
  scripts/release/release.sh --simulate v0.16.0

说明:
  使用 patch/minor/major 时，脚本会先推导版本并提示确认。
  直接回车使用推导版本，也可以输入 v<major>.<minor>.<patch> 手动覆盖。

  交互约定: 所有确认步骤回车即为安全默认；输错会提示后重试，不会终止脚本；
  q 随时中止。仅在最后执行提交、打 tag、推送等外部动作前需要输入完整 yes。

  --dry-run   只执行检查并预览发布说明，不修改任何文件、分支或远端；
              显式 tag 时无需任何交互。
  --simulate  把仓库镜像克隆到临时沙箱，在其中原样走一遍完整发布流程，
              真实仓库与远端不受影响；结束后默认删除沙箱，也可保留供检查，
              演练可随时用 q 中止。

流程:
  1. 前置检查（命令、工作区、分支、远端状态）
  2. 确定发布版本（推导 + 确认，或直接校验显式 tag）
  3. 同步 main，并把 dev 快进合入 main
  4. 生成 CHANGELOG.md 和 .changes/<tag>.md
  5. 审查发布说明（可编辑、可返回重选版本）
  6. 输入 yes 后提交 release commit 到 main，创建 annotated tag
  7. 推送 main 和 tag，触发 release workflow
  8. 将 release commit 快进同步回 dev 并推送 dev

中止发布会还原 CHANGELOG.md 并切回原分支；手动编辑过的发布说明会保留。
EOF
}

# ---- 交互输入 ----

read_choice() {
  # 读取一行用户输入到全局 REPLY；输入流关闭（EOF）时返回非 0
  REPLY=""
  local line
  if ! IFS= read -r line; then
    return 1
  fi
  REPLY="${line%$'\r'}"
  return 0
}

prompt_invalid() {
  say_err "无效输入: ${REPLY}，请重新选择（q 中止）"
}

abort_on_eof() {
  echo
  abort_release "输入流已关闭，中止发布"
}

# ---- 中止回滚 ----

restore_start_branch() {
  local current
  current="$(git branch --show-current || true)"
  if [ -n "$START_BRANCH" ] && [ "$current" != "$START_BRANCH" ]; then
    if git checkout "$START_BRANCH" >/dev/null 2>&1; then
      say_ok "已切回分支 ${START_BRANCH}"
    else
      say_warn "警告: 切回分支 ${START_BRANCH} 失败，当前在 $(git branch --show-current || true)，请手动处理" >&2
    fi
  fi
}

abort_release() {
  local reason="${1:-已中止发布}"
  echo
  say_warn "$reason"
  if ! git diff --quiet -- "$CHANGELOG_FILE" 2>/dev/null; then
    git restore -- "$CHANGELOG_FILE"
    say_ok "已还原 ${CHANGELOG_FILE} 的未提交改动"
  fi
  if [ -n "$NOTES_FILE" ] && [ -f "$NOTES_FILE" ]; then
    if [ "$EDITED_NOTES" -eq 1 ]; then
      say_warn "手动编辑过的发布说明保留在 ${NOTES_FILE}，可自行取用；重新发布前请删除该文件"
    else
      rm -f "$NOTES_FILE"
      say_ok "已删除自动生成的 ${NOTES_FILE}"
    fi
  fi
  restore_start_branch
  exit 1
}

reset_generated_changes() {
  # 返回重选版本时清掉本次生成的内容，重新开始
  if [ -n "$NOTES_FILE" ] && [ -f "$NOTES_FILE" ]; then
    rm -f "$NOTES_FILE"
    say_ok "已删除 ${NOTES_FILE}（返回重选版本，将重新生成）"
  fi
  if ! git diff --quiet -- "$CHANGELOG_FILE" 2>/dev/null; then
    git restore -- "$CHANGELOG_FILE"
    say_ok "已还原 ${CHANGELOG_FILE} 的未提交改动"
  fi
  NOTES_FILE=""
  EDITED_NOTES=0
}

# ---- 版本确定 ----

derive_release_tag() {
  local bump="$1"
  local current_major current_minor current_patch

  CURRENT_TAG="$(latest_semver_tag)"
  if [ -z "$CURRENT_TAG" ]; then
    die "无法根据最新版本推导 ${bump} 版本：仓库没有 v<major>.<minor>.<patch> 格式的 tag"
  fi

  read -r current_major current_minor current_patch < <(parse_version_parts "$CURRENT_TAG")
  case "$bump" in
    patch)
      TARGET_MAJOR="$current_major"
      TARGET_MINOR="$current_minor"
      TARGET_PATCH="$((current_patch + 1))"
      ;;
    minor)
      TARGET_MAJOR="$current_major"
      TARGET_MINOR="$((current_minor + 1))"
      TARGET_PATCH="0"
      ;;
    major)
      TARGET_MAJOR="$((current_major + 1))"
      TARGET_MINOR="0"
      TARGET_PATCH="0"
      ;;
    *)
      die "未知版本推导参数 ${bump}"
      ;;
  esac

  TAG="v${TARGET_MAJOR}.${TARGET_MINOR}.${TARGET_PATCH}"
}

print_bump_warning() {
  case "$BUMP_KIND" in
    major)
      echo
      say_warn "⚠ 检测到大版本发布(major): ${CURRENT_TAG} -> ${TAG}"
      say_warn "  大版本发布通常表示兼容性或发布节奏变化。"
      ;;
    minor)
      echo
      say_warn "⚠ 检测到 minor 版本发布: ${CURRENT_TAG} -> ${TAG}"
      say_warn "  minor 版本发布通常表示功能级更新。"
      ;;
  esac
}

choose_version_interactively() {
  local bump="$1"
  while true; do
    echo
    echo "根据当前最新版本 ${CURRENT_TAG} 推导发布版本: ${TAG}（${bump} 递增）"
    echo
    echo "  回车 或 y   使用 ${TAG}"
    echo "  <tag>       覆盖为指定版本（格式 v<major>.<minor>.<patch>）"
    echo "  q           中止发布"
    printf '%s请选择: %s' "$C_TITLE" "$C_RESET"
    if ! read_choice; then
      abort_on_eof
    fi
    case "$REPLY" in
      ""|y|Y)
        return 0
        ;;
      q|Q)
        abort_release "已中止发布"
        ;;
      *)
        if is_valid_release_tag "$REPLY"; then
          TAG="$REPLY"
          say_ok "发布版本已覆盖为 ${TAG}"
          return 0
        fi
        prompt_invalid
        ;;
    esac
  done
}

resolve_release_tag() {
  case "$RELEASE_INPUT" in
    patch|minor|major)
      VERSION_DERIVED=1
      derive_release_tag "$RELEASE_INPUT"
      choose_version_interactively "$RELEASE_INPUT"
      ;;
    *)
      validate_release_tag "$RELEASE_INPUT"
      TAG="$RELEASE_INPUT"
      VERSION_DERIVED=0
      ;;
  esac
}

ensure_tag_newer_than_current() {
  local tag="$1"
  local current_major current_minor current_patch comparison

  CURRENT_TAG="$(latest_semver_tag)"
  if [ -z "$CURRENT_TAG" ]; then
    BUMP_KIND="none"
    return
  fi

  read -r current_major current_minor current_patch < <(parse_version_parts "$CURRENT_TAG")
  comparison="$(compare_versions "$TARGET_MAJOR" "$TARGET_MINOR" "$TARGET_PATCH" "$current_major" "$current_minor" "$current_patch")"
  if [ "$comparison" -le 0 ]; then
    die "tag ${tag} 必须大于当前最新版本 ${CURRENT_TAG}"
  fi

  if (( TARGET_MAJOR > current_major )); then
    BUMP_KIND="major"
  elif (( TARGET_MINOR > current_minor )); then
    BUMP_KIND="minor"
  else
    BUMP_KIND="patch"
  fi
}

# ---- 发布前检查 ----

ensure_clean_worktree() {
  if ! git diff --quiet || ! git diff --cached --quiet; then
    die "工作区不干净，请先提交或暂存现有改动"
  fi
}

ensure_branch_exists() {
  local branch="$1"
  if ! git show-ref --verify --quiet "refs/heads/${branch}"; then
    die "本地分支 ${branch} 不存在"
  fi
}

ensure_remote_branch_exists() {
  local remote="$1"
  local branch="$2"
  if ! git show-ref --verify --quiet "refs/remotes/${remote}/${branch}"; then
    die "远端分支 ${remote}/${branch} 不存在"
  fi
}

ensure_branch_contains_remote() {
  local remote="$1"
  local branch="$2"
  if ! git merge-base --is-ancestor "${remote}/${branch}" "$branch"; then
    die "本地 ${branch} 不包含 ${remote}/${branch}，请先同步或处理分叉"
  fi
}

ensure_no_remote_tag() {
  local remote="$1"
  local tag="$2"
  if git ls-remote --exit-code --tags "$remote" "refs/tags/${tag}" >/dev/null 2>&1; then
    die "远端 tag ${tag} 已存在"
  fi
}

# ---- 审查与编辑 ----

show_changelog_diff() {
  echo
  echo "生成的发布说明变更（发布说明文件: ${NOTES_FILE}）:"
  git --no-pager diff -- "$CHANGELOG_FILE" "$NOTES_FILE"
}

find_editor() {
  local candidate
  if [ -n "${VISUAL:-}" ]; then
    printf '%s\n' "$VISUAL"
    return 0
  fi
  if [ -n "${EDITOR:-}" ]; then
    printf '%s\n' "$EDITOR"
    return 0
  fi
  # shell 环境变量未设置时，回退到 git 的编辑器配置
  if [ -n "${GIT_EDITOR:-}" ]; then
    printf '%s\n' "$GIT_EDITOR"
    return 0
  fi
  candidate="$(git config --get core.editor 2>/dev/null || true)"
  if [ -n "$candidate" ]; then
    printf '%s\n' "$candidate"
    return 0
  fi
  for candidate in vi nano vim; do
    if command -v "$candidate" >/dev/null 2>&1; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

edit_notes() {
  local editor
  local -a editor_parts

  if ! editor="$(find_editor)"; then
    say_warn "未找到可用编辑器；请设置 VISUAL 或 EDITOR 环境变量后重试"
    return 1
  fi
  read -r -a editor_parts <<< "$editor"

  echo "正在使用 ${editor_parts[0]} 编辑 ${NOTES_FILE}（保存并退出后继续）..."
  if ! "${editor_parts[@]}" "$NOTES_FILE"; then
    say_err "编辑器异常退出，未同步改动"
    return 1
  fi

  EDITED_NOTES=1
  git restore -- "$CHANGELOG_FILE"
  scripts/release/gen-changelog.sh --from-existing "$TAG"
}

review_changelog() {
  # 返回 0: 确认内容继续；返回 3: 返回重新选择版本
  while true; do
    show_changelog_diff
    echo
    say_title "请审查发布说明:"
    echo "  回车 或 y   确认内容并继续"
    echo "  e           编辑 ${NOTES_FILE}（保存后自动同步到 ${CHANGELOG_FILE}）"
    if [ "$VERSION_DERIVED" -eq 1 ]; then
      echo "  v           返回重新选择版本"
    fi
    echo "  q           中止发布"
    printf '%s请选择: %s' "$C_TITLE" "$C_RESET"
    if ! read_choice; then
      abort_on_eof
    fi
    case "$REPLY" in
      ""|y|Y)
        return 0
        ;;
      e|E)
        edit_notes || true
        ;;
      v|V)
        if [ "$VERSION_DERIVED" -eq 1 ]; then
          return 3
        fi
        prompt_invalid
        ;;
      q|Q)
        abort_release "已中止发布"
        ;;
      *)
        prompt_invalid
        ;;
    esac
  done
}

confirm_release() {
  # 返回 0: 确认执行；返回 3: 返回审查
  while true; do
    echo
    say_title "将执行以下外部动作:"
    echo "  - 提交 chore: release ${TAG} 到 ${MAIN_BRANCH}"
    echo "  - 创建 annotated tag ${TAG}"
    echo "  - 推送 ${MAIN_BRANCH}"
    echo "  - 推送 tag ${TAG} 触发 release workflow"
    echo "  - 将 release commit 快进同步回 ${DEV_BRANCH} 并推送 ${DEV_BRANCH}"
    print_bump_warning
    echo
    echo "  yes   执行发布"
    echo "  b     返回修改发布说明"
    echo "  q     中止发布"
    printf '%s输入 yes 执行发布: %s' "$C_TITLE" "$C_RESET"
    if ! read_choice; then
      abort_on_eof
    fi
    case "$REPLY" in
      yes)
        return 0
        ;;
      b|B)
        return 3
        ;;
      q|Q)
        abort_release "已中止发布"
        ;;
      *)
        prompt_invalid
        ;;
    esac
  done
}

# ---- 执行发布 ----

report_release_failure() {
  # set -e 下执行阶段任一步失败时由 ERR trap 调用；只打印状态与恢复建议，不改变退出码
  local status=$?
  local main_pushed=0 tag_created=0 tag_pushed=0 dev_synced=0

  git merge-base --is-ancestor "$MAIN_BRANCH" "${REMOTE}/${MAIN_BRANCH}" >/dev/null 2>&1 && main_pushed=1 || true
  git rev-parse -q --verify "refs/tags/${TAG}" >/dev/null 2>&1 && tag_created=1 || true
  if [ "$tag_created" -eq 1 ]; then
    git ls-remote --exit-code --tags "$REMOTE" "refs/tags/${TAG}" >/dev/null 2>&1 && tag_pushed=1 || true
  fi
  git merge-base --is-ancestor "$DEV_BRANCH" "${REMOTE}/${DEV_BRANCH}" >/dev/null 2>&1 && dev_synced=1 || true

  echo
  say_err "发布流程在上述步骤失败（退出码 ${status}）。当前状态:"
  echo "  当前分支: $(git branch --show-current || true)"
  echo "  ${MAIN_BRANCH}: $([ "$main_pushed" -eq 1 ] && echo "与 ${REMOTE}/${MAIN_BRANCH} 一致" || echo "有未推送提交")"
  if [ "$tag_created" -eq 0 ]; then
    echo "  tag: 未创建"
  elif [ "$tag_pushed" -eq 1 ]; then
    echo "  tag ${TAG}: 已推送，release workflow 应已触发"
  else
    echo "  tag ${TAG}: 已创建但未推送"
  fi
  echo "  ${DEV_BRANCH}: $([ "$dev_synced" -eq 1 ] && echo "与 ${REMOTE}/${DEV_BRANCH} 一致" || echo "尚未同步 release commit")"
  echo
  echo "恢复建议:"
  if [ "$tag_pushed" -eq 1 ]; then
    echo "  - 不要回退已推送的 ${MAIN_BRANCH} 和 ${TAG}"
    if [ "$dev_synced" -eq 0 ]; then
      echo "  - 手动同步 dev: git checkout ${DEV_BRANCH} && git merge --ff-only ${MAIN_BRANCH} && git push ${REMOTE} ${DEV_BRANCH}"
    fi
    echo "  - 到 GitHub Actions 查看 release workflow 执行结果"
  elif [ "$tag_created" -eq 1 ]; then
    if [ "$main_pushed" -eq 0 ]; then
      echo "  - 继续发布: git push ${REMOTE} ${MAIN_BRANCH} && git push ${REMOTE} ${TAG}"
      echo "  - 完全回退: git tag -d ${TAG} && git checkout ${MAIN_BRANCH} && git reset --hard ${REMOTE}/${MAIN_BRANCH}"
    else
      echo "  - 继续发布: git push ${REMOTE} ${TAG}"
      echo "  - 完全回退: git tag -d ${TAG}"
    fi
  elif [ "$main_pushed" -eq 1 ]; then
    echo "  - ${MAIN_BRANCH} 本地与远端一致，未产生任何提交或 tag；修正问题后重跑本脚本即可"
  else
    echo "  - release commit 已在本地 ${MAIN_BRANCH}（此时重跑脚本会因 main 领先 dev 而失败）"
    echo "  - 继续发布: git tag -a ${TAG} -m \"Release ${TAG}\" && git push ${REMOTE} ${MAIN_BRANCH} && git push ${REMOTE} ${TAG}"
    echo "  - 完全回退: git checkout ${MAIN_BRANCH} && git reset --hard ${REMOTE}/${MAIN_BRANCH}"
  fi
  echo
  echo "恢复或回退后请先确认工作区状态（git status）。"
}

run_release() {
  echo
  echo "开始执行发布..."
  git add "$CHANGELOG_FILE" "$NOTES_FILE"
  git commit -m "chore: release ${TAG}"
  git tag -a "$TAG" -m "Release $TAG"
  git push "$REMOTE" "$MAIN_BRANCH"
  git push "$REMOTE" "$TAG"

  git checkout "$DEV_BRANCH"
  git pull --ff-only "$REMOTE" "$DEV_BRANCH"
  git merge --ff-only "$MAIN_BRANCH"
  git push "$REMOTE" "$DEV_BRANCH"
}

print_actions_url() {
  local url
  url="$(git remote get-url "$REMOTE" 2>/dev/null || true)"
  case "$url" in
    git@github.com:*)
      url="https://github.com/${url#git@github.com:}"
      ;;
    ssh://git@github.com/*)
      url="https://github.com/${url#ssh://git@github.com/}"
      ;;
    https://github.com/*|http://github.com/*)
      :
      ;;
    *)
      return 0
      ;;
  esac
  url="${url%.git}"
  say_ok "release workflow: ${url}/actions/workflows/release.yaml"
}

print_release_summary() {
  cat <<EOF

${C_TITLE}发布流程已提交并触发:${C_RESET}
${C_OK}  ${MAIN_BRANCH}: 已推送到 ${REMOTE}/${MAIN_BRANCH}${C_RESET}
${C_OK}  tag:  已推送 ${TAG}${C_RESET}
${C_OK}  ${DEV_BRANCH}: 已同步 release commit 并推送到 ${REMOTE}/${DEV_BRANCH}${C_RESET}
EOF
  if [ -n "${QMS_RELEASE_IN_SANDBOX:-}" ]; then
    echo
    echo "（模拟模式：以上动作全部发生在沙箱中，release workflow 不会触发）"
  else
    echo
    echo "release workflow 会由 tag 自动触发，请到 GitHub Actions 页面查看执行结果。"
    print_actions_url
  fi
}

# ---- dry-run 只读预览 ----

run_dry_run() {
  local preview_file switched=0

  echo
  say_title "== dry-run 预览模式：以下为只读检查与预览，不会修改任何文件、分支或远端 =="

  if ! git merge-base --is-ancestor "$MAIN_BRANCH" "$DEV_BRANCH"; then
    die "dry-run: ${MAIN_BRANCH} 无法快进合并 ${DEV_BRANCH}（正式发布同样会失败，请先同步两个分支）"
  fi

  preview_file="$(mktemp "${TMPDIR:-/tmp}/qms-release-dryrun-XXXXXX.md")"
  if [ -n "$START_BRANCH" ] && [ "$START_BRANCH" != "$DEV_BRANCH" ]; then
    git checkout --detach "$DEV_BRANCH" >/dev/null 2>&1 || {
      rm -f "$preview_file"
      die "dry-run: 无法临时检出 ${DEV_BRANCH} 预览发布说明"
    }
    switched=1
  elif [ -z "$START_BRANCH" ]; then
    say_warn "警告: 当前处于分离 HEAD，发布说明预览基于当前 HEAD 而非 ${DEV_BRANCH}" >&2
  fi

  if ! git-cliff --unreleased --tag "$TAG" --strip header -o "$preview_file"; then
    if [ "$switched" -eq 1 ]; then
      git checkout "$START_BRANCH" >/dev/null 2>&1 || true
    fi
    rm -f "$preview_file"
    die "dry-run: git-cliff 生成预览失败"
  fi

  if [ "$switched" -eq 1 ]; then
    if ! git checkout "$START_BRANCH" >/dev/null 2>&1; then
      say_warn "警告: 切回分支 ${START_BRANCH} 失败，请手动处理" >&2
    else
      say_ok "已切回分支 ${START_BRANCH}"
    fi
  fi

  echo
  say_title "== .changes/${TAG}.md 预览（正式发布时将写入该文件并插入 ${CHANGELOG_FILE} 顶部）=="
  echo
  if [ -s "$preview_file" ]; then
    cat "$preview_file"
  else
    say_warn "（警告: 自上一个 tag 以来没有符合 conventional commits 规范的提交，发布说明为空）"
  fi
  rm -f "$preview_file"

  echo
  say_title "== 将执行的外部动作 =="
  echo "  - 提交 chore: release ${TAG} 到 ${MAIN_BRANCH}"
  echo "  - 创建 annotated tag ${TAG}"
  echo "  - 推送 ${MAIN_BRANCH} 和 tag ${TAG} 触发 release workflow"
  echo "  - 将 release commit 快进同步回 ${DEV_BRANCH} 并推送 ${DEV_BRANCH}"
  print_bump_warning
  echo
  say_ok "dry-run 结束：未修改任何文件、分支或远端。完整演练请使用 --simulate。"
}

# ---- simulate 沙箱演练 ----

run_simulated() {
  local sandbox rc=0

  if [ -n "${QMS_RELEASE_IN_SANDBOX:-}" ]; then
    die "--simulate 不能在沙箱内再次使用"
  fi

  sandbox="$(mktemp -d "${TMPDIR:-/tmp}/qms-release-simulate-XXXXXXXX")"

  echo
  say_title "== 模拟模式 =="
  echo "完整发布流程将在临时沙箱中真实执行，真实仓库与远端不受影响。"
  echo "沙箱目录: ${sandbox}"

  # 先对真实远端做一次只读 fetch，让沙箱引用尽量反映已发布的 tag（离线时忽略失败）
  if ! git -C "$ROOT" fetch "$REMOTE" --tags >/dev/null 2>&1; then
    say_warn "警告: 获取 ${REMOTE} 失败（离线?），沙箱将基于本地引用" >&2
  fi

  if ! git clone --quiet --mirror "$ROOT" "${sandbox}/origin.git"; then
    die "创建沙箱远端失败"
  fi
  if ! git clone --quiet "${sandbox}/origin.git" "${sandbox}/work"; then
    die "创建沙箱工作区失败"
  fi

  (
    cd "${sandbox}/work"
    # clone 只把远端 HEAD 检出为本地分支，补齐发布流程需要的 main/dev
    git branch "$MAIN_BRANCH" "origin/${MAIN_BRANCH}" >/dev/null 2>&1 || true
    git branch "$DEV_BRANCH" "origin/${DEV_BRANCH}" >/dev/null 2>&1 || true
    # 演练当前工作区的脚本版本（含未提交改动）；保留可执行位。
    # 沙箱克隆只含已提交内容，若脚本有未提交改动则需提交到沙箱分支，保证子流程的工作区检查通过
    mkdir -p scripts/release
    cp -p "$SCRIPT_DIR/release.sh" "$SCRIPT_DIR/lib.sh" "$SCRIPT_DIR/gen-changelog.sh" scripts/release/
    git config user.email >/dev/null 2>&1 || git config user.email "release-simulate@localhost"
    git config user.name >/dev/null 2>&1 || git config user.name "Release Simulate"
    if ! git diff --quiet -- scripts/release || ! git diff --cached --quiet -- scripts/release; then
      git add scripts/release
      git commit -qm "chore: 同步演练使用的脚本版本" --no-verify || true
    fi
    echo
    say_title "== 沙箱内开始演练，以下交互与真实发布一致 =="
    QMS_RELEASE_IN_SANDBOX=1 bash scripts/release/release.sh "$@"
  ) || rc=$?

  echo
  if [ "$rc" -eq 0 ]; then
    say_title "== 模拟完成 =="
  else
    say_title "== 模拟结束（退出码 ${rc}）=="
  fi
  echo "真实仓库与远端未受影响。沙箱目录: ${sandbox}"
  echo "沙箱内生成的 ${CHANGELOG_FILE} 与 .changes/ 可用于检查演练结果。"
  echo
  # 结束后默认删除沙箱；EOF（管道/自动化场景）同样按回车处理
  while true; do
    printf '%s是否删除临时沙箱？回车删除，k 保留: %s' "$C_TITLE" "$C_RESET"
    if ! read_choice; then
      REPLY=""
      break
    fi
    case "$REPLY" in
      ""|k|K)
        break
        ;;
      *)
        prompt_invalid
        ;;
    esac
  done
  case "$REPLY" in
    k|K)
      say_warn "已保留沙箱，检查后手动删除: rm -rf ${sandbox}"
      ;;
    *)
      rm -rf "$sandbox"
      say_ok "已删除临时沙箱"
      ;;
  esac
  echo "正式发布请去掉 --simulate 重新运行。"
  exit "$rc"
}

# ---- 主流程 ----

main() {
  local -a positional=()
  local stage rc

  while [ "$#" -gt 0 ]; do
    case "$1" in
      -h|--help)
        usage
        exit 0
        ;;
      --dry-run)
        DRY_RUN=1
        ;;
      --simulate)
        SIMULATE=1
        ;;
      --*)
        echo "未知选项: $1" >&2
        usage >&2
        exit 1
        ;;
      *)
        positional+=("$1")
        ;;
    esac
    shift
  done

  if [ "$DRY_RUN" -eq 1 ] && [ "$SIMULATE" -eq 1 ]; then
    die "--dry-run 与 --simulate 不能同时使用"
  fi
  if [ "${#positional[@]}" -ne 1 ]; then
    usage >&2
    exit 1
  fi

  RELEASE_INPUT="${positional[0]}"
  validate_release_input "$RELEASE_INPUT"

  require_command git
  require_command git-cliff

  if [ "$SIMULATE" -eq 1 ]; then
    # 沙箱演练：不要求真实工作区干净，全部检查由沙箱内的子流程执行
    run_simulated "${positional[@]}"
    exit 0
  fi

  cd "$ROOT"
  START_BRANCH="$(git branch --show-current || true)"

  ensure_clean_worktree
  ensure_branch_exists "$MAIN_BRANCH"
  ensure_branch_exists "$DEV_BRANCH"

  git fetch "$REMOTE" --tags

  ensure_remote_branch_exists "$REMOTE" "$MAIN_BRANCH"
  ensure_remote_branch_exists "$REMOTE" "$DEV_BRANCH"
  ensure_branch_contains_remote "$REMOTE" "$DEV_BRANCH"

  resolve_release_tag
  ensure_tag_newer_than_current "$TAG"
  ensure_no_remote_tag "$REMOTE" "$TAG"

  if [ "$DRY_RUN" -eq 1 ]; then
    run_dry_run
    exit 0
  fi

  git checkout "$MAIN_BRANCH"
  if ! git pull --ff-only "$REMOTE" "$MAIN_BRANCH"; then
    restore_start_branch
    die "同步 ${REMOTE}/${MAIN_BRANCH} 失败"
  fi
  if ! git merge --ff-only "$DEV_BRANCH"; then
    restore_start_branch
    die "无法快进合并 ${DEV_BRANCH} 到 ${MAIN_BRANCH}，请先处理两个分支的分叉"
  fi

  ensure_no_remote_tag "$REMOTE" "$TAG"

  if ! scripts/release/gen-changelog.sh "$TAG"; then
    restore_start_branch
    die "生成 changelog 失败"
  fi
  NOTES_FILE=".changes/${TAG}.md"

  # 审查 <-> 确认 <-> 重选版本 的状态循环；返回值 3 表示回退上一步
  stage="review"
  while true; do
    case "$stage" in
      review)
        rc=0
        review_changelog || rc=$?
        case "$rc" in
          0)
            stage="confirm"
            ;;
          3)
            reset_generated_changes
            resolve_release_tag
            ensure_tag_newer_than_current "$TAG"
            ensure_no_remote_tag "$REMOTE" "$TAG"
            if ! scripts/release/gen-changelog.sh "$TAG"; then
              restore_start_branch
              die "生成 changelog 失败"
            fi
            NOTES_FILE=".changes/${TAG}.md"
            ;;
          *)
            exit "$rc"
            ;;
        esac
        ;;
      confirm)
        rc=0
        confirm_release || rc=$?
        case "$rc" in
          0)
            stage="execute"
            ;;
          3)
            stage="review"
            ;;
          *)
            exit "$rc"
            ;;
        esac
        ;;
      execute)
        break
        ;;
    esac
  done

  trap report_release_failure ERR
  run_release
  trap - ERR

  print_release_summary
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi

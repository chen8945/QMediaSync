#!/usr/bin/env bash
# ponytail: 只支持 /app/QMediaSync 和 /app/config 的标准镜像布局，自定义布局使用二进制恢复命令。
set +x
set +v
set -euo pipefail

usage() {
  cat <<'EOF'
用法：recover-admin.sh --action reset-password|delete-admin [选项]
  -f, --file FILE         Compose 文件，可重复；默认识别当前目录
  -p, --project-name NAME 原部署的 Compose 项目名
      --env-file FILE     原部署的环境变量文件，可重复
      --service NAME      QMediaSync 服务名；自动识别失败时交互选择容器
      --yes               确认删除管理员，跳过交互确认
需要 Bash、Docker Compose 2.24.4+、tar、awk；不会拉取镜像或停止数据库。
EOF
}

die() { printf '错误：%s\n' "$*" >&2; exit 1; }
# 管道执行时从终端读取，避免把脚本内容当作交互输入。
open_terminal() {
  if [[ -t 0 ]]; then
    exec 3<&0
  else
    { exec 3</dev/tty; } 2>/dev/null
  fi
}
file_args=()
context_args=()
service=""
action=""
confirmed=false
while (( $# )); do
  case "$1" in
    -f|--file|-p|--project-name|--env-file|--service|--action)
      (( $# >= 2 )) && [[ -n "$2" ]] || die "$1 缺少参数"
      case "$1" in
        -f|--file) file_args+=(-f "$2") ;;
        -p|--project-name|--env-file) context_args+=("$1" "$2") ;;
        --service) service="$2" ;;
        --action) action="$2" ;;
      esac
      shift 2 ;;
    --yes) confirmed=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; die "未知参数：$1" ;;
  esac
done
case "$action" in
  reset-password) recovery_args=(--reset-admin-password) ;;
  delete-admin) recovery_args=(--delete-admin --yes) ;;
  *) usage >&2; die "请指定 --action reset-password 或 --action delete-admin" ;;
esac
for command_name in docker tar awk mktemp; do
  command -v "$command_name" >/dev/null || die "未找到 $command_name"
done

if (( ${#file_args[@]} == 0 )); then
  for file in compose.yaml compose.yml docker-compose.yaml docker-compose.yml; do
    if [[ -f "$file" ]]; then
      file_args=(-f "$file")
      break
    fi
  done
  (( ${#file_args[@]} )) || die "当前目录没有 Compose 文件，请通过 -f 指定"
  for file in compose.override.yaml compose.override.yml docker-compose.override.yaml docker-compose.override.yml; do
    if [[ -f "$file" ]]; then
      file_args+=(-f "$file")
      break
    fi
  done
fi
compose=(docker compose "${file_args[@]}" "${context_args[@]}")
services=$("${compose[@]}" config --services)
if [[ -z "$service" ]]; then
  candidates=()
  while IFS= read -r candidate; do
    [[ -n "$candidate" ]] || continue
    images=$("${compose[@]}" config --images "$candidate")
    if [[ "$candidate" == qmediasync || "$candidate" == qms || "$images" =~ (^|/)qmediasync(:|@|$) ]]; then
      candidates+=("$candidate")
    fi
  done <<< "$services"
  if (( ${#candidates[@]} == 1 )); then
    service="${candidates[0]}"
  else
    choices=()
    while IFS= read -r candidate; do
      [[ -n "$candidate" ]] || continue
      containers=$("${compose[@]}" ps --all --quiet "$candidate")
      while IFS= read -r candidate_container; do
        [[ -n "$candidate_container" ]] || continue
        details=$(docker inspect --format '{{.Name}} / {{.State.Status}}' "$candidate_container")
        choices+=("$candidate / ${details#/}")
      done <<< "$containers"
    done <<< "$services"
    (( ${#choices[@]} )) || die "当前 Compose 项目没有已部署的容器，请先完成正常部署"
    open_terminal || die "无法唯一识别 QMediaSync，当前无可用终端，请通过 --service 指定服务名"
    printf '请选择 QMediaSync 对应的容器（服务 / 容器 / 状态）：\n' >&2
    PS3='请输入编号（Ctrl+D 取消）：'
    select choice in "${choices[@]}"; do
      if [[ -n "$choice" ]]; then
        service="${choice%% *}"
        break
      fi
      printf '编号无效，请输入列表中的编号。\n' >&2
    done <&3 || die "已取消，未修改数据"
    exec 3<&-
  fi
fi
[[ "$service" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || die "服务名格式无效"
found=false
while IFS= read -r candidate; do
  [[ "$candidate" != "$service" ]] || found=true
done <<< "$services"
$found || die "Compose 项目中不存在服务 $service"

container=$("${compose[@]}" ps --all --quiet "$service")
[[ -n "$container" && "$container" != *$'\n'* ]] || die "目标服务必须已有且仅有一个容器，请先完成正常部署"
inspect() { docker inspect --format "$1" "$container"; }
state=$(inspect '{{.State.Status}}')
[[ "$state" == running || "$state" == exited ]] || die "容器状态为 $state，请先处理暂停、重启或未初始化状态"
# config --hash 不展开服务级 env_file，先用 Compose 生成完整配置快照。
normalized_config=$("${compose[@]}" config)
expected_hash=$(docker compose -f - config --hash "$service" <<< "$normalized_config")
read -r hash_service config_hash <<< "$expected_hash"
[[ "$hash_service" == "$service" && -n "$config_hash" ]] || die "无法核对 Compose 配置哈希，请检查 Compose 版本"
[[ "$(inspect '{{index .Config.Labels "com.docker.compose.config-hash"}}')" == "$config_hash" ]] ||
  die "Compose 配置与已部署容器不一致，请使用原部署的文件、项目名和环境参数"
image=$(inspect '{{.Config.Image}}')
image_id=$(inspect '{{.Image}}')
[[ "$(docker image inspect --format '{{.Id}}' "$image")" == "$image_id" ]] ||
  die "本地镜像与原容器版本不一致，请先完成正常部署；恢复不会拉取镜像"
changes=$(docker diff "$container")
while IFS= read -r change; do
  if [[ "$change" == [ACD]' /app/QMediaSync' || "$change" == [ACD]' /app/qms.update.tar.gz' ]]; then
    die "容器内程序已更新或有待更新包，请先部署匹配版本的镜像"
  fi
done <<< "$changes"

mount_type=$(inspect '{{range .Mounts}}{{if eq .Destination "/app/config"}}{{.Type}}{{end}}{{end}}')
mount_rw=$(inspect '{{range .Mounts}}{{if eq .Destination "/app/config"}}{{.RW}}{{end}}{{end}}')
[[ "$mount_rw" == true && ( "$mount_type" == bind || "$mount_type" == volume ) ]] ||
  die "/app/config 必须使用可写的持久化 bind 或 volume 挂载"
mount_source=$(inspect '{{range .Mounts}}{{if eq .Destination "/app/config"}}{{if eq .Type "volume"}}{{json .Name}}{{else}}{{json .Source}}{{end}}{{end}}{{end}}')
# Compose 会再次插值；JSON 引号可直接用作 YAML 字符串，美元符号须保留为字面量。
mount_source="${mount_source//\$/\$\$}"
mount_subpath=$(inspect '{{range .HostConfig.Mounts}}{{if eq .Target "/app/config"}}{{if .VolumeOptions}}{{with index .VolumeOptions "Subpath"}}{{.}}{{end}}{{end}}{{end}}{{end}}')
[[ -z "$mount_subpath" ]] || die "配置卷使用了子目录挂载，请保留原 volume subpath 并使用二进制恢复命令"
bind_options="create_host_path: false"
if [[ "$mount_type" == bind ]]; then
  propagation=$(inspect '{{range .Mounts}}{{if eq .Destination "/app/config"}}{{.Propagation}}{{end}}{{end}}')
  case "$propagation" in
    private|rprivate|shared|rshared|slave|rslave) bind_options+=", propagation: $propagation" ;;
    "") ;;
    *) die "无法识别原配置挂载的传播模式" ;;
  esac
  mount_mode=$(inspect '{{range .Mounts}}{{if eq .Destination "/app/config"}}{{.Mode}}{{end}}{{end}}')
  case ",$mount_mode," in
    *,z,*) bind_options+=", selinux: z" ;;
    *,Z,*) bind_options+=", selinux: Z" ;;
  esac
fi

network_mode=$(inspect '{{.HostConfig.NetworkMode}}')
networks=()
case "$network_mode" in
  host|none|bridge|container:*) ;;
  *)
    network_names=$(inspect '{{range $name, $_ := .NetworkSettings.Networks}}{{json $name}}{{"\n"}}{{end}}')
    while IFS= read -r network; do
      [[ -z "$network" ]] || networks+=("${network//\$/\$\$}")
    done <<< "$network_names"
    (( ${#networks[@]} )) || die "无法确定原容器的实际网络"
    ;;
esac

guid=$(inspect '{{range .Config.Env}}{{if eq (index (split . "=") 0) "GUID"}}{{index (split . "=") 1}}{{end}}{{end}}')
user=$(inspect '{{.Config.User}}')
if [[ -n "$guid" && "$guid" != 0 ]]; then
  [[ "$guid" =~ ^[0-9]+$ ]] || die "GUID 必须是数值 UID"
  user="$guid"
fi
user="${user:-0}"
IFS=: read -r user_name group_name <<< "$user"
passwd=$(docker cp "$container:/etc/passwd" - | tar -xO)
identity=$(awk -F: -v name="$user_name" '$1 == name || $3 == name { print $3 ":" $4; exit }' <<< "$passwd")
[[ -n "$identity" ]] || die "无法确认原容器运行用户 $user_name 的 UID/GID"
if [[ -n "$group_name" ]]; then
  if [[ "$group_name" =~ ^[0-9]+$ ]]; then
    group_id="$group_name"
  else
    groups=$(docker cp "$container:/etc/group" - | tar -xO)
    group_id=$(awk -F: -v name="$group_name" '$1 == name { print $3; exit }' <<< "$groups")
    [[ -n "$group_id" ]] || die "无法确认原容器运行组 $group_name"
  fi
  identity="${identity%%:*}:$group_id"
fi
[[ "$identity" =~ ^[0-9]+:[0-9]+$ ]] || die "无法确认原容器运行身份"

project=$(inspect '{{index .Config.Labels "com.docker.compose.project"}}')
printf '目标：%s / %s，容器 %s，原状态 %s\n' "$project" "$service" "$container" "$state" >&2
if [[ "$action" == delete-admin ]] && ! $confirmed; then
  open_terminal || die "删除管理员需要交互确认，或显式传入 --yes"
  printf '将删除管理员并撤销全部浏览器会话和 QMS API Key，业务数据保留。输入 DELETE 确认：' >&2
  read -r answer <&3 || die "已取消，未修改数据"
  exec 3<&-
  [[ "$answer" == DELETE ]] || die "已取消，未修改数据"
fi

override=$(mktemp)
restore_running=false
recovery_output=""
cleanup() {
  local status=$?
  trap - EXIT
  # 凭据可能已经提交，停启收尾期间不能被中断而丢失结果。
  trap '' INT TERM
  if $restore_running; then
    if ! docker start "$container" >&2; then
      printf '错误：恢复后的原容器启动失败，请手动启动；已完成的认证变更不会回滚。\n' >&2
      status=1
    elif [[ "$(inspect '{{.State.Running}}')" != true ]]; then
      printf '错误：原容器未恢复运行，请检查启动日志。\n' >&2
      status=1
    fi
  fi
  rm -f "$override" || status=1
  if [[ -n "$recovery_output" ]]; then
    printf '\n%s\n' "$recovery_output"
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# 从配置快照运行，锁定原镜像、实际配置卷和网络，保留其他部署参数。
# !override 避免把原 logging.options 合并到 none 驱动，要求 Compose 2.24.4+。
{
  printf 'services:\n  %s:\n    image: %s\n    logging: !override {driver: none}\n' "$service" "$image_id"
  if (( ${#networks[@]} )); then
    printf '    networks: !override\n'
    for index in "${!networks[@]}"; do
      printf '      qms_admin_recovery_network_%s: {}\n' "$index"
    done
  else
    printf '    network_mode: "%s"\n' "$network_mode"
  fi
  printf '    volumes:\n'
  if [[ "$mount_type" == bind ]]; then
    printf '      - type: bind\n        source: %s\n        target: /app/config\n        bind: {%s}\n' "$mount_source" "$bind_options"
  else
    printf '      - type: volume\n        source: qms_admin_recovery_config\n        target: /app/config\n'
    printf 'volumes:\n  qms_admin_recovery_config:\n    external: true\n    name: %s\n' "$mount_source"
  fi
  if (( ${#networks[@]} )); then
    printf 'networks:\n'
    for index in "${!networks[@]}"; do
      printf '  qms_admin_recovery_network_%s:\n    external: true\n    name: %s\n' "$index" "${networks[$index]}"
    done
  fi
} > "$override"
recovery_compose=(docker compose -f - "${context_args[@]}" -f "$override")
"${recovery_compose[@]}" config --quiet <<< "$normalized_config" || die "无法解析临时恢复配置，需要 Docker Compose 2.24.4+"
if [[ "$state" == running ]]; then
  restore_running=true
  docker stop "$container" >&2
fi
if recovery_output=$("${recovery_compose[@]}" run --rm --no-deps --pull never -T --user "$identity" \
  --entrypoint /app/QMediaSync "$service" "${recovery_args[@]}" <<< "$normalized_config"); then
  exit 0
else
  status=$?
  printf '错误：恢复命令未正常结束（退出码 %s），请检查下方结果。\n' "$status" >&2
  exit "$status"
fi

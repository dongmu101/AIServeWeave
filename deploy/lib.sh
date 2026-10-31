# lib.sh holds what install.sh and upgrade.sh share: .env editing, the
# sequential image build, backups and the post-start health check. It is
# sourced, never run, and changes into the deploy directory so every compose
# call below resolves the same project and the same .env.
#
# lib.sh 存放 install.sh 与 upgrade.sh 共用的部分：.env 编辑、逐个构建镜像、备份与启动后
# 的健康检查。它只被 source、不单独执行，并切换到 deploy 目录，让下面每一次 compose 调用都
# 落在同一个项目、同一份 .env 上。

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$DEPLOY_DIR/.." && pwd)"
ENV_FILE="$DEPLOY_DIR/.env"
cd "$DEPLOY_DIR"

# BUILD_ORDER builds one image at a time. Compose builds in parallel by
# default, and four Go and Next.js builds at once exhaust a 4 GiB host.
#
# BUILD_ORDER 一次只构建一个镜像。Compose 默认并行构建，四个 Go 与 Next.js 构建同时跑会
# 耗尽一台 4 GiB 的主机。
BUILD_ORDER="registry-init registry controlplane gateway console"

# SECRET_KEYS are the independent secrets .env.example asks for, generated
# with openssl rand -base64 32; the Console session secret takes 48 bytes.
#
# SECRET_KEYS 是 .env.example 要求的独立密钥，用 openssl rand -base64 32 生成；Console
# 会话密钥取 48 字节。
SECRET_KEYS="AISW_ACCESS_SECRET AISW_INTERNAL_TOKEN AISW_BOOTSTRAP_TOKEN AISW_REGISTRY_ADMIN_TOKEN AISW_GATEWAY_ADMIN_TOKEN AISW_GATEWAY_MODEL_PULL_TOKEN AISW_GATEWAY_COMFYUI_MANAGED_TOKEN"

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m警告:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31m错误:\033[0m %s\n' "$*" >&2; exit 1; }

compose() { docker compose "$@"; }

# require_tools fails before anything is changed when a prerequisite is
# missing, rather than halfway through a build.
#
# require_tools 在缺少前置依赖时、在改动任何东西之前就失败，而不是构建到一半才失败。
require_tools() {
  local tool
  for tool in "$@"; do
    command -v "$tool" >/dev/null 2>&1 || die "缺少 ${tool}，请先安装"
  done
  docker compose version >/dev/null 2>&1 || die "需要 Docker Compose v2（docker compose 子命令）"
  docker info >/dev/null 2>&1 || die "无法连接 Docker daemon：确认 Docker 已启动，并以 root 或 docker 组用户运行"
}

# check_memory warns when memory plus swap is below what a full build needs.
# It only warns: a host that already has every image cached builds nothing.
#
# check_memory 在内存加 swap 低于完整构建所需时给出警告。它只警告：镜像已全部缓存的主机
# 实际上什么也不用构建。
check_memory() {
  [[ -r /proc/meminfo ]] || return 0
  local total_kib
  total_kib="$(awk '/^(MemTotal|SwapTotal):/ {sum += $2} END {print sum + 0}' /proc/meminfo)"
  if (( total_kib < 6 * 1024 * 1024 )); then
    warn "内存加 swap 共 $((total_kib / 1024)) MiB，低于 6 GiB，构建时可能 OOM，建议先加 swap"
  fi
}

# env_get prints KEY's value from the last uncommented assignment in .env,
# or nothing when the key is absent or only present as a comment.
#
# env_get 输出 .env 中最后一条未注释赋值里 KEY 的值；键不存在或只以注释形式存在时输出空。
env_get() {
  [[ -f "$ENV_FILE" ]] || return 0
  KEY="$1" awk 'BEGIN { k = ENVIRON["KEY"] "=" } index($0, k) == 1 { v = substr($0, length(k) + 1) } END { print v }' "$ENV_FILE"
}

# env_set writes KEY=VALUE into .env. It replaces every uncommented
# assignment, otherwise uncomments the first "# KEY=" line, otherwise
# appends. Values travel through the environment, not awk -v, so base64's
# "/" and "+" need no escaping.
#
# env_set 把 KEY=VALUE 写入 .env：替换所有未注释的赋值；没有则取消注释第一行 "# KEY="；
# 再没有就追加。值经由环境变量而不是 awk -v 传入，因此 base64 里的 "/" 与 "+" 无需转义。
env_set() {
  local tmp
  tmp="$(mktemp "$DEPLOY_DIR/.env.XXXXXX")"
  KEY="$1" VALUE="$2" awk '
    BEGIN { k = ENVIRON["KEY"]; v = ENVIRON["VALUE"]; live = 0 }
    { lines[NR] = $0; if (index($0, k "=") == 1) live = 1 }
    END {
      done = 0
      for (i = 1; i <= NR; i++) {
        line = lines[i]
        if (live && index(line, k "=") == 1) { print k "=" v; done = 1; continue }
        if (!live && !done && line ~ ("^#[[:space:]]*" k "=")) { print k "=" v; done = 1; continue }
        print line
      }
      if (!done) print k "=" v
    }' "$ENV_FILE" >"$tmp"
  chmod 600 "$tmp"
  mv "$tmp" "$ENV_FILE"
}

# ensure_env creates .env from the example when it is missing, keeps it
# private and strips CR line endings, which make every "KEY=" match fail.
#
# ensure_env 在 .env 不存在时从示例创建，保持其私有权限，并去掉 CR 换行——CR 会让所有
# "KEY=" 匹配失败。
ensure_env() {
  if [[ ! -f "$ENV_FILE" ]]; then
    (umask 077 && cp "$DEPLOY_DIR/.env.example" "$ENV_FILE")
    log "已从 .env.example 创建 .env"
  fi
  chmod 600 "$ENV_FILE"
  if grep -q $'\r' "$ENV_FILE"; then
    local tmp
    tmp="$(mktemp "$DEPLOY_DIR/.env.XXXXXX")"
    tr -d '\r' <"$ENV_FILE" >"$tmp" && chmod 600 "$tmp" && mv "$tmp" "$ENV_FILE"
    log "已去掉 .env 中的 CR 换行"
  fi
}

# fill_secrets generates every secret that is still empty and never touches
# one that already has a value: rotating a secret is a deliberate act, not a
# side effect of rerunning a script.
#
# fill_secrets 为每个仍为空的密钥生成值，绝不改动已有值的密钥：轮换密钥应是有意为之，而不
# 是重跑脚本的副作用。
fill_secrets() {
  local key filled=0
  for key in $SECRET_KEYS; do
    if [[ -z "$(env_get "$key")" ]]; then
      env_set "$key" "$(openssl rand -base64 32)"
      filled=$((filled + 1))
    fi
  done
  if [[ -z "$(env_get AISW_CONSOLE_SESSION_SECRET)" ]]; then
    env_set AISW_CONSOLE_SESSION_SECRET "$(openssl rand -base64 48)"
    filled=$((filled + 1))
  fi
  (( filled == 0 )) || log "已生成 $filled 个空缺的密钥"
}

# ensure_access_log_off disables go-zero's access-log middleware in
# controlplane.yaml. On every 5xx it dumps the whole request, Authorization
# header and body included, which writes internal tokens and passwords to the
# log.
#
# ensure_access_log_off 在 controlplane.yaml 中关闭 go-zero 的访问日志中间件。它在每次
# 5xx 时转储整个请求，包括 Authorization 头与请求体，会把内部令牌和密码写进日志。
ensure_access_log_off() {
  local file="$DEPLOY_DIR/controlplane.yaml"
  grep -q '^Middlewares:' "$file" && return 0
  cat >>"$file" <<'EOF'

# go-zero dumps the full request, headers and body, on every 5xx; keep it off.
# go-zero 在每次 5xx 时转储包括请求头与请求体在内的完整请求，保持关闭。
Middlewares:
  Log: false
EOF
  log "已在 controlplane.yaml 中关闭 go-zero 访问日志中间件"
}

# ensure_config_readable keeps controlplane.yaml world-readable. It is bind
# mounted into a container that runs as uid 10001, and git rewrites it with
# the caller's umask on every pull or stash, so a shell left at umask 077
# turns it into 0600 and the control plane restarts on "permission denied".
#
# ensure_config_readable 保持 controlplane.yaml 对所有用户可读。它被 bind mount 进一个以
# uid 10001 运行的容器，而 git 每次 pull 或 stash 都会按调用方的 umask 重写它，因此停留在
# umask 077 的 shell 会把它变成 0600，控制面随即因 "permission denied" 反复重启。
ensure_config_readable() {
  chmod a+r "$DEPLOY_DIR/controlplane.yaml"
}

# project_name is the compose project, which names the data volumes.
# project_name 是 compose 项目名，数据卷以它命名。
project_name() {
  local name="${COMPOSE_PROJECT_NAME:-$(env_get COMPOSE_PROJECT_NAME)}"
  [[ -n "$name" ]] || name="$(basename "$DEPLOY_DIR")"
  printf '%s' "$name" | tr '[:upper:]' '[:lower:]'
}

volume_exists() { docker volume inspect "$(project_name)_$1" >/dev/null 2>&1; }

# build_images builds the named services, or BUILD_ORDER, one at a time.
# GOPROXY is forwarded only when set, so the Dockerfile default applies
# otherwise.
#
# build_images 逐个构建指定服务（未指定时为 BUILD_ORDER）。只在设置了 GOPROXY 时才转发，
# 否则沿用 Dockerfile 的默认值。
build_images() {
  local version="$1"
  shift
  local services=() args=(--build-arg "VERSION=$version") service
  if (( $# > 0 )); then services=("$@"); else read -r -a services <<<"$BUILD_ORDER"; fi
  [[ -z "${GOPROXY_ARG:-}" ]] || args+=(--build-arg "GOPROXY=$GOPROXY_ARG")
  for service in "${services[@]}"; do
    log "构建 ${service}（${version}）"
    compose build "${args[@]}" "$service" || die "$service 构建失败"
  done
}

# source_version names the checkout being built, for each binary's -version.
# source_version 标识正在构建的代码版本，供各二进制的 -version 使用。
source_version() {
  git -C "$ROOT_DIR" describe --tags --always --dirty 2>/dev/null || echo dev
}

# wait_exited waits for a one-shot service to finish and prints its exit code.
# wait_exited 等待一次性服务结束并输出其退出码。
wait_exited() {
  local service="$1" timeout="$2" id state i
  for ((i = 0; i < timeout; i += 2)); do
    id="$(compose ps -a -q "$service" 2>/dev/null | head -1)"
    if [[ -n "$id" ]]; then
      state="$(docker inspect -f '{{.State.Status}} {{.State.ExitCode}}' "$id")"
      if [[ "$state" == exited* ]]; then
        printf '%s' "${state#exited }"
        return 0
      fi
    fi
    sleep 2
  done
  printf 'timeout'
}

# probe waits until url answers with a status other than 5xx or no connection.
# probe 等待 url 返回一个既非 5xx、也非连接失败的状态码。
probe() {
  local name="$1" url="$2" method="${3:-GET}" code i
  for ((i = 0; i < 60; i += 2)); do
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 -X "$method" "$url" || true)"
    if [[ "$code" != 000 && "$code" != 5* ]]; then
      log "$name 就绪（HTTP ${code}）"
      return 0
    fi
    sleep 2
  done
  warn "$name 在 60 秒内未就绪（最后一次 HTTP ${code}）：docker compose logs --tail=100 ${name}"
  return 1
}

# health_check probes the three published HTTP entry points. Without
# credentials the control plane answers 4xx and the Gateway 401, which is
# enough to show each process is serving.
#
# health_check 探测三个已发布的 HTTP 入口。不带凭据时控制面回答 4xx、Gateway 回答 401，
# 足以说明各进程都在提供服务。
health_check() {
  local console_port controlplane_port gateway_port failed=0
  console_port="$(env_get CONSOLE_PORT)"
  controlplane_port="$(env_get CONTROLPLANE_PORT)"
  gateway_port="$(env_get GATEWAY_PORT)"
  probe console "http://127.0.0.1:${console_port:-3000}/login" || failed=1
  probe controlplane "http://127.0.0.1:${controlplane_port:-8090}/admin/v1/auth/login" POST || failed=1
  probe gateway "http://127.0.0.1:${gateway_port:-8080}/v1/models" || failed=1
  compose ps --all
  return "$failed"
}

# count_errors reports error-level lines since a timestamp without printing
# them: a deployment that still has the access log on would echo credentials.
#
# count_errors 统计某个时间点以来的错误级日志行数而不打印内容：仍开着访问日志的部署会把
# 凭据回显出来。
count_errors() {
  local since="$1" count
  count="$(compose logs --since "$since" controlplane gateway registry 2>/dev/null |
    grep -ciE '"level":"(error|fatal)"|level=(error|fatal)|panic' || true)"
  if (( count > 0 )); then
    warn "启动后出现 $count 行错误日志：docker compose logs --since $since controlplane gateway registry"
  else
    log "启动后没有错误日志"
  fi
}

# backup_state writes a MySQL dump and a Registry identity archive into dir.
# The dump uses the application account through MYSQL_PWD inside the
# container, so no password appears in arguments on this host. A failed step
# leaves no file behind: a partial file is not a backup.
#
# backup_state 把一份 MySQL 转储与一份 Registry 身份材料归档写入 dir。转储在容器内经由
# MYSQL_PWD 使用应用账号，因此本机命令参数里不出现密码。失败的步骤不留下文件：残缺的文件
# 不算备份。
backup_state() {
  local dir="$1" stamp sql registry
  stamp="$(date +%Y%m%d-%H%M%S)"
  (umask 077 && mkdir -p "$dir")
  sql="$dir/mysql-$stamp.sql.gz"
  registry="$dir/registry-data-$stamp.tar.gz"

  if [[ -n "$(compose ps --status running -q mysql 2>/dev/null)" ]]; then
    log "备份 MySQL → $sql"
    if ! (umask 077 && set -o pipefail && compose exec -T mysql sh -c \
      'MYSQL_PWD="$MYSQL_PASSWORD" exec mysqldump -u"$MYSQL_USER" --single-transaction --quick --no-tablespaces --set-gtid-purged=OFF --skip-masking-policies --skip-add-drop-table "$MYSQL_DATABASE"' |
      gzip >"$sql"); then
      rm -f "$sql"
      die "MySQL 备份失败，已中止"
    fi
  else
    warn "mysql 未运行，跳过数据库备份"
  fi

  if volume_exists registry-data; then
    log "备份 Registry 身份材料 → $registry"
    if ! (umask 077 && compose run --rm --no-deps -T --entrypoint tar registry-init czf - -C /data . >"$registry"); then
      rm -f "$registry"
      die "Registry 身份材料备份失败，已中止"
    fi
  fi
}

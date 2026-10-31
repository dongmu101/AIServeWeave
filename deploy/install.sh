#!/usr/bin/env bash
# install.sh performs a first Docker Compose deployment on one host: it
# fills deploy/.env, builds the images one at a time, starts the stack and
# checks that Console, the control plane and the Gateway answer. It is
# idempotent: a value already in .env is kept, so rerunning it after a
# failure resumes rather than regenerating secrets.
#
# install.sh 在单台主机上完成首次 Docker Compose 部署：填充 deploy/.env、逐个构建镜像、
# 启动整套服务，并检查 Console、控制面与 Gateway 是否应答。脚本是幂等的：.env 里已有的值
# 会保留，失败后重跑是接着做，而不是重新生成密钥。
#
# Usage:
#   deploy/install.sh --owner-email owner@example.com
#   deploy/install.sh --owner-email a@example.com --operator-email ops@example.com --https --goproxy https://goproxy.cn,direct
#   deploy/install.sh --help
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

usage() {
  cat <<'EOF'
用法: deploy/install.sh [选项]

账户（未在 .env 中设置时生效；密码留空则自动生成并在结束时显示一次）:
  --owner-email EMAIL        创建默认租户及其 owner
  --owner-password PASS
  --tenant-name NAME         默认租户名（默认 Default）
  --operator-email EMAIL     创建平台运维账户
  --operator-password PASS

部署选项:
  --https                    Console 部署在 HTTPS 反向代理之后，启用 Secure Cookie
  --signup                   开启租户自助注册（控制面与 Console 同时开启）
  --advertise-addr HOST:PORT Agent 拨号的 Gateway 隧道地址（默认 127.0.0.1:8443）
  --local-discovery          保留 Gateway 对 host.docker.internal 上 Ollama/vLLM 的发现
                             （Linux 原生 Docker 默认关闭，因为该主机名无法解析）
  --goproxy URL              构建时的 Go 模块代理，如 https://goproxy.cn,direct
  --no-build                 只准备配置，不构建、不启动
  -h, --help                 显示本帮助
EOF
}

OWNER_EMAIL="" OWNER_PASSWORD="" TENANT_NAME="" OPERATOR_EMAIL="" OPERATOR_PASSWORD=""
HTTPS=0 SIGNUP=0 ADVERTISE_ADDR="" LOCAL_DISCOVERY=0 NO_BUILD=0 GOPROXY_ARG=""
while (($# > 0)); do
  case "$1" in
    --owner-email) OWNER_EMAIL="${2:?}"; shift 2 ;;
    --owner-password) OWNER_PASSWORD="${2:?}"; shift 2 ;;
    --tenant-name) TENANT_NAME="${2:?}"; shift 2 ;;
    --operator-email) OPERATOR_EMAIL="${2:?}"; shift 2 ;;
    --operator-password) OPERATOR_PASSWORD="${2:?}"; shift 2 ;;
    --https) HTTPS=1; shift ;;
    --signup) SIGNUP=1; shift ;;
    --advertise-addr) ADVERTISE_ADDR="${2:?}"; shift 2 ;;
    --local-discovery) LOCAL_DISCOVERY=1; shift ;;
    --goproxy) GOPROXY_ARG="${2:?}"; shift 2 ;;
    --no-build) NO_BUILD=1; shift ;;
    -h | --help) usage; exit 0 ;;
    *) usage >&2; die "未知参数: $1" ;;
  esac
done

# The bootstrap service builds its JSON by hand, so a quote, backslash or
# newline in these values would produce a request the control plane rejects.
#
# bootstrap 服务手工拼接 JSON，因此这些值里的引号、反斜杠或换行会拼出控制面拒绝的请求。
for value in "$OWNER_EMAIL" "$OWNER_PASSWORD" "$TENANT_NAME" "$OPERATOR_EMAIL" "$OPERATOR_PASSWORD"; do
  [[ "$value" != *[\"\\]* && "$value" != *$'\n'* ]] || die "账户名称、邮箱与密码不能包含双引号、反斜杠或换行"
done

require_tools openssl curl awk
check_memory

# ---- .env ----
ensure_env
fill_secrets

# Database passwords only take effect when the MySQL volume is first
# initialised; setting them for an existing volume would leave the control
# plane unable to log in.
#
# 数据库密码只在 MySQL 数据卷首次初始化时生效；为已有数据卷设置它们，会让控制面无法登录。
if volume_exists mysql-data; then
  [[ -n "$(env_get MYSQL_PASSWORD)" ]] || warn "MySQL 数据卷已存在且使用默认密码，脚本不改动；改密见 deploy/README.md"
else
  for key in MYSQL_PASSWORD MYSQL_ROOT_PASSWORD; do
    [[ -n "$(env_get "$key")" ]] || env_set "$key" "$(openssl rand -hex 24)"
  done
fi

GENERATED=()
[[ -z "$TENANT_NAME" ]] || env_set AISW_DEFAULT_TENANT_NAME "$TENANT_NAME"
if [[ -n "$OWNER_EMAIL" ]]; then
  env_set AISW_DEFAULT_OWNER_EMAIL "$OWNER_EMAIL"
  [[ -z "$OWNER_PASSWORD" ]] || env_set AISW_DEFAULT_OWNER_PASSWORD "$OWNER_PASSWORD"
fi
if [[ -n "$OPERATOR_EMAIL" ]]; then
  env_set AISW_DEFAULT_OPERATOR_EMAIL "$OPERATOR_EMAIL"
  [[ -z "$OPERATOR_PASSWORD" ]] || env_set AISW_DEFAULT_OPERATOR_PASSWORD "$OPERATOR_PASSWORD"
fi
if [[ -n "$(env_get AISW_DEFAULT_OWNER_EMAIL)" && -z "$(env_get AISW_DEFAULT_OWNER_PASSWORD)" ]]; then
  env_set AISW_DEFAULT_OWNER_PASSWORD "$(openssl rand -hex 12)"
  GENERATED+=("租户 owner  $(env_get AISW_DEFAULT_OWNER_EMAIL)  密码 $(env_get AISW_DEFAULT_OWNER_PASSWORD)")
fi
if [[ -n "$(env_get AISW_DEFAULT_OPERATOR_EMAIL)" && -z "$(env_get AISW_DEFAULT_OPERATOR_PASSWORD)" ]]; then
  env_set AISW_DEFAULT_OPERATOR_PASSWORD "$(openssl rand -hex 12)"
  GENERATED+=("平台运维    $(env_get AISW_DEFAULT_OPERATOR_EMAIL)  密码 $(env_get AISW_DEFAULT_OPERATOR_PASSWORD)")
fi
[[ -n "$(env_get AISW_DEFAULT_OWNER_EMAIL)$(env_get AISW_DEFAULT_OPERATOR_EMAIL)" ]] ||
  warn "未设置 owner 与运维邮箱，bootstrap 不会建号；可用 --owner-email 重跑"

(( HTTPS == 0 )) || env_set AISW_CONSOLE_COOKIE_SECURE true
[[ -z "$ADVERTISE_ADDR" ]] || env_set GATEWAY_ADVERTISE_ADDR "$ADVERTISE_ADDR"

# host.docker.internal exists only under Docker Desktop. Clearing the example
# defaults on Linux skips a discovery that could never succeed; a value the
# operator changed is left alone.
#
# host.docker.internal 只在 Docker Desktop 下存在。在 Linux 上清空示例默认值，跳过一次注定
# 失败的发现；运维改过的值保持不动。
if (( LOCAL_DISCOVERY == 0 )) && [[ "$(uname -s)" == Linux ]]; then
  for pair in GATEWAY_LOCAL_OLLAMA_URL=http://host.docker.internal:11434 GATEWAY_LOCAL_VLLM_URL=http://host.docker.internal:8000; do
    [[ "$(env_get "${pair%%=*}")" != "${pair#*=}" ]] || env_set "${pair%%=*}" ""
  done
fi

# ---- controlplane.yaml / override ----
ensure_controlplane_config
ensure_access_log_off
ensure_tenant_nodes
ensure_config_readable
if (( SIGNUP == 1 )); then
  if ! grep -q '^Signup:' controlplane.yaml; then
    cat >>controlplane.yaml <<'EOF'

# Self-service tenant sign-up, enabled by install.sh --signup.
# 租户自助注册，由 install.sh --signup 开启。
Signup:
  Enabled: true
  MaxPerHour: 10
  RequestsPerMinute: 60
  TokensPerMinute: 100000
  MaxConcurrent: 4
EOF
    log "已在 controlplane.yaml 中开启自助注册"
  fi
  if [[ ! -f docker-compose.override.yaml ]]; then
    cat >docker-compose.override.yaml <<'EOF'
# Local overrides for this host, written by install.sh.
# 本机的本地覆盖，由 install.sh 写入。
services:
  console:
    environment:
      AISW_CONSOLE_SIGNUP_ENABLED: "true"
EOF
    log "已写入 docker-compose.override.yaml 开启 Console 注册页"
  elif ! grep -q AISW_CONSOLE_SIGNUP_ENABLED docker-compose.override.yaml; then
    warn "docker-compose.override.yaml 已存在，请手动在 services.console.environment 下加 AISW_CONSOLE_SIGNUP_ENABLED: \"true\""
  fi
fi

compose config --quiet || die "compose 配置校验失败"
log ".env 与 compose 配置就绪"

if (( NO_BUILD == 1 )); then
  log "--no-build：配置已准备好，未构建、未启动"
  exit 0
fi

# ---- build and start ----
START="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
build_images "$(source_version)"
log "启动服务"
compose up -d

code="$(wait_exited bootstrap 180)"
compose logs --no-log-prefix bootstrap | tail -5
[[ "$code" == 0 ]] || warn "bootstrap 未成功结束（${code}），查看：docker compose logs bootstrap"

health_check || warn "部分服务未就绪，见上方提示"
count_errors "$START"

console_port="$(env_get CONSOLE_PORT)"
echo
log "部署完成"
echo "  租户登录:  http://127.0.0.1:${console_port:-3000}/login"
echo "  运维登录:  http://127.0.0.1:${console_port:-3000}/operator/login"
echo "  端口只绑定 127.0.0.1；远程访问用 SSH 隧道或 HTTPS 反向代理（反代后用 --https 重跑）"
if (( ${#GENERATED[@]} > 0 )); then
  echo
  echo "  自动生成的账户密码（只显示这一次，也保存在 deploy/.env）:"
  printf '    %s\n' "${GENERATED[@]}"
fi
echo
echo "  以后更新代码重新部署: deploy/upgrade.sh"

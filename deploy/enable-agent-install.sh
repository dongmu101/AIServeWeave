#!/usr/bin/env bash
# enable-agent-install.sh turns on the one-line Agent install for an existing
# Docker Compose deployment in one go: it writes AGENT_PUBLIC_URL into .env,
# makes sure controlplane.yaml has the AgentInstall block, rebuilds the Gateway
# the control plane and the Console WITH a version, recreates them and then checks the whole
# chain. The Nginx forwarding for /agent/ stays manual; the script tells you
# whether it is missing and prints what to add.
#
# enable-agent-install.sh 一步为已有的 Docker Compose 部署开启 Agent 一行命令安装：把
# AGENT_PUBLIC_URL 写进 .env，确保 controlplane.yaml 有 AgentInstall 段，带版本重建 Gateway
# 控制面与 Console，重建容器，再逐段自检。/agent/ 的 Nginx 转发仍需手动配置；脚本会告诉你它是否缺失，
# 并打印要加的内容。
#
# Usage:
#   deploy/enable-agent-install.sh https://open7x.com
#   deploy/enable-agent-install.sh https://open7x.com --platforms "linux/amd64 darwin/arm64 windows/amd64"
#   deploy/enable-agent-install.sh https://open7x.com --no-build   # reuse images built with a version
#   deploy/enable-agent-install.sh --help
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

usage() {
  cat <<'EOF'
用法: deploy/enable-agent-install.sh 平台站点地址 [选项]

  平台站点地址        平台对外的 HTTPS 站点根地址，如 https://open7x.com
  --platforms "..."   只把这些平台的 Agent 打进 Gateway 镜像，如 "linux/amd64 darwin/arm64 windows/amd64"
                      （默认六个平台，构建约 10 分钟；验收时三个就够）
  --goproxy URL       构建时的 Go 模块代理，如 https://goproxy.cn,direct
  --no-build          不构建，沿用已带版本构建的镜像
  -h, --help          显示本帮助

脚本会：写 .env、补 controlplane.yaml、带版本重建 gateway、controlplane 与 console、重建容器，
然后检查 Gateway 本机入口、安装脚本和（若可达）公网入口。工作区必须干净，否则版本带 -dirty，
安装脚本会拒绝。
EOF
}

URL="" PLATFORMS="" GOPROXY_ARG="" BUILD=1
while (($# > 0)); do
  case "$1" in
    --platforms) PLATFORMS="${2:?}"; shift 2 ;;
    --goproxy) GOPROXY_ARG="${2:?}"; shift 2 ;;
    --no-build) BUILD=0; shift ;;
    -h | --help) usage; exit 0 ;;
    -*) die "未知选项 $1（--help 查看用法）" ;;
    *) [[ -z "$URL" ]] || die "只能给一个站点地址"; URL="$1"; shift ;;
  esac
done

[[ -n "$URL" ]] || { usage; exit 1; }
URL="${URL%/}"
[[ "$URL" =~ ^https://[^/?#[:space:]]+$ ]] || die "站点地址必须是不带路径的 https 根地址，如 https://open7x.com（收到 ${URL}）"

require_tools curl git
[[ -f "$ENV_FILE" ]] || die "找不到 ${ENV_FILE}：先用 deploy/install.sh 完成首次部署"
[[ -f "$DEPLOY_DIR/controlplane.yaml" ]] || die "deploy/controlplane.yaml 不存在或不是文件（Docker 在文件缺失时会建同名空目录，删掉它）：先用 deploy/install.sh 完成首次部署"

# ---- 1. version / 版本 ----
VERSION="$(source_version)"
case "$VERSION" in
  dev) die "取不到 git 版本（不是 git 检出，或 git 不可用）；安装脚本会拒绝 dev 版本" ;;
  *-dirty)
    git -C "$ROOT_DIR" status --short | head -10 >&2
    die "工作区有未提交的改动，版本是 ${VERSION}，安装脚本会拒绝带 -dirty 的版本。提交或 git stash 后重试" ;;
esac
log "版本 ${VERSION}"

# ---- 2. config / 配置 ----
env_set AGENT_PUBLIC_URL "$URL"
[[ -z "$PLATFORMS" ]] || env_set AGENT_PLATFORMS "\"${PLATFORMS}\""
ensure_agent_install
if ! grep -A4 '^AgentInstall:' controlplane.yaml | grep -Eq 'PublicURL:[[:space:]]*"?(\$\{AISW_AGENT_PUBLIC_URL\}|'"${URL//./\\.}"')"?[[:space:]]*(#.*)?$'; then
  die "controlplane.yaml 的 AgentInstall.PublicURL 与 ${URL} 不一致。把它改成 PublicURL: \"\${AISW_AGENT_PUBLIC_URL}\"（跟随 .env）或 \"${URL}\"，再重跑"
fi
log "配置已就绪（.env: AGENT_PUBLIC_URL=${URL}）"

# ---- 3. build and recreate / 构建并重建 ----
if (( BUILD == 1 )); then
  check_memory
  build_images "$VERSION" controlplane gateway console
fi
log "重建 gateway、controlplane 与 console 容器"
compose up -d --no-build --force-recreate controlplane gateway console

# ---- 4. checks / 自检 ----
GATEWAY_PORT_VALUE="$(env_get GATEWAY_PORT)"
LOCAL="http://127.0.0.1:${GATEWAY_PORT_VALUE:-8080}"
problems=0

manifest=""
for ((i = 0; i < 30; i += 2)); do
  manifest="$(curl -s --max-time 5 "$LOCAL/agent/v1/manifest" || true)"
  [[ "$manifest" == *'"version"'* ]] && break
  sleep 2
done

if [[ "$manifest" != *'"version"'* ]]; then
  warn "Gateway 本机入口 ${LOCAL}/agent/v1/manifest 没有返回清单。看日志：docker compose logs --tail=50 gateway"
  warn "常见原因：.env 没有 AGENT_PUBLIC_URL，或镜像里没有 Agent 制品（构建失败）"
  problems=1
else
  served="$(printf '%s' "$manifest" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')"
  if [[ "$manifest" != *'"installable":true'* ]]; then
    warn "Gateway 版本是 ${served}，安装脚本会拒绝它。用本脚本（不带 --no-build）重建"
    problems=1
  elif [[ "$served" != "$VERSION" ]]; then
    warn "Gateway 的版本 ${served} 与检出版本 ${VERSION} 不一致：镜像不是这次构建的。去掉 --no-build 重跑"
    problems=1
  else
    log "Gateway 本机入口正常（版本 ${served}）"
  fi
  if curl -fsS --max-time 10 "$LOCAL/agent/install.sh" 2>/dev/null | grep -q '^PLATFORM_URL=__AISW_PLATFORM_URL__'; then
    warn "install.sh 里的平台地址占位符没有被替换"
    problems=1
  fi
fi

if curl -fsS --max-time 10 "$URL/agent/v1/manifest" 2>/dev/null | grep -q '"installable":true'; then
  log "公网入口正常：${URL}/agent/v1/manifest"
else
  warn "公网入口 ${URL}/agent/v1/manifest 不可用：反向代理还没有把 /agent/ 转给 Gateway（或 DNS/证书有问题）"
  cat >&2 <<EOF

在站点 ${URL#https://} 的 Nginx server 块里加入下面的内容，nginx -t && nginx -s reload 后重跑本脚本（加 --no-build）：

    location /agent/ {
        proxy_pass http://127.0.0.1:${GATEWAY_PORT_VALUE:-8080};
        proxy_set_header Host \$host;
        proxy_buffering off;
        client_max_body_size 4k;
    }

EOF
  problems=1
fi

if (( problems == 0 )); then
  cat <<EOF

一行命令安装已开启（版本 ${VERSION}）。下一步：
  1. 登录 Console，进入「Agent 接入」(/operator/agent-enrollment) 或「节点管理」(/console/nodes)。
  2. 在「添加节点」选系统和方式，点生成，复制命令。
  3. 在【节点机器】上执行（不是这台服务器），装完回到「节点管理」看节点是否在线。
EOF
else
  die "有未通过的检查，见上面的警告"
fi

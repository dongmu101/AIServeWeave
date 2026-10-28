#!/usr/bin/env bash
# upgrade.sh redeploys an existing Docker Compose installation from the
# latest commit of the checked-out branch: it fast-forwards the checkout,
# backs up MySQL and the Registry identity material, rebuilds the images one
# at a time and recreates only the containers whose image or configuration
# changed.
#
# Local edits to deploy/controlplane.yaml (sign-up, access-log settings) are
# stashed across the pull and restored afterwards; any other tracked change
# stops the upgrade, because a fast-forward must not silently merge it.
#
# upgrade.sh 用当前分支的最新提交重新部署一套已有的 Docker Compose 安装：快进代码、备份
# MySQL 与 Registry 身份材料、逐个重建镜像，并且只重建镜像或配置有变化的容器。
#
# deploy/controlplane.yaml 的本地修改（自助注册、访问日志设置）在拉取期间暂存、之后恢复；
# 其他任何已跟踪文件的改动都会让升级停下，因为快进不能悄悄把它们合并掉。
#
# Usage:
#   deploy/upgrade.sh                      # pull, back up, rebuild everything that changed
#   deploy/upgrade.sh gateway console      # rebuild only these services
#   deploy/upgrade.sh --no-pull --force    # rebuild the current checkout as it is
#   deploy/upgrade.sh --help
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

usage() {
  cat <<'EOF'
用法: deploy/upgrade.sh [选项] [服务...]

  服务...           只重建这些服务（registry-init registry controlplane gateway console），默认全部
  --no-pull         不拉取代码，直接用当前工作区重建
  --force           代码没有新提交也照常重建
  --no-backup       跳过 MySQL 与 Registry 备份
  --backup-dir DIR  备份目录（默认 ~/aiserveweave-backups）
  --goproxy URL     构建时的 Go 模块代理，如 https://goproxy.cn,direct
  --prune           完成后清理悬空镜像与构建缓存
  -h, --help        显示本帮助
EOF
}

ORIGINAL_ARGS=("$@")
PULL=1 FORCE=0 BACKUP=1 PRUNE=0 FROM="" GOPROXY_ARG=""
BACKUP_DIR="${AISW_BACKUP_DIR:-$HOME/aiserveweave-backups}"
SERVICES=()
while (($# > 0)); do
  case "$1" in
    --no-pull) PULL=0; shift ;;
    --force) FORCE=1; shift ;;
    --no-backup) BACKUP=0; shift ;;
    --backup-dir) BACKUP_DIR="${2:?}"; shift 2 ;;
    --goproxy) GOPROXY_ARG="${2:?}"; shift 2 ;;
    --prune) PRUNE=1; shift ;;
    --from) FROM="${2:?}"; shift 2 ;; # internal: set by the re-exec below / 内部参数，由下面的重新执行设置
    -h | --help) usage; exit 0 ;;
    -*) usage >&2; die "未知参数: $1" ;;
    *)
      [[ " $BUILD_ORDER " == *" $1 "* ]] || die "未知服务: ${1}（可选: ${BUILD_ORDER}）"
      SERVICES+=("$1"); shift ;;
  esac
done

require_tools git curl awk gzip
[[ -f "$ENV_FILE" ]] || die "deploy/.env 不存在：这台主机还没部署过，先运行 deploy/install.sh"

# ---- pull ----
if (( PULL == 1 )); then
  git -C "$ROOT_DIR" rev-parse --abbrev-ref '@{u}' >/dev/null 2>&1 ||
    die "当前分支没有上游分支，无法拉取；用 git branch --set-upstream-to 设置，或加 --no-pull"

  dirty="$(git -C "$ROOT_DIR" status --porcelain --untracked-files=no | grep -v ' deploy/controlplane.yaml$' || true)"
  [[ -z "$dirty" ]] || die "以下已跟踪文件有本地修改，先提交或还原后再升级:
$dirty"

  log "拉取 $(git -C "$ROOT_DIR" rev-parse --abbrev-ref '@{u}')"
  git -C "$ROOT_DIR" fetch --quiet
  old="$(git -C "$ROOT_DIR" rev-parse HEAD)"
  new="$(git -C "$ROOT_DIR" rev-parse '@{u}')"
  if [[ "$old" == "$new" ]]; then
    (( FORCE == 1 )) || { log "已是最新（${old:0:12}），无需重新部署；要强制重建加 --force"; exit 0; }
  else
    stashed=0
    if ! git -C "$ROOT_DIR" diff --quiet -- deploy/controlplane.yaml; then
      git -C "$ROOT_DIR" stash push --quiet -m "upgrade.sh $(date +%F-%H%M%S)" -- deploy/controlplane.yaml
      stashed=1
    fi
    if ! git -C "$ROOT_DIR" merge --ff-only --quiet '@{u}'; then
      (( stashed == 0 )) || git -C "$ROOT_DIR" stash pop --quiet
      die "无法快进到上游（本地有未推送的提交？），代码未改动"
    fi
    if (( stashed == 1 )) && ! git -C "$ROOT_DIR" stash pop --quiet; then
      die "恢复 deploy/controlplane.yaml 本地修改时冲突。代码已更新到 ${new:0:12}，服务未改动。
  手工解决 deploy/controlplane.yaml 中的冲突标记后执行:
    git -C $ROOT_DIR reset -q -- deploy/controlplane.yaml && git -C $ROOT_DIR stash drop
  再运行: deploy/upgrade.sh --no-pull --force"
    fi
    log "代码 ${old:0:12} → ${new:0:12}"

    # Re-run the freshly pulled copy of this script, so the build and start
    # steps below are the new version's rather than the one already in memory.
    #
    # 重新执行刚拉取下来的这份脚本，让下面的构建与启动步骤用新版本，而不是已经读进内存的旧版本。
    exec bash "$DEPLOY_DIR/upgrade.sh" --no-pull --force --from "$old" ${ORIGINAL_ARGS[@]+"${ORIGINAL_ARGS[@]}"}
  fi
fi

# ---- configuration that a new version may require ----
ensure_env
fill_secrets
ensure_access_log_off
ensure_config_readable
if [[ -n "$FROM" ]]; then
  added="$(git -C "$ROOT_DIR" diff "$FROM" HEAD -- deploy/.env.example | sed -n 's/^+\([A-Z_][A-Z0-9_]*\)=.*/\1/p' || true)"
  [[ -z "$added" ]] || warn "新版本 .env.example 新增了变量，确认 deploy/.env 是否需要设置: $(echo $added)"
  git -C "$ROOT_DIR" diff --quiet "$FROM" HEAD -- deploy/controlplane.yaml ||
    warn "新版本改动了 deploy/controlplane.yaml，确认与本地修改合并后的结果符合预期"
fi
compose config --quiet || die "compose 配置校验失败，服务未改动"

# ---- backup, build, start ----
(( BACKUP == 0 )) || backup_state "$BACKUP_DIR"

check_memory
build_images "$(source_version)" ${SERVICES[@]+"${SERVICES[@]}"}

START="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
log "重建有变化的容器（Gateway 重建会断开隧道，Agent 自动重连）"
compose up -d

health_check || warn "部分服务未就绪，见上方提示"
count_errors "$START"

if (( PRUNE == 1 )); then
  log "清理悬空镜像与构建缓存"
  docker image prune -f >/dev/null
  docker builder prune -f --keep-storage 5GB >/dev/null
fi

echo
log "重新部署完成：$(source_version)"
if [[ -n "$FROM" ]]; then
  echo "  回滚代码: cd $ROOT_DIR && git stash && git checkout ${FROM:0:12} && git stash pop && deploy/upgrade.sh --no-pull --force --no-backup"
  echo "            回滚后处于 detached HEAD，恢复跟踪分支用 git checkout <分支名>"
fi
(( BACKUP == 0 )) || echo "  本次备份: ${BACKUP_DIR}（数据库恢复见 deploy/database-recovery.md）"

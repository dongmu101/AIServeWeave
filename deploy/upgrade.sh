#!/usr/bin/env bash
# upgrade.sh redeploys an existing Docker Compose installation from the
# latest commit of the checked-out branch: it fast-forwards the checkout,
# rebuilds the images one at a time (or, with AISW_IMAGE_TAG set, pulls that
# release's prebuilt images), backs up MySQL and the Registry identity
# material and recreates only the containers whose image or configuration
# changed.
#
# deploy/controlplane.yaml is each deployment's own file: it is created from
# controlplane.example.yaml when missing and is never tracked, so a pull
# leaves it alone. A checkout from before that change still tracks it; the
# first upgrade past it sets the local file aside across the pull and puts it
# back untracked. Any other tracked change stops the upgrade, because a
# fast-forward must not silently merge it.
#
# upgrade.sh 用当前分支的最新提交重新部署一套已有的 Docker Compose 安装：快进代码、逐个重建
# 镜像（设置了 AISW_IMAGE_TAG 时改为拉取该 release 的预构建镜像）、备份 MySQL 与 Registry
# 身份材料，并且只重建镜像或配置有变化的容器。
#
# deploy/controlplane.yaml 属于各个部署自己：缺失时由 controlplane.example.yaml 生成，不受 git
# 跟踪，拉取不会动它。早于这一调整的检出仍跟踪该文件；越过这一调整的第一次升级会在拉取期间
# 把本地文件挪到一边，拉取后以未跟踪文件放回。其他任何已跟踪文件的改动都会让升级停下，因为
# 快进不能悄悄把它们合并掉。
#
# Usage:
#   deploy/upgrade.sh                      # pull, back up, rebuild everything that changed
#   deploy/upgrade.sh gateway console      # rebuild only these services
#   deploy/upgrade.sh --no-pull            # rebuild the current checkout as it is
#   deploy/upgrade.sh --image-tag v0.2.0   # pull that release's images instead of building
#   deploy/upgrade.sh --no-build           # start images already loaded here (make push)
#   deploy/upgrade.sh --help
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

usage() {
  cat <<'EOF'
用法: deploy/upgrade.sh [选项] [服务...]

  服务...           只重建这些服务（registry-init registry controlplane gateway console），默认全部
  --no-pull         不拉取代码，直接用当前工作区重建
  --force           兼容旧用法，无作用（代码没有新提交时也会照常重建）
  --no-backup       跳过 MySQL 与 Registry 备份
  --backup-dir DIR  备份目录（默认 ~/aiserveweave-backups）
  --image-tag TAG   改为拉取该 release 的预构建镜像（写入 .env 的 AISW_IMAGE_TAG）；
                    传空字符串 '' 切回源码构建。拉取模式下总是拉取全部服务的镜像
  --no-build        不构建也不拉取，直接启动本机已有的镜像（在别处构建后 load 进来，见根目录
                    Makefile 的 make push）；缺少镜像时中止
  --goproxy URL     源码构建时的 Go 模块代理，如 https://goproxy.cn,direct
  --prune           完成后清理悬空镜像、旧命名的镜像与构建缓存
  -h, --help        显示本帮助
EOF
}

ORIGINAL_ARGS=("$@")
PULL=1 BACKUP=1 PRUNE=0 FROM="" GOPROXY_ARG=""
SET_TAG=0 IMAGE_TAG_ARG="" NO_BUILD=0
BACKUP_DIR="${AISW_BACKUP_DIR:-$HOME/aiserveweave-backups}"
SERVICES=()
while (($# > 0)); do
  case "$1" in
    --no-pull) PULL=0; shift ;;
    --force) shift ;; # accepted for old commands; a rebuild always runs / 兼容旧命令；总会重建
    --no-backup) BACKUP=0; shift ;;
    --no-build) NO_BUILD=1; shift ;;
    --backup-dir) BACKUP_DIR="${2:?}"; shift 2 ;;
    --goproxy) GOPROXY_ARG="${2:?}"; shift 2 ;;
    --image-tag)
      (($# >= 2)) || die "--image-tag 需要一个值（切回源码构建用 --image-tag ''）"
      IMAGE_TAG_ARG="$2"; SET_TAG=1; shift 2 ;;
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
    # An up-to-date checkout says nothing about the running containers: a
    # manual git pull leaves them on the old build. So keep going; unchanged
    # images come from the build cache and compose recreates only what changed.
    #
    # 代码已是最新不代表容器已是最新：手动 git pull 之后容器仍是旧版本。所以照常继续；
    # 没变化的镜像走构建缓存，compose 只重建有变化的容器。
    log "代码已是最新（${old:0:12}），按当前代码重建；未变化的镜像走构建缓存，容器只在有变化时重建"
  else
    # Whether the old and new commits track controlplane.yaml decides how
    # the local copy survives the fast-forward.
    #
    # 新旧两个提交是否跟踪 controlplane.yaml，决定本地这份配置如何在快进中保留下来。
    config="$DEPLOY_DIR/controlplane.yaml"
    aside="$DEPLOY_DIR/.controlplane.yaml.upgrade"
    old_tracks=0 new_tracks=0
    git -C "$ROOT_DIR" ls-files --error-unmatch deploy/controlplane.yaml >/dev/null 2>&1 && old_tracks=1
    git -C "$ROOT_DIR" cat-file -e "$new:deploy/controlplane.yaml" 2>/dev/null && new_tracks=1
    (( old_tracks == 1 || new_tracks == 0 )) ||
      die "上游 ${new:0:12} 重新跟踪了 deploy/controlplane.yaml，快进会覆盖本机配置；先备份该文件再手工处理，代码未改动"
    [[ ! -e "$aside" ]] ||
      die "$aside 已存在（上次升级中断？），确认其内容后移走再升级，代码未改动"

    stashed=0 moved=0
    if (( old_tracks == 1 && new_tracks == 1 )); then
      # Both still track it: carry the local edits across with a stash.
      # 两边都仍跟踪：用 stash 把本地修改带过快进。
      if ! git -C "$ROOT_DIR" diff --quiet -- deploy/controlplane.yaml; then
        git -C "$ROOT_DIR" stash push --quiet -m "upgrade.sh $(date +%F-%H%M%S)" -- deploy/controlplane.yaml
        stashed=1
      fi
    elif (( old_tracks == 1 )); then
      # The new commit stops tracking it: set the local copy aside so the
      # fast-forward can delete the tracked one, then put it back untracked.
      # 新提交不再跟踪：先把本地这份挪开，让快进删除被跟踪的那份，再以未跟踪文件放回。
      if [[ -f "$config" ]]; then
        cp -p "$config" "$aside"
        moved=1
      fi
      git -C "$ROOT_DIR" checkout --quiet -- deploy/controlplane.yaml
    fi

    if ! git -C "$ROOT_DIR" merge --ff-only --quiet '@{u}'; then
      (( stashed == 0 )) || git -C "$ROOT_DIR" stash pop --quiet
      (( moved == 0 )) || mv -f "$aside" "$config"
      die "无法快进到上游（本地有未推送的提交？），代码未改动"
    fi
    if (( stashed == 1 )) && ! git -C "$ROOT_DIR" stash pop --quiet; then
      die "恢复 deploy/controlplane.yaml 本地修改时冲突。代码已更新到 ${new:0:12}，服务未改动。
  手工解决 deploy/controlplane.yaml 中的冲突标记后执行:
    git -C $ROOT_DIR reset -q -- deploy/controlplane.yaml && git -C $ROOT_DIR stash drop
  再运行: deploy/upgrade.sh --no-pull"
    fi
    if (( moved == 1 )); then
      mv -f "$aside" "$config"
      log "deploy/controlplane.yaml 已改为本机文件、不再受 git 跟踪，内容保持原样"
    fi
    log "代码 ${old:0:12} → ${new:0:12}"

    # Re-run the freshly pulled copy of this script, so the build and start
    # steps below are the new version's rather than the one already in memory.
    #
    # 重新执行刚拉取下来的这份脚本，让下面的构建与启动步骤用新版本，而不是已经读进内存的旧版本。
    exec bash "$DEPLOY_DIR/upgrade.sh" --no-pull --from "$old" ${ORIGINAL_ARGS[@]+"${ORIGINAL_ARGS[@]}"}
  fi
fi

# ---- configuration that a new version may require ----
ensure_env
OLD_TAG="$(image_tag)"
if (( SET_TAG == 1 )); then
  [[ -z "${AISW_IMAGE_TAG+x}" ]] || warn "shell 环境里设置了 AISW_IMAGE_TAG，它优先于 .env，--image-tag 写入 .env 的值不会生效"
  env_set AISW_IMAGE_TAG "$IMAGE_TAG_ARG"
fi
fill_secrets
ensure_controlplane_config
ensure_access_log_off
ensure_tenant_nodes
ensure_agent_install
ensure_remote_providers
ensure_config_readable
if [[ -n "$FROM" ]]; then
  added="$(git -C "$ROOT_DIR" diff "$FROM" HEAD -- deploy/.env.example | sed -n 's/^+\([A-Z_][A-Z0-9_]*\)=.*/\1/p' || true)"
  [[ -z "$added" ]] || warn "新版本 .env.example 新增了变量，确认 deploy/.env 是否需要设置: $(echo $added)"
  git -C "$ROOT_DIR" diff --quiet "$FROM" HEAD -- deploy/controlplane.example.yaml ||
    warn "新版本改动了 deploy/controlplane.example.yaml，对照差异确认本机 deploy/controlplane.yaml 是否需要同步: git -C $ROOT_DIR diff ${FROM:0:12} HEAD -- deploy/controlplane.example.yaml"
fi
compose config --quiet || die "compose 配置校验失败，服务未改动"

# ---- images, backup, start ----
# Images come first: building or pulling changes nothing that is running, and
# the Registry backup runs in the registry-init image, which must already
# exist so compose does not build it on the spot.
#
# 先准备镜像：构建或拉取不改动任何正在运行的东西，而 Registry 备份在 registry-init 镜像里
# 执行，镜像须已存在，compose 才不会当场构建它。
UP_FLAGS="$(up_flags)"
if (( NO_BUILD == 1 )); then
  require_images
  log "沿用本机已有的镜像，不构建也不拉取"
  UP_FLAGS=--no-build
else
  if [[ -n "$(image_tag)" && ${#SERVICES[@]} -gt 0 ]]; then
    log "拉取模式下拉取全部服务的镜像；镜像未变化的容器不会重建"
  fi
  prepare_images "$(source_version)" ${SERVICES[@]+"${SERVICES[@]}"}
fi

(( BACKUP == 0 )) || backup_state "$BACKUP_DIR"

START="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
log "重建有变化的容器（Gateway 重建会断开隧道，Agent 自动重连）"
# shellcheck disable=SC2086 # empty or one flag / 为空或一个参数
compose up -d $UP_FLAGS

health_check || warn "部分服务未就绪，见上方提示"
count_errors "$START"

if (( PRUNE == 1 )); then
  log "清理悬空镜像、旧命名的镜像与构建缓存"
  docker image prune -f >/dev/null
  # Images were once named <project>-<service>; nothing uses those names now.
  # 镜像曾以 <项目名>-<服务名> 命名，现在已没有任何东西使用这些名字。
  for service in $BUILD_ORDER; do
    docker image rm "$(project_name)-${service}" >/dev/null 2>&1 || true
  done
  docker builder prune -f --keep-storage 2GB >/dev/null
fi

echo
NEW_TAG="$(image_tag)"
if [[ -n "$NEW_TAG" ]]; then
  log "重新部署完成：镜像 ${NEW_TAG}，代码 $(source_version)"
else
  log "重新部署完成：$(source_version)"
fi
if [[ "$OLD_TAG" != "$NEW_TAG" ]]; then
  echo "  回滚镜像: deploy/upgrade.sh --no-pull --no-backup --image-tag '${OLD_TAG}'"
fi
if [[ -n "$FROM" ]]; then
  echo "  回滚代码: cd $ROOT_DIR && cp deploy/controlplane.yaml deploy/controlplane.yaml.bak && git stash && git checkout ${FROM:0:12} && git stash pop && cp deploy/controlplane.yaml.bak deploy/controlplane.yaml && deploy/upgrade.sh --no-pull --no-backup"
  echo "            旧版本若仍跟踪 controlplane.yaml，checkout 会覆盖它，所以先备份再放回"
  echo "            回滚后处于 detached HEAD，恢复跟踪分支用 git checkout <分支名>"
fi
(( BACKUP == 0 )) || echo "  本次备份: ${BACKUP_DIR}（数据库恢复见 deploy/database-recovery.md）"

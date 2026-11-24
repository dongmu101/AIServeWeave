#!/usr/bin/env bash
# build-go.sh compiles the Go services for their container images on the host,
# into .image-build/<SERVICE>/, which the root Dockerfile only copies: the
# image build never runs the Go toolchain. CGO is off, so every target
# platform is a plain cross-compile with the same flags as build-release.sh.
# For the Gateway it also lays out the Agent artifacts the image serves under
# /agent/download/<VERSION>/.
#
# build-go.sh 在宿主机上为容器镜像编译 Go 服务，产物放进 .image-build/<SERVICE>/，根目录
# Dockerfile 只拷贝它们：镜像构建从不运行 Go 工具链。CGO 关闭，每个目标平台都是普通的
# 交叉编译，参数与 build-release.sh 相同。Gateway 还会摆出镜像在 /agent/download/<VERSION>/
# 下提供的 Agent 制品。
#
# With PREBUILT=DIR it compiles nothing and takes the binaries from
# build-release.sh's output in DIR instead, after checking them against
# DIR/SHA256SUMS.txt, so the images carry the very bytes of the GitHub Release.
#
# 设置 PREBUILT=DIR 时不编译，改为从 DIR 中 build-release.sh 的产物取二进制，先对照
# DIR/SHA256SUMS.txt 校验，使镜像里的正是 GitHub Release 的那批字节。
#
# Usage / 用法:
#   scripts/build-go.sh                                     # 三个服务，本机 Docker 的平台
#   VERSION=v3.1.7 PLATFORMS="linux/amd64 linux/arm64" scripts/build-go.sh aiServeWeaveGateway
#   PREBUILT=dist VERSION=v3.1.7 PLATFORMS="linux/amd64 linux/arm64" scripts/build-go.sh aiServeWeaveGateway
#
# Environment / 环境变量:
#   VERSION          stamped into main.version（默认 git describe，与 Makefile、deploy/lib.sh 相同）
#   PLATFORMS        image platforms, linux/* only（默认本机 Docker daemon 的平台）
#   AGENT_PLATFORMS  Agent platforms baked into the Gateway（默认六个）
#   PREBUILT         build-release.sh output directory to copy from instead of compiling
#
# Layout per service / 每个服务的目录:
#   .image-build/<SERVICE>/<os>_<arch>/service     service binary / 服务二进制
#   .image-build/<SERVICE>/agent-dist/<VERSION>/   Gateway only; empty for the others / 仅 Gateway，其他服务为空目录
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

die() { echo "$*" >&2; exit 1; }

OUT_DIR=".image-build"
VERSION="${VERSION:-$(git describe --tags --always --dirty --abbrev=12 2>/dev/null || echo dev)}"
AGENT_PLATFORMS="${AGENT_PLATFORMS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64}"
PREBUILT="${PREBUILT:-}"
if [[ -z "${PLATFORMS:-}" ]]; then
  PLATFORMS="$(docker version --format '{{.Server.Os}}/{{.Server.Arch}}' 2>/dev/null)" ||
    die "无法从 Docker 取得镜像平台：启动 Docker，或设置 PLATFORMS（如 PLATFORMS=linux/amd64）"
fi
(( $# > 0 )) || set -- aiServeWeaveRegistry aiServeWeaveControlPlane aiServeWeaveGateway

if [[ -n "$PREBUILT" ]]; then
  [[ -f "$PREBUILT/SHA256SUMS.txt" ]] || die "${PREBUILT}/SHA256SUMS.txt 不存在：先 VERSION=${VERSION} scripts/build-release.sh"
else
  command -v go >/dev/null 2>&1 ||
    die "需要 Go 来编译镜像里的服务（go.mod 指定的版本；Go 1.21 以上会自动下载）。服务器上不想装 Go，可设置 AISW_IMAGE_TAG 改为拉取预构建镜像"
fi

sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$@"; else shasum -a 256 "$@"; fi; }

# compile builds one binary with exactly build-release.sh's flags.
# compile 以与 build-release.sh 完全相同的参数编译一个二进制。
compile() {
  local out="$1" goos="$2" goarch="$3" service="$4"
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o "$out" "./service/${service}"
}

# take copies NAME from PREBUILT after checking it against PREBUILT's
# SHA256SUMS.txt. The mode is set explicitly: a CI artifact download drops it.
# take 从 PREBUILT 拷贝 NAME，先对照其 SHA256SUMS.txt 校验。权限显式设置：CI 制品下载会丢掉它。
take() {
  local name="$1" out="$2" mode="$3" line
  line="$(awk -v n="$name" '$2 == n || $2 == "*" n' "$PREBUILT/SHA256SUMS.txt")"
  [[ -n "$line" ]] || die "${PREBUILT}/SHA256SUMS.txt 没有 ${name}：${PREBUILT} 不是 ${VERSION} 的 build-release.sh 产物，或平台超出了它的范围"
  (cd "$PREBUILT" && printf '%s\n' "$line" | sha256 -c - >/dev/null) || die "${PREBUILT}/${name} 与 SHA256SUMS.txt 不符"
  cp "$PREBUILT/$name" "$out"
  chmod "$mode" "$out"
}

# agent_dist lays out the directory the Gateway serves under
# /agent/download/<VERSION>/: the Agent for AGENT_PLATFORMS, the four install
# scripts and a SHA256SUMS.txt of just those.
# agent_dist 摆出 Gateway 在 /agent/download/<VERSION>/ 下提供的目录：AGENT_PLATFORMS 的 Agent、
# 四个安装脚本，以及只覆盖这些文件的 SHA256SUMS.txt。
agent_dist() {
  local dir="$1" platform goos goarch ext name script
  mkdir -p "$dir"
  for platform in $AGENT_PLATFORMS; do
    goos="${platform%/*}" goarch="${platform#*/}" ext=""
    [[ "$goos" != windows ]] || ext=.exe
    name="aiserveweave-agent_${VERSION}_${goos}_${goarch}${ext}"
    if [[ -n "$PREBUILT" ]]; then
      take "$name" "$dir/$name" 0755
    else
      echo "  aiserveweave-agent ${platform}（内置于 Gateway）"
      compile "$dir/$name" "$goos" "$goarch" aiServeWeaveAgent
    fi
  done
  for script in agent.sh agent.ps1 install.sh install.ps1; do
    if [[ -n "$PREBUILT" ]]; then take "$script" "$dir/$script" 0644; else cp "deploy/agent/$script" "$dir/$script"; fi
  done
  (cd "$dir" && sha256 -- * > SHA256SUMS.txt)
}

echo "==> 编译镜像用的 Go 服务（版本 ${VERSION}，平台 ${PLATFORMS}${PREBUILT:+，取自 ${PREBUILT}}）"
for service in "$@"; do
  [[ -f "service/${service}/main.go" ]] || die "未知服务 ${service}（可选: aiServeWeaveRegistry aiServeWeaveControlPlane aiServeWeaveGateway）"
  bin="aiserveweave-$(echo "${service#aiServeWeave}" | tr '[:upper:]' '[:lower:]')"
  dir="${OUT_DIR}/${service}"
  # A stale tree would otherwise keep a platform or Agent this run no longer builds.
  # 否则旧目录会留下本次不再构建的平台或 Agent。
  rm -rf "$dir"
  mkdir -p "$dir/agent-dist"
  for platform in $PLATFORMS; do
    goos="${platform%/*}" goarch="${platform#*/}"
    [[ "$goos" == linux ]] || die "镜像平台只能是 linux/*（收到 ${platform}）"
    mkdir -p "$dir/${goos}_${goarch}"
    if [[ -n "$PREBUILT" ]]; then
      take "${bin}_${VERSION}_${goos}_${goarch}" "$dir/${goos}_${goarch}/service" 0755
    else
      echo "  ${bin} ${platform}"
      compile "$dir/${goos}_${goarch}/service" "$goos" "$goarch" "$service"
    fi
  done
  [[ "$service" != aiServeWeaveGateway ]] || agent_dist "$dir/agent-dist/${VERSION}"
done

#!/usr/bin/env bash
# build-release.sh cross-compiles the four Go binaries for every supported
# platform and writes a checksum file next to them, for a GitHub Release or a
# local dry run.
#
# build-release.sh 为四个 Go 二进制交叉编译出每个受支持平台的产物，并在旁边生成一份
# 校验和文件，供 GitHub Release 或本地试跑使用。
#
# Usage:
#   VERSION=v0.1.0 scripts/build-release.sh
#   VERSION=v0.1.0 PLATFORMS="linux/amd64" scripts/build-release.sh   # smoke test, one platform
#
# VERSION defaults to a git describe so a local run without a tag still
# produces something identifiable. PLATFORMS defaults to the full supported
# matrix; override it to build a subset quickly.
#
# VERSION 默认取自 git describe，这样本地没打 tag 跑一次也能得到可辨识的产物。
# PLATFORMS 默认是完整的受支持矩阵；覆盖它可以快速只构建一个子集。
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
PLATFORMS="${PLATFORMS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64}"
SERVICES="aiServeWeaveAgent aiServeWeaveGateway aiServeWeaveControlPlane aiServeWeaveRegistry"
OUT_DIR="dist"

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"

echo "building version ${VERSION} for: ${PLATFORMS}"

for service in $SERVICES; do
  # Lowercase the CamelCase service directory name into the binary name this
  # repository already uses elsewhere (aiserveweave-agent, ...).
  #
  # 把驼峰式的服务目录名转成仓库其他地方已经在用的二进制名（aiserveweave-agent 等）。
  bin_suffix="$(echo "$service" | sed 's/^aiServeWeave//' | tr '[:upper:]' '[:lower:]')"
  bin_name="aiserveweave-${bin_suffix}"

  for platform in $PLATFORMS; do
    goos="${platform%/*}"
    goarch="${platform#*/}"
    out="${OUT_DIR}/${bin_name}_${VERSION}_${goos}_${goarch}"
    echo "  ${bin_name} ${goos}/${goarch}"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o "$out" "./service/${service}"
  done
done

(
  cd "$OUT_DIR"
  # sha256sum is what CI (ubuntu-latest) has; shasum -a 256 is the macOS
  # fallback for a local dry run. Both produce the same `sha256sum -c`
  # compatible line format.
  #
  # sha256sum 是 CI（ubuntu-latest）自带的；shasum -a 256 是本地在 macOS 上试跑的
  # 备选。两者产出的行格式对 `sha256sum -c` 都兼容。
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -- * > SHA256SUMS.txt
  else
    shasum -a 256 -- * > SHA256SUMS.txt
  fi
)

echo "done: ${OUT_DIR}/ ($(ls "$OUT_DIR" | wc -l | tr -d ' ') files)"

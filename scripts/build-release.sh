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
PLATFORMS="${PLATFORMS:-linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64}"
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
    if [ "$goos" = windows ] && [ "$service" != aiServeWeaveAgent ];then continue;fi
    extension=""
    if [ "$goos" = windows ];then extension=.exe;fi
    out="${OUT_DIR}/${bin_name}_${VERSION}_${goos}_${goarch}${extension}"
    echo "  ${bin_name} ${goos}/${goarch}"
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
      -o "$out" "./service/${service}"
  done
done

# Bundle maintenance scripts while retaining raw binary names for existing consumers.
# 打包维护脚本，同时保留现有使用方依赖的原始二进制命名。
package_dir=$(mktemp -d)
trap 'rm -rf "$package_dir"' EXIT
for platform in $PLATFORMS;do
  goos="${platform%/*}";goarch="${platform#*/}"
  stem="aiserveweave-agent_${VERSION}_${goos}_${goarch}"
  extension="";if [ "$goos" = windows ];then extension=.exe;fi
  [ -f "$OUT_DIR/$stem$extension" ] || continue
  cp "$OUT_DIR/$stem$extension" "$package_dir/"
  if command -v sha256sum >/dev/null 2>&1;then
    (cd "$package_dir";sha256sum "$stem$extension" >SHA256SUMS.txt)
  else (cd "$package_dir";shasum -a 256 "$stem$extension" >SHA256SUMS.txt);fi
  cp deploy/agent/README.md "$package_dir/README.md"
  cp deploy/agent/acceptance.md "$package_dir/acceptance.md"
  sed 's@\[Agent 配置\](../../service/aiServeWeaveAgent/README.md)@仓库中的 service/aiServeWeaveAgent/README.md@' "$package_dir/README.md" >"$package_dir/README.next"
  mv "$package_dir/README.next" "$package_dir/README.md"
  if [ "$goos" = windows ];then
    cp deploy/agent/agent.ps1 "$package_dir/agent.ps1"
    (cd "$package_dir";zip -q "$OLDPWD/$OUT_DIR/${stem}.zip" "$stem$extension" SHA256SUMS.txt README.md acceptance.md agent.ps1)
  else
    cp deploy/agent/agent.sh "$package_dir/agent.sh"
    tar -czf "$OUT_DIR/${stem}.tar.gz" -C "$package_dir" "$stem" SHA256SUMS.txt README.md acceptance.md agent.sh
  fi
  rm -f "$package_dir"/*
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

#!/usr/bin/env bash
# fetch-comfyui-frontend.sh downloads the pinned ComfyUI_frontend release the
# control plane's workflow editor serves (ComfyEditor.DistDir), checks its
# SHA-256, unpacks it and removes source maps.
#
# fetch-comfyui-frontend.sh 下载控制面工作流编辑器所提供的固定版本
# ComfyUI_frontend 发布包（ComfyEditor.DistDir），校验其 SHA-256，解压并删除
# source map。
#
# Usage / 用法:
#   scripts/fetch-comfyui-frontend.sh /opt/aiserveweave/comfy-editor
#
# The release is GPL-3.0 and is not vendored into this Apache-2.0 repository;
# it is fetched at deploy time and served unmodified. The version must match
# the frontend_version in
# service/aiServeWeaveControlPlane/internal/comfyeditor/catalog/meta.json:
# bump both together, and regenerate the catalogue from that ComfyUI release.
#
# 该发布包是 GPL-3.0，不纳入本 Apache-2.0 仓库；它在部署时获取，原样提供。版本必须
# 与 service/aiServeWeaveControlPlane/internal/comfyeditor/catalog/meta.json 中的
# frontend_version 一致：两者一起升级，并用对应 ComfyUI 版本重新生成节点目录。
set -euo pipefail

VERSION="1.55.17"
SHA256="bbb7d605956df19ac232a276055b26fbca8f39e7e6e81568340ef64d147888d5"
URL="https://github.com/Comfy-Org/ComfyUI_frontend/releases/download/v${VERSION}/dist.zip"

DEST="${1:?usage: $0 <destination directory>}"
if [ -e "$DEST" ] && [ -n "$(ls -A "$DEST" 2>/dev/null)" ]; then
  echo "refusing to overwrite non-empty $DEST" >&2
  exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

curl -fsSL --retry 3 -o "$work/dist.zip" "$URL"
echo "${SHA256}  $work/dist.zip" | shasum -a 256 -c - >/dev/null
mkdir -p "$work/out"
unzip -q "$work/dist.zip" -d "$work/out"
# Source maps are never served (the stub refuses *.map); dropping them keeps
# the directory small and the refusal moot.
# source map 从不提供（桩后端拒绝 *.map）；删除它们让目录更小，那条拒绝也就无需触发。
find "$work/out" -name '*.map' -type f -delete
if [ ! -f "$work/out/index.html" ]; then
  echo "dist.zip did not contain index.html at its root" >&2
  exit 1
fi
mkdir -p "$DEST"
cp -R "$work/out/." "$DEST/"
echo "ComfyUI_frontend ${VERSION} unpacked to ${DEST}"

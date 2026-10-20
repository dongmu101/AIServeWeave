#!/usr/bin/env bash
# fetch-comfyui-frontend.sh downloads the pinned ComfyUI_frontend release the
# control plane's workflow editor serves (ComfyEditor.DistDir). --localized
# builds the pinned source with audited localization patches and a manifest.
#
# fetch-comfyui-frontend.sh 下载控制面工作流编辑器所提供的固定版本
# ComfyUI_frontend 资源（ComfyEditor.DistDir）。--localized 从固定源码应用
# 经审查的本地化补丁构建并生成制品清单。
#
# Usage / 用法:
#   scripts/fetch-comfyui-frontend.sh --localized /opt/aiserveweave/comfy-editor
#   scripts/fetch-comfyui-frontend.sh /tmp/original-release
#
# The release is GPL-3.0 and is not vendored into this Apache-2.0 repository;
# it is fetched at deploy time. The version must match
# the frontend_version in
# service/aiServeWeaveControlPlane/internal/comfyeditor/catalog/meta.json:
# bump both together, and regenerate the catalogue from that ComfyUI release.
#
# 该发布包是 GPL-3.0，不纳入本 Apache-2.0 仓库；它在部署时获取。版本必须
# 与 service/aiServeWeaveControlPlane/internal/comfyeditor/catalog/meta.json 中的
# frontend_version 一致：两者一起升级，并用对应 ComfyUI 版本重新生成节点目录。
set -euo pipefail

VERSION="1.55.17"
SHA256="bbb7d605956df19ac232a276055b26fbca8f39e7e6e81568340ef64d147888d5"
URL="https://github.com/Comfy-Org/ComfyUI_frontend/releases/download/v${VERSION}/dist.zip"

localized=false
if [ "${1:-}" = "--localized" ]; then localized=true; shift; fi
DEST="${1:?usage: $0 [--localized] <destination directory>}"
if [ -e "$DEST" ] && [ -n "$(ls -A "$DEST" 2>/dev/null)" ]; then
  echo "refusing to overwrite non-empty $DEST" >&2
  exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

if [ "$localized" = true ]; then
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  config="$script_dir/comfyui-localization/manifest.json"
  config_value() {
    node -e 'const c=JSON.parse(require("node:fs").readFileSync(process.argv[1])); console.log(process.argv[2].split(".").reduce((v,k)=>v[k],c))' "$config" "$1"
  }
  expected_node="$(config_value toolchain.node)"
  expected_pnpm="$(config_value toolchain.pnpm)"
  if [ "$(node --version)" != "$expected_node" ] || [ "$(pnpm --version)" != "$expected_pnpm" ]; then
    echo "localized build requires Node $expected_node and pnpm $expected_pnpm from manifest.json" >&2
    exit 1
  fi
  if [ -n "${AISW_COMFY_SOURCE_ARCHIVE:-}" ]; then
    cp "$AISW_COMFY_SOURCE_ARCHIVE" "$work/source.tar.gz"
  else
    curl -fsSL --retry 3 -o "$work/source.tar.gz" "$(config_value source.archive_url)"
  fi
  echo "$(config_value source.archive_sha256)  $work/source.tar.gz" | shasum -a 256 -c - >/dev/null
  mkdir "$work/source"
  tar -xzf "$work/source.tar.gz" --strip-components=1 -C "$work/source"
  node "$script_dir/comfyui-localization/apply.mjs" apply "$work/source"
  (
    cd "$work/source"
    if [ -n "${AISW_COMFY_PNPM_STORE:-}" ]; then
      pnpm install --frozen-lockfile --ignore-scripts --store-dir "$AISW_COMFY_PNPM_STORE"
    else
      pnpm install --frozen-lockfile --ignore-scripts
    fi
    DISTRIBUTION=localhost SENTRY_AUTH_TOKEN= pnpm exec vite build --config vite.config.mts
  )
  find "$work/source/dist" -name '*.map' -type f -delete
  node "$script_dir/comfyui-localization/apply.mjs" manifest "$work/source/dist"
  mkdir -p "$DEST"
  cp -R "$work/source/dist/." "$DEST/"
  echo "ComfyUI_frontend ${VERSION} localized assets built to ${DEST}"
  exit 0
fi

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

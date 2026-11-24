#!/usr/bin/env bash
# build-web.sh builds a Next.js frontend (the Console or Studio) on the host,
# leaving the standalone output its Dockerfile copies into the image: the image
# build only copies files and never runs pnpm. The output is plain JavaScript,
# so one build serves every image platform; the script fails if a native
# binary (*.node) slipped into it, since that would be the host's platform.
#
# build-web.sh 在宿主机上构建一个 Next.js 前端（Console 或 Studio），留下其 Dockerfile 拷进
# 镜像的 standalone 产物：镜像构建只拷贝文件，不运行 pnpm。产物是纯 JavaScript，一次构建
# 适用于所有镜像平台；若混入原生二进制（*.node）则失败，因为那属于宿主机平台。
#
# Usage / 用法:
#   scripts/build-web.sh service/aiServeWeaveConsole [service/aiServeWeaveStudio ...]
set -euo pipefail

(( $# > 0 )) || { echo "用法: $0 <前端目录>..." >&2; exit 2; }
command -v pnpm >/dev/null 2>&1 || {
  echo "需要 Node.js 26 与 pnpm 来构建前端（npm install --global pnpm，或 corepack enable）" >&2; exit 1; }

for dir in "$@"; do
  [[ -f "${dir}/package.json" && -f "${dir}/Dockerfile" ]] || { echo "${dir} 不是前端服务目录" >&2; exit 1; }
  echo "==> 构建前端 ${dir}"
  # A stale standalone tree would otherwise keep files the new build no longer traces.
  # 否则旧的 standalone 目录会留下新构建已不再追踪的文件。
  rm -rf "${dir}/.next/standalone"
  (cd "${dir}" && pnpm install --frozen-lockfile && NEXT_TELEMETRY_DISABLED=1 pnpm build)
  [[ -f "${dir}/.next/standalone/server.js" && -d "${dir}/.next/static" ]] || {
    echo "${dir} 构建后缺少 .next/standalone/server.js 或 .next/static（next.config 需 output: \"standalone\"）" >&2; exit 1; }
  native="$(find "${dir}/.next/standalone" -name '*.node' -print -quit)"
  [[ -z "${native}" ]] || {
    echo "${dir} 的 standalone 产物含原生二进制 ${native}，它只适用于本机平台，不能拷进镜像；在 next.config 的 outputFileTracingExcludes 中排除该依赖" >&2; exit 1; }
done

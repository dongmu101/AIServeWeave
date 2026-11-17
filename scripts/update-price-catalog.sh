#!/usr/bin/env bash
# update-price-catalog.sh refreshes the control plane's built-in price catalog
# snapshot (service/aiServeWeaveControlPlane/internal/pricecatalog/snapshot.json)
# from the upstream LiteLLM price list, trimmed by the same Go parser the
# control plane uses. Review the diff before committing it.
#
# update-price-catalog.sh 从上游 LiteLLM 价目表更新控制面内置的价目表快照
# （service/aiServeWeaveControlPlane/internal/pricecatalog/snapshot.json），裁剪使用与
# 控制面相同的 Go 解析器。提交前请检查差异。
#
# Usage:
#   scripts/update-price-catalog.sh
#   PRICE_CATALOG_URL=https://api.github.com/repos/BerriAI/litellm/contents/model_prices_and_context_window.json scripts/update-price-catalog.sh
#   PRICE_CATALOG_FILE=/path/to/model_prices_and_context_window.json scripts/update-price-catalog.sh
#
# PRICE_CATALOG_URL defaults to GitHub raw. Where GitHub raw is unreachable
# (often in mainland China), use the GitHub API address above: the Accept
# header below makes it return the file itself (unauthenticated, 60 requests
# an hour). PRICE_CATALOG_FILE skips the download.
#
# PRICE_CATALOG_URL 默认是 GitHub raw。连不上 GitHub raw 的地方（中国大陆常见）改用上面的
# GitHub API 地址：下面的 Accept 头会让它直接返回文件本身（未认证时每小时 60 次）。设置
# PRICE_CATALOG_FILE 时不下载。
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
url="${PRICE_CATALOG_URL:-https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json}"
out="$root/service/aiServeWeaveControlPlane/internal/pricecatalog/snapshot.json"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

src="${PRICE_CATALOG_FILE:-}"
if [[ -z "$src" ]]; then
  src="$work/upstream.json"
  curl -fsSL --max-time 120 -H 'Accept: application/vnd.github.raw' -o "$src" "$url"
fi

cd "$root"
go run ./service/aiServeWeaveControlPlane/internal/pricecatalog/cmd/pricecatalog-snapshot -in "$src" -out "$out"
echo "wrote $out"

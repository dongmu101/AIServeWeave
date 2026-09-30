#!/usr/bin/env bash
# codex-model-catalog.sh writes a Codex CLI model catalog for a model served by
# AIServeWeave, so Codex stops running it on generic fallback metadata.
#
# codex-model-catalog.sh 为一个由 AIServeWeave 提供服务的模型生成 Codex CLI 的模型目录，
# 使 Codex 不再拿通用的兜底元数据来运行它。
#
# Why this exists: Codex looks a model up by name in its own catalog. A name it
# has never heard of (qwen3-coder, say) gets fallback metadata, prints "Model
# metadata for `...` not found", and assumes a context window that is not the
# model's — so auto-compaction fires at the wrong point. Codex does not ask the
# provider's /v1/models for this, so the fix has to live on the machine that
# runs Codex: a catalog file named by `model_catalog_json` in config.toml.
#
# 为什么需要它：Codex 按名字在自己的目录里查模型。它从没听过的名字（比如 qwen3-coder）会拿到
# 兜底元数据、打印 "Model metadata for `...` not found"，并假定一个并不是该模型真实的上下文
# 窗口——于是自动压缩在错误的时点触发。Codex 不会为此去问 provider 的 /v1/models，因此修复
# 只能放在运行 Codex 的那台机器上：由 config.toml 里的 `model_catalog_json` 指向一个目录文件。
#
# The catalog is derived from the installed Codex's own bundled catalog
# (`codex debug models --bundled`) rather than written out here, because its
# schema changes between Codex releases and a hand-written copy would rot. The
# entry is stripped of everything specific to OpenAI's hosted models: reasoning
# levels, service tiers, the upgrade prompt, verbosity, hosted web search and
# image input.
#
# 目录取自已安装 Codex 自带的目录（`codex debug models --bundled`），而不是在这里手写，
# 因为它的 schema 随 Codex 版本变化，手写的副本会过时。条目里所有专属于 OpenAI 托管模型的
# 东西都被去掉：推理档位、服务档位、升级提示、verbosity、托管的 web search 与图片输入。
#
# Usage:
#   scripts/codex-model-catalog.sh MODEL [CONTEXT_WINDOW] > ~/.codex/aiserveweave-models.json
#
# then, in ~/.codex/config.toml:
#   model = "MODEL"
#   model_catalog_json = "/Users/you/.codex/aiserveweave-models.json"
#
# MODEL is the alias the Gateway routes (the name in GET /v1/models).
# CONTEXT_WINDOW is the model's real window in tokens, default 128000; set it to
# what the backend actually serves — Codex compacts relative to it.
# CODEX_BIN overrides the codex executable.
#
# MODEL 是 Gateway 路由的别名（GET /v1/models 里的名字）。
# CONTEXT_WINDOW 是模型真实的窗口大小（token 数），默认 128000；请设成后端实际提供的值——
# Codex 是相对于它来压缩的。CODEX_BIN 可覆盖 codex 可执行文件。
set -euo pipefail

if [[ $# -lt 1 || $# -gt 2 ]]; then
	echo "usage: $0 MODEL [CONTEXT_WINDOW]" >&2
	exit 2
fi
model=$1
window=${2:-128000}
codex=${CODEX_BIN:-codex}

if ! [[ $window =~ ^[0-9]+$ ]] || [[ $window -le 0 ]]; then
	echo "$0: CONTEXT_WINDOW must be a positive integer, got '$window'" >&2
	exit 2
fi
for tool in "$codex" jq; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "$0: '$tool' is required but was not found" >&2
		exit 1
	fi
done

# The base entry only supplies the fields Codex requires and the prompt it was
# tuned with; gpt-5.5 is preferred, and any listed model will do if a future
# Codex drops it.
#
# 基础条目只提供 Codex 必需的字段，以及它所调校的提示词；优先用 gpt-5.5，将来的 Codex
# 若去掉它，任何一个列出的模型都行。
"$codex" debug models --bundled | jq \
	--arg slug "$model" --argjson window "$window" '
	(.models | (map(select(.slug == "gpt-5.5")) + map(select(.visibility == "list")))[0]) as $base
	| if $base == null then error("the bundled catalog has no usable base model") else . end
	| {models: [
		$base
		| .slug = $slug
		| .display_name = $slug
		| .description = "Served by AIServeWeave"
		| .supported_reasoning_levels = []
		| .default_reasoning_level = null
		| .support_verbosity = false
		| .default_verbosity = null
		| .supports_search_tool = false
		| .web_search_tool_type = "text"
		| .input_modalities = ["text"]
		| .context_window = $window
		| .max_context_window = $window
		| del(.upgrade, .service_tiers, .additional_speed_tiers, .comp_hash, .model_messages)
	]}'

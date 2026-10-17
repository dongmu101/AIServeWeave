package hosted

import "strings"

// The non-chat filters drop catalogue families a provider documents as
// image, audio, embedding or moderation models, so chat routing never offers
// them. They follow dbx's provider filters (last checked against official
// catalogues on 2026-07-27); an ID that matches no rule is kept.
//
// 非聊天过滤器剔除供应商文档中标明为图像、音频、嵌入或审核用途的模型族，
// 使聊天路由永远不会提供它们。规则沿用 dbx 的供应商过滤器（最近一次对照官方
// 目录为 2026-07-27）；不匹配任何规则的 ID 予以保留。

func matches(id string, prefixes, contains []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	for _, c := range contains {
		if strings.Contains(id, c) {
			return true
		}
	}
	return false
}

func openAINonChat(id string) bool {
	return matches(id,
		[]string{"babbage-002", "chatgpt-image", "computer-use", "dall-e", "davinci-002", "gpt-image", "omni-moderation", "sora",
			"text-embedding", "text-moderation", "tts", "whisper"},
		// "-pro", "codex" and "deep-research" models are Responses-only and
		// fail on chat/completions.
		// "-pro"、"codex" 与 "deep-research" 模型只支持 Responses 接口，调用
		// chat/completions 会失败。
		[]string{"-audio", "-realtime", "-transcribe", "-tts", "-pro", "codex", "deep-research"})
}

func geminiNonChat(id string) bool {
	return matches(id,
		[]string{"antigravity", "chirp", "imagen", "lyria", "nano-banana", "veo"},
		[]string{"computer-use", "deep-research", "embedding", "-image", "gemini-omni", "image-generation", "-live", "live-",
			"native-audio", "robotics", "-tts"})
}

func qwenNonChat(id string) bool {
	return matches(id,
		[]string{"ccai-", "cosyvoice", "flux-", "fun-asr", "paraformer", "qwen-image", "qwen-mt-", "qwen-tts", "qwen3-tts", "sambert",
			"sensevoice", "speech-", "stable-diffusion", "tongyi-tingwu", "wan", "wanx", "z-image"},
		[]string{"-asr", "-captioner", "embedding", "livetranslate", "moderation", "-ocr", "realtime", "rerank", "-s2s"})
}

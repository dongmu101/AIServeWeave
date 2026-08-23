package vllm

import (
	"AIServeWeave/common/runtime"
	"AIServeWeave/common/runtime/internal/capprofile"
)

// runtimeProfile is the conservative, version-keyed capability table for
// vLLM's OpenAI-compatible server. Floors are set at the release from which
// the capability is documented on the OpenAI-Compatible Server page, not at
// the release that first experimented with it: too high only costs an older
// server an "unknown", which CapabilityOverrides can recover, while too low
// reports support that may not be there.
//
// Deliberately absent, and why:
//
//   - embeddings: vLLM only serves /v1/embeddings when the loaded model is
//     a pooling/embedding model. The endpoint existing says nothing about
//     the model behind it, so embeddings stays unknown and an operator must
//     declare it per instance — this is what "only for explicitly supported
//     models" means for a backend that serves one model per process.
//   - responses, vision, reasoning, parallel_tool_calls: all depend on the
//     served model or on flags this adapter never inspects.
var runtimeProfile = capprofile.Table{
	{
		MinVersion: "0.4.0",
		Detail:     "vLLM OpenAI-Compatible Server chat/completions (docs.vllm.ai serving/online_serving/openai_compatible_server)",
		Caps: map[runtime.Capability]runtime.SupportLevel{
			runtime.CapabilityChat:        runtime.SupportSupported,
			runtime.CapabilityChatStream:  runtime.SupportSupported,
			runtime.CapabilityCompletions: runtime.SupportSupported,
		},
	},
	{
		MinVersion: "0.6.0",
		Detail:     "vLLM named tool calling and guided decoding via response_format (docs.vllm.ai serving/online_serving/openai_compatible_server)",
		Caps: map[runtime.Capability]runtime.SupportLevel{
			runtime.CapabilityTools:            runtime.SupportSupported,
			runtime.CapabilityStructuredOutput: runtime.SupportSupported,
		},
	},
}

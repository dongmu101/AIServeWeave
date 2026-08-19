package ollama

import (
	"AIServeWeave/service/aiServeWeaveAgent/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/runtime/internal/capprofile"
)

// runtimeProfile is the conservative, version-keyed capability table for
// the Ollama OpenAI-compatible surface. Version floors are deliberately
// higher than the release that first shipped each capability: only from
// these versions on is the combination documented on the
// OpenAI-compatibility page this adapter targets. A floor that is too high
// costs an older server an "unknown", which CapabilityOverrides can
// recover; a floor that is too low would report support that is not there.
//
// Deliberately absent: vision, reasoning and parallel_tool_calls. The first
// two vary per model and are reported by /api/tags and /api/show as model
// metadata; the third has no documented answer for Ollama's
// OpenAI-compatible layer, and guessing it here would turn "we never
// checked" into "the backend supports it".
var runtimeProfile = capprofile.Table{
	{
		MinVersion: "0.1.24",
		Detail:     "Ollama OpenAI-compatible chat completions (docs.ollama.com/api/openai-compatibility)",
		Caps: map[runtime.Capability]runtime.SupportLevel{
			runtime.CapabilityChat:        runtime.SupportSupported,
			runtime.CapabilityChatStream:  runtime.SupportSupported,
			runtime.CapabilityCompletions: runtime.SupportSupported,
		},
	},
	{
		MinVersion: "0.5.0",
		Detail:     "Ollama OpenAI-compatible tools, embeddings and response_format (docs.ollama.com/api/openai-compatibility)",
		Caps: map[runtime.Capability]runtime.SupportLevel{
			runtime.CapabilityTools:            runtime.SupportSupported,
			runtime.CapabilityEmbeddings:       runtime.SupportSupported,
			runtime.CapabilityStructuredOutput: runtime.SupportSupported,
		},
	},
}

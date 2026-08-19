package sglang

import (
	"AIServeWeave/service/aiServeWeaveAgent/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/runtime/internal/capprofile"
)

// runtimeProfile is the conservative, version-keyed capability table for
// SGLang's OpenAI-compatible server.
//
// It is expected to apply rarely: SGLang has no documented version
// endpoint, so unless GET /get_server_info happens to report one, every
// entry here stays inapplicable and the capabilities below remain unknown.
// That is the intended failure mode — the core chat capabilities come from
// endpoint evidence instead (see endpointCapabilities), and everything
// listed here is exactly the set that must not be assumed without knowing
// which release is running.
//
// Deliberately absent, and why:
//
//   - embeddings: SGLang serves /v1/embeddings only for embedding models,
//     and no endpoint reports which model class is loaded. Per the backend
//     matrix this capability is enabled only once an adapter contract test
//     proves it, so until then an operator must declare it per instance.
//   - vision, reasoning, parallel_tool_calls: all depend on the served
//     model or on launch flags this adapter never inspects.
var runtimeProfile = capprofile.Table{
	{
		MinVersion: "0.4.0",
		Detail:     "SGLang OpenAI-compatible function calling and structured output (docs.sglang.ai)",
		Caps: map[runtime.Capability]runtime.SupportLevel{
			runtime.CapabilityTools:            runtime.SupportSupported,
			runtime.CapabilityStructuredOutput: runtime.SupportSupported,
		},
	},
}

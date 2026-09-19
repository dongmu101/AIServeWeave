package runtime

import "fmt"

// Capability names a single, independently verifiable feature a Runtime or
// Model may support. Adapters must use these constants rather than string
// literals so the capability vocabulary stays closed.
type Capability string

const (
	CapabilityChat              Capability = "chat"
	CapabilityChatStream        Capability = "chat_stream"
	CapabilityCompletions       Capability = "completions"
	CapabilityEmbeddings        Capability = "embeddings"
	CapabilityResponses         Capability = "responses"
	CapabilityVision            Capability = "vision"
	CapabilityTools             Capability = "tools"
	CapabilityParallelToolCalls Capability = "parallel_tool_calls"
	CapabilityStructuredOutput  Capability = "structured_output"
	CapabilityReasoning         Capability = "reasoning"
	CapabilityWorkflowExecution Capability = "workflow_execution"
	CapabilityWorkflowEvents    Capability = "workflow_events"
	CapabilityWorkflowCancel    Capability = "workflow_cancel"
	CapabilityArtifactRead      Capability = "artifact_read"
	CapabilityInputWrite        Capability = "input_write"
	// CapabilityAudioTranscription gates InferenceRuntime.Transcribe for both
	// AudioTaskTranscribe and AudioTaskTranslate (STATUS.md's P2). It is a
	// deliberately single capability for both tasks: their wire and backend
	// contracts differ only in the request's Task field, not in what the
	// caller must prove is supported.
	//
	// CapabilityAudioTranscription 同时为 AudioTaskTranscribe 与
	// AudioTaskTranslate 两种任务门禁 InferenceRuntime.Transcribe（STATUS.md
	// 的 P2）。两个任务刻意共用一个能力：它们的 wire 与后端契约只在请求的 Task
	// 字段上不同，调用方需要证明支持的东西并无不同。
	CapabilityAudioTranscription Capability = "audio_transcription"
	// CapabilityRerank gates InferenceRuntime.Rerank (STATUS.md's P2). It is
	// the pilot for the "new InferenceRuntime method" breaking-change pattern
	// the P2 API compat design doc recommended validating before audio
	// transcription reused it: new capability constant, new request/response
	// types, a new interface method, and a shared oaibase default that keeps
	// every existing adapter compiling.
	//
	// CapabilityRerank 为 InferenceRuntime.Rerank 门禁（STATUS.md 的 P2）。它是
	// P2 API 兼容边界设计文档建议先行验证的「新增 InferenceRuntime 方法」这套
	// 破坏性变更模式的试点，音频转录随后复用了同一套模式：新增能力常量、新增
	// 请求/响应类型、接口新增方法，以及一个让全部既有适配器保持可编译的
	// oaibase 共享默认实现。
	CapabilityRerank Capability = "rerank"
	// CapabilityDocumentInput gates a chat request whose message content
	// includes a "file" content part (STATUS.md's P2 multimodal input: an
	// inline document such as a PDF, mirroring Anthropic's "document"
	// content block and the OpenAI Responses "input_file" part). No adapter
	// in this repository publishes it yet — see CapabilityRerank's note on
	// shipping a capability's protocol plumbing ahead of any backend that
	// grants it.
	//
	// CapabilityDocumentInput 为一次聊天请求内容中含有 "file" 内容片段的情形
	// 门禁（STATUS.md 的 P2 多模态输入：内联附带一份 PDF 等文档，对应
	// Anthropic 的 "document" 内容块与 OpenAI Responses 的 "input_file"
	// 部件）。本仓库目前没有任何适配器发布这项能力——协议先于任何授予它的
	// 后端交付的先例见 CapabilityRerank 的说明。
	CapabilityDocumentInput Capability = "document_input"
	// CapabilityAudioInput gates a chat request whose message content
	// includes an "input_audio" content part (STATUS.md's P2 multimodal
	// input, mirroring OpenAI Chat Completions' inline audio input for an
	// audio-capable model). It is distinct from CapabilityAudioTranscription,
	// which gates the separate /v1/audio/transcriptions endpoint rather than
	// inline chat content; no adapter in this repository publishes either
	// yet.
	//
	// CapabilityAudioInput 为一次聊天请求内容中含有 "input_audio" 内容片段的
	// 情形门禁（STATUS.md 的 P2 多模态输入，对应 OpenAI Chat Completions
	// 面向具备音频能力模型的内联音频输入）。它与 CapabilityAudioTranscription
	// 不同——后者为独立的 /v1/audio/transcriptions 端点门禁，而不是内联聊天
	// 内容；本仓库目前对这两项能力都没有任何适配器发布。
	CapabilityAudioInput Capability = "audio_input"
)

// CapabilitySource records how a piece of capability evidence was obtained,
// used to rank conflicting evidence for the same Capability.
type CapabilitySource string

const (
	SourceEndpoint       CapabilitySource = "endpoint"
	SourceModelMetadata  CapabilitySource = "model_metadata"
	SourceRuntimeProfile CapabilitySource = "runtime_profile"
	SourceConfigOverride CapabilitySource = "config_override"
)

// sourcePriority ranks CapabilitySource from most to least trusted:
// config_override > endpoint > model_metadata > runtime_profile.
var sourcePriority = map[CapabilitySource]int{
	SourceConfigOverride: 4,
	SourceEndpoint:       3,
	SourceModelMetadata:  2,
	SourceRuntimeProfile: 1,
}

type SupportLevel string

const (
	SupportUnknown     SupportLevel = "unknown"
	SupportSupported   SupportLevel = "supported"
	SupportUnsupported SupportLevel = "unsupported"
)

// levelRank orders SupportLevel for conservative same-source conflict
// resolution: unsupported beats supported beats unknown.
func levelRank(l SupportLevel) int {
	switch l {
	case SupportUnsupported:
		return 2
	case SupportSupported:
		return 1
	default:
		return 0
	}
}

type CapabilityEvidence struct {
	Capability Capability
	Level      SupportLevel
	Source     CapabilitySource
	Detail     string
}

// CapabilitySet is the full, evidence-backed capability picture for a
// runtime instance or a single model. A missing entry means unknown, not
// unsupported — always read through Resolve rather than a map index.
type CapabilitySet map[Capability]CapabilityEvidence

// Resolve returns the final evidence for c. A capability absent from s is
// reported as SupportUnknown rather than zero-valued, so callers never
// mistake "no evidence" for "explicitly unsupported".
func (s CapabilitySet) Resolve(c Capability) CapabilityEvidence {
	if ev, ok := s[c]; ok {
		return ev
	}
	return CapabilityEvidence{Capability: c, Level: SupportUnknown}
}

// Require is the call-time gate: it returns nil when c is supported, and
// otherwise a *RuntimeError with Code ErrorCapability wrapping
// ErrCapabilityUnsupported or ErrCapabilityUnknown depending on which the
// evidence indicates.
func (s CapabilitySet) Require(c Capability) error {
	ev := s.Resolve(c)
	switch ev.Level {
	case SupportSupported:
		return nil
	case SupportUnsupported:
		return &RuntimeError{
			Code:      ErrorCapability,
			Operation: "capability_check",
			Message:   fmt.Sprintf("capability %q is not supported", c),
			Cause:     ErrCapabilityUnsupported,
		}
	default:
		return &RuntimeError{
			Code:      ErrorCapability,
			Operation: "capability_check",
			Message:   fmt.Sprintf("capability %q support is unknown", c),
			Cause:     ErrCapabilityUnknown,
		}
	}
}

// Merge combines capability sets from possibly many sources into one,
// keeping for each Capability the evidence from the highest-priority
// CapabilitySource. When two inputs disagree at the same priority, the
// result converges conservatively (unsupported > supported > unknown) and
// the winning evidence's Detail is annotated with the conflict so callers
// can surface it (e.g. into Discovery.Warnings). Inputs are never mutated.
func Merge(sets ...CapabilitySet) CapabilitySet {
	merged := make(CapabilitySet)
	rank := make(map[Capability]int, len(merged))

	for _, set := range sets {
		for cap, ev := range set {
			cur, exists := merged[cap]
			if !exists {
				merged[cap] = ev
				rank[cap] = sourcePriority[ev.Source]
				continue
			}
			curRank := rank[cap]
			evRank := sourcePriority[ev.Source]
			switch {
			case evRank > curRank:
				merged[cap] = ev
				rank[cap] = evRank
			case evRank < curRank:
				// Lower-priority source; ignore.
			default:
				merged[cap] = resolveConflict(cap, cur, ev)
			}
		}
	}
	return merged
}

func resolveConflict(cap Capability, a, b CapabilityEvidence) CapabilityEvidence {
	if a.Level == b.Level {
		return a
	}
	winner := a
	if levelRank(b.Level) > levelRank(a.Level) {
		winner = b
	}
	winner.Detail = fmt.Sprintf(
		"conflicting %s evidence for %q: %s vs %s; resolved to %s (conservative)",
		winner.Source, cap, a.Level, b.Level, winner.Level,
	)
	return winner
}

// Intersect combines a runtime-level capability set with a model-level
// capability set: a runtime-unsupported capability stays unsupported
// regardless of model evidence, an unknown-at-runtime capability defers to
// the model's evidence, and a runtime-only capability the model has no
// opinion on passes through unchanged.
func Intersect(runtimeCaps, modelCaps CapabilitySet) CapabilitySet {
	result := make(CapabilitySet, len(runtimeCaps)+len(modelCaps))
	for cap, modelEv := range modelCaps {
		if runtimeEv, ok := runtimeCaps[cap]; ok && runtimeEv.Level == SupportUnsupported {
			result[cap] = runtimeEv
			continue
		}
		result[cap] = modelEv
	}
	for cap, runtimeEv := range runtimeCaps {
		if _, exists := result[cap]; !exists {
			result[cap] = runtimeEv
		}
	}
	return result
}

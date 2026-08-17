package runtime

type Kind string

const (
	KindVLLM    Kind = "vllm"
	KindSGLang  Kind = "sglang"
	KindOllama  Kind = "ollama"
	KindComfyUI Kind = "comfyui"
)

// Package tunnel implements the Agent side of the mTLS gRPC tunnel: an
// outbound, long-lived connection to every Gateway replica that delivers the
// node's local vLLM, SGLang, Ollama and ComfyUI runtimes without requiring any
// inbound port, DDNS or UPnP.
//
// The tunnel carries runtime semantics, never arbitrary HTTP. Every proto
// message is encoded and decoded by AIServeWeave/common/tunnelwire, the codec
// shared with the Gateway, so dispatch, control and slot code never touch
// proto fields inline. See README.md for the full design.
package tunnel

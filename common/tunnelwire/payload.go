package tunnelwire

import (
	"slices"
	"sync"

	"github.com/klauspost/compress/zstd"

	tunnelv1 "AIServeWeave/api/proto/tunnel/v1"
	"AIServeWeave/common/runtime"
)

// Request payload compression. A Claude Code turn resends its whole context,
// so a Messages payload of hundreds of KB to several MB is routine; on a
// cross-region tunnel every extra congestion window of bytes is another round
// trip before the Agent can even start the request. JSON compresses several
// times over, so the Gateway compresses large request payloads for slots that
// declared they can decode them.
//
// Only RequestHeaders.payload is compressed. Streamed response frames are
// not: they are small, and a compressor that fills a block before emitting
// would hold back the first token.
//
// 请求 payload 压缩。Claude Code 每一轮都重发完整上下文，几百 KB 到数 MB 的
// Messages payload 很常见；在跨地域隧道上，每多一个拥塞窗口的字节，就要多等一个往返
// Agent 才能开始处理请求。JSON 可以压缩数倍，因此 Gateway 对声明了可解码的槽压缩大的
// 请求 payload。
//
// 只压缩 RequestHeaders.payload。流式响应帧不压缩：它们很小，而攒满一块再输出的压缩器
// 会拖住首个 token。

// PayloadCompressionMinBytes is the smallest payload worth compressing. Below
// it the bytes fit in the first congestion windows anyway, and compression
// would only add CPU time.
//
// PayloadCompressionMinBytes 是值得压缩的最小 payload。低于它时这些字节本就能装进
// 最初的几个拥塞窗口，压缩只会徒增 CPU 时间。
const PayloadCompressionMinBytes = 16 << 10

// supportedPayloadEncodings is what this build decodes beyond identity.
var supportedPayloadEncodings = []tunnelv1.PayloadEncoding{tunnelv1.PayloadEncoding_PAYLOAD_ENCODING_ZSTD}

// SupportedPayloadEncodings returns the encodings an Agent built from this
// code declares on Ready. The caller gets its own copy.
//
// SupportedPayloadEncodings 返回由本代码构建的 Agent 在 Ready 中声明的编码。调用方拿到
// 的是独立副本。
func SupportedPayloadEncodings() []tunnelv1.PayloadEncoding {
	return slices.Clone(supportedPayloadEncodings)
}

var zstdEncoder = sync.OnceValues(func() (*zstd.Encoder, error) {
	return zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithLowerEncoderMem(true))
})

// The decoder refuses output beyond MaxTunnelMessageBytes. That is no new
// ceiling: an identity payload larger than it never got past gRPC's message
// limit, so everything that decoded before still decodes, and a hostile
// frame cannot expand into unbounded memory.
//
// 解码器拒绝超过 MaxTunnelMessageBytes 的输出。这不是新增的上限：比它更大的原文
// payload 本就过不了 gRPC 的消息上限，因此以前能解码的现在仍能解码，而恶意帧也无法
// 膨胀成无界内存。
var zstdDecoder = sync.OnceValues(func() (*zstd.Decoder, error) {
	return zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(0),
		zstd.WithDecoderMaxMemory(MaxTunnelMessageBytes))
})

// EncodePayload returns payload as it should travel to a slot that accepts
// the given encodings, and the encoding used. It falls back to identity when
// the slot accepts nothing else, when the payload is too small or too large
// to be worth it, or when compression would not save at least an eighth.
// Payloads larger than MaxTunnelMessageBytes stay identity, so gRPC refuses
// them exactly as it did before compression existed.
//
// EncodePayload 返回 payload 发往接受给定编码的槽时应有的形式，以及所用编码。槽不接受
// 其他编码、payload 太小或太大不值得压缩、或压缩省不下至少八分之一时，退回原文。大于
// MaxTunnelMessageBytes 的 payload 保持原文，使 gRPC 像引入压缩之前一样拒绝它。
func EncodePayload(payload []byte, accepted []tunnelv1.PayloadEncoding) ([]byte, tunnelv1.PayloadEncoding) {
	identity := tunnelv1.PayloadEncoding_PAYLOAD_ENCODING_IDENTITY
	if len(payload) < PayloadCompressionMinBytes || len(payload) > MaxTunnelMessageBytes ||
		!slices.Contains(accepted, tunnelv1.PayloadEncoding_PAYLOAD_ENCODING_ZSTD) {
		return payload, identity
	}
	enc, err := zstdEncoder()
	if err != nil {
		return payload, identity
	}
	compressed := enc.EncodeAll(payload, make([]byte, 0, len(payload)/4))
	if len(compressed) > len(payload)-len(payload)/8 {
		return payload, identity
	}
	return compressed, tunnelv1.PayloadEncoding_PAYLOAD_ENCODING_ZSTD
}

// DecodePayload undoes EncodePayload. An unknown encoding, a corrupt frame or
// one that expands past MaxTunnelMessageBytes is a protocol error.
//
// DecodePayload 还原 EncodePayload 的结果。未知编码、损坏的帧或膨胀超过
// MaxTunnelMessageBytes 的帧都是协议错误。
func DecodePayload(payload []byte, encoding tunnelv1.PayloadEncoding) ([]byte, error) {
	switch encoding {
	case tunnelv1.PayloadEncoding_PAYLOAD_ENCODING_IDENTITY:
		return payload, nil
	case tunnelv1.PayloadEncoding_PAYLOAD_ENCODING_ZSTD:
		dec, err := zstdDecoder()
		if err != nil {
			return nil, &runtime.RuntimeError{Code: runtime.ErrorProtocol, Message: "zstd payload decoder unavailable", Cause: err}
		}
		out, err := dec.DecodeAll(payload, nil)
		if err != nil {
			return nil, &runtime.RuntimeError{Code: runtime.ErrorProtocol, Message: "invalid zstd request payload"}
		}
		return out, nil
	default:
		return nil, &runtime.RuntimeError{Code: runtime.ErrorProtocol, Message: "unsupported request payload encoding"}
	}
}

package scheduler

import (
	"context"
	"io"

	"AIServeWeave/common/runtime"
)

// ImageCandidates ranks the nodes that published model with image generation.
// Image models carry no chat capability, so this never selects a chat model.
//
// ImageCandidates 对以图像生成能力发布了 model 的节点排序。图像模型不带聊天能力，
// 因此这里永远不会选中聊天模型。
func (s *Scheduler) ImageCandidates(ctx context.Context, model string) []Candidate {
	return s.candidates(ctx, model, runtime.CapabilityImageGeneration)
}

// GenerateImage dispatches req and returns the image's metadata together with
// a stream positioned at its first byte chunk. A failure before the metadata
// arrives retries on the next candidate while it is Retryable; once the
// metadata is read nothing is retried, because the node has already spent
// the account's image quota.
//
// GenerateImage 派发 req，返回图像元数据，以及一个已定位到第一个字节分块的流。
// 元数据到达前的失败在 Retryable 时换下一个候选重试；元数据一旦读到就不再重试，
// 因为该节点已经消耗了账户的生图额度。
func (s *Scheduler) GenerateImage(ctx context.Context, req runtime.ImageRequest) (runtime.ImageMeta, runtime.Stream[runtime.ImageEvent], Candidate, error) {
	if !modelAllowed(ctx, req.Model) {
		return runtime.ImageMeta{}, nil, Candidate{}, ErrModelNotAllowed
	}
	candidates := s.ImageCandidates(ctx, req.Model)
	s.metrics.Selection(runtime.CapabilityImageGeneration, len(candidates))
	if len(candidates) == 0 {
		return runtime.ImageMeta{}, nil, Candidate{}, ErrNoCapableNode
	}
	var lastErr error
	var cooled cooledCandidates
	for _, c := range candidates {
		if s.skipCooling(c, &cooled) {
			continue
		}
		attempt := req
		attempt.Model = c.Model
		stream, err := s.server.Runtime(c.NodeID, c.RuntimeID).GenerateImage(ctx, attempt)
		if err == nil {
			var first runtime.ImageEvent
			first, err = stream.Recv()
			if err == nil && first.Meta == nil {
				err = &runtime.RuntimeError{Code: runtime.ErrorProtocol, RuntimeID: c.RuntimeID, Message: "image stream did not start with metadata"}
			}
			if err == io.EOF {
				err = &runtime.RuntimeError{Code: runtime.ErrorProtocol, RuntimeID: c.RuntimeID, Message: "image stream ended before metadata"}
			}
			if err == nil {
				s.breakers.record(c, nil, s.clock.Now())
				s.metrics.Dispatch(c, nil)
				s.logDispatchDecision(ctx, "image_generate", c, nil)
				return *first.Meta, stream, c, nil
			}
			_ = stream.Close()
		}
		s.breakers.record(c, err, s.clock.Now())
		s.metrics.Dispatch(c, err)
		s.logDispatchDecision(ctx, "image_generate", c, err)
		lastErr = err
		if !retryable(err) {
			return runtime.ImageMeta{}, nil, c, err
		}
		s.metrics.Retry(runtime.CapabilityImageGeneration)
	}
	return runtime.ImageMeta{}, nil, Candidate{}, cooled.or(lastErr)
}

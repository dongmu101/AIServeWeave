package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"AIServeWeave/common/arkvideo"
	"AIServeWeave/common/idgen"
	"AIServeWeave/common/pricing"
	"AIServeWeave/common/reqid"
	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/flow"
)

// The Studio video_generation node calls Volcengine Ark's video generation
// tasks directly from the Gateway, with the Ark key the control plane hands
// out for the tenant: its own when it saved one, the platform's otherwise.
// The node creates a task, asks after it until it ends, and copies the video
// (and its last frame) into the tenant's object storage, since the URLs Ark
// returns expire after about a day. A node that is stopped cancels its task,
// which Ark allows only while the task is still queued.
//
// Usage is ledgered once the task succeeded, as completion tokens under the
// model id, with the attribute credential=tenant|platform: the control plane
// prices both, and charges only the platform key's.
//
// Studio 的 video_generation 节点从 Gateway 直接调用火山方舟的视频生成任务，所用 Key 由
// 控制面为该租户给出：租户保存了自己的就用它，否则用平台的。节点创建任务、持续询问直到它
// 结束，然后把视频（及其尾帧）复制进租户的对象存储，因为方舟返回的地址大约一天后就失效。
// 被停止的节点会取消它的任务，方舟只在任务仍在排队时允许取消。
//
// 任务成功后记一次账：以模型 id 记 completion token，并带属性 credential=tenant|platform：
// 控制面对两者都计价，只对平台 Key 扣费。

// Video node handling. / 视频节点处理参数。
const (
	// studioVideoPollInterval is how often a video node asks after its task;
	// studioVideoPollFailures how many failed asks in a row fail it.
	// studioVideoPollInterval 是视频节点询问其任务的间隔；studioVideoPollFailures 是连续
	// 多少次询问失败后令节点失败。
	studioVideoPollInterval = 5 * time.Second
	studioVideoPollFailures = 5
	// studioVideoExpiresAfter is how long Ark may keep a task queued or
	// running: the node's own timeout, so a task nobody waits for anymore
	// does not go on to run and bill. It is also Ark's smallest allowed
	// value.
	// studioVideoExpiresAfter 是方舟让任务排队或执行的最长时长：即节点自己的超时，使没有人
	// 再等待的任务不会继续执行并计费。它也是方舟允许的最小值。
	studioVideoExpiresAfter = flow.DefaultVideoNodeTimeout
	// studioVideoCancelTimeout bounds cancelling a task whose node was
	// stopped.
	// studioVideoCancelTimeout 限制取消一个节点已被停止的任务所用的时间。
	studioVideoCancelTimeout = 10 * time.Second
)

// Failure codes of the video node, on top of the artifact nodes' own.
// 视频节点的失败码，在产物节点自己的之外。
const (
	codeVideoUnavailable = "video_generation_unavailable"
	codeVideoFailed      = "video_generation_failed"
	codeContentRejected  = "content_rejected"
	codeVideoExpired     = "video_task_expired"
	codeImageMissing     = "image_missing"
)

// GenerateVideo implements flow.Dispatcher.
//
// GenerateVideo 实现 flow.Dispatcher。
func (d studioFlowDispatcher) GenerateVideo(ctx context.Context, call flow.VideoCall) (flow.MediaReply, error) {
	ctx = reqid.WithValue(ctx, reqid.New())
	identity, _ := IdentityFrom(ctx)
	cred, err := d.h.ark.credential(ctx, identity.TenantID)
	if err != nil {
		if ctx.Err() != nil {
			return flow.MediaReply{}, ctx.Err()
		}
		return flow.MediaReply{}, &flow.Error{Code: flow.CodeUpstream}
	}
	if !cred.Configured {
		return flow.MediaReply{}, &flow.Error{Code: codeVideoUnavailable}
	}
	if !cred.allows(call.Model) {
		return flow.MediaReply{}, &flow.Error{Code: codeModelUnavailable}
	}
	if !utf8.ValidString(call.Prompt) {
		return flow.MediaReply{}, &flow.Error{Code: codeInvalidInput}
	}
	// The result has to be kept somewhere before anything is paid for.
	// 在为任何东西付费之前，先确认结果有地方存放。
	target, err := d.h.files.writer(ctx, identity.TenantID)
	if err != nil || target.backend == nil {
		return flow.MediaReply{}, &flow.Error{Code: codeArtifactStorage}
	}
	req := arkvideo.TaskRequest{
		Model: call.Model, Prompt: call.Prompt, Resolution: call.Resolution, Ratio: call.Ratio,
		Duration: int64(call.Duration), GenerateAudio: call.GenerateAudio, Watermark: call.Watermark,
		ReturnLastFrame: true, ExpiresAfter: studioVideoExpiresAfter,
	}
	slots := []struct{ role, id string }{{arkvideo.RoleFirstFrame, call.FirstFrame}, {arkvideo.RoleLastFrame, call.LastFrame}}
	for _, id := range call.ReferenceImages {
		slots = append(slots, struct{ role, id string }{arkvideo.RoleReferenceImage, id})
	}
	for _, slot := range slots {
		if slot.id == "" {
			continue
		}
		url, err := d.imageDataURL(ctx, identity.TenantID, slot.id)
		if err != nil {
			return flow.MediaReply{}, err
		}
		req.Images = append(req.Images, arkvideo.Image{Role: slot.role, URL: url})
	}

	client := d.h.ark.client(cred)
	var taskID string
	if qerr := d.h.withQuota(ctx, func(ctx context.Context) { taskID, err = client.Create(ctx, req) }); qerr != nil {
		return flow.MediaReply{}, &flow.Error{Code: codeRateLimited}
	}
	if err != nil {
		return flow.MediaReply{}, arkFailure(ctx, err)
	}
	task, err := d.awaitVideo(ctx, client, taskID)
	if err != nil {
		if ctx.Err() != nil {
			d.cancelVideo(ctx, client, taskID)
			return flow.MediaReply{}, ctx.Err()
		}
		return flow.MediaReply{}, err
	}
	// Ark bills a task once it succeeded, whatever happens to the copy.
	// 任务成功后方舟即计费，无论复制结果如何。
	d.ledgerVideo(ctx, cred, call.Model, task)
	reply := flow.MediaReply{Usage: flow.Usage{CompletionTokens: task.CompletionTokens}}
	if task.VideoURL == "" {
		return reply, &flow.Error{Code: flow.CodeNoVideo}
	}
	video, err := d.storeVideoResult(ctx, target, task.VideoURL, fileKindVideo)
	if err != nil {
		return reply, err
	}
	reply.Artifacts = append(reply.Artifacts, video)
	if task.LastFrameURL != "" {
		// The last frame is a convenience; losing it does not fail the node.
		// 尾帧只是附带的便利；丢失它不令节点失败。
		if frame, err := d.storeVideoResult(ctx, target, task.LastFrameURL, fileKindImage); err == nil {
			reply.Artifacts = append(reply.Artifacts, frame)
		} else if ctx.Err() != nil {
			return reply, ctx.Err()
		}
	}
	return reply, nil
}

// imageDataURL reads one image of the run and returns it as a data: URL
// Ark accepts in place of a link.
//
// imageDataURL 读取本次运行的一张图片，并以方舟可代替链接接受的 data: URL 返回。
func (d studioFlowDispatcher) imageDataURL(ctx context.Context, tenantID, id string) (string, error) {
	body, _, err := d.openFile(ctx, tenantID, id)
	if err != nil {
		var fe *flow.Error
		if errors.As(err, &fe) && fe.Code == codeStudioArtifactMissing {
			return "", &flow.Error{Code: codeImageMissing}
		}
		return "", err
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &flow.Error{Code: codeArtifactStorage}
	}
	contentType := http.DetectContentType(data)
	if !strings.HasPrefix(contentType, "image/") || contentType == "image/svg+xml" {
		return "", &flow.Error{Code: codeInvalidInput}
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// awaitVideo asks after taskID until it ends, tolerating a few failed asks
// in a row before giving up. It returns the succeeded task.
//
// awaitVideo 持续询问 taskID 直到它结束；放弃之前容忍连续几次询问失败。返回成功的任务。
func (d studioFlowDispatcher) awaitVideo(ctx context.Context, client arkVideoClient, taskID string) (arkvideo.Task, error) {
	failures := 0
	for {
		task, err := client.Get(ctx, taskID)
		if ctx.Err() != nil {
			return arkvideo.Task{}, ctx.Err()
		}
		switch {
		case err != nil:
			if failures++; failures >= studioVideoPollFailures || arkvideo.IsKind(err, arkvideo.KindNotFound) || arkvideo.IsKind(err, arkvideo.KindAuth) {
				d.h.logger.Warn("asking after a studio video task failed", slog.String("run_id", d.session), slog.String("task_id", taskID), slog.Any("error", err))
				return arkvideo.Task{}, &flow.Error{Code: flow.CodeUpstream}
			}
		case task.Status == arkvideo.StatusSucceeded:
			return task, nil
		case task.Status == arkvideo.StatusFailed:
			d.h.logger.Info("a studio video task failed", slog.String("run_id", d.session), slog.String("task_id", taskID), slog.String("code", task.ErrorCode))
			if arkvideo.ContentRejected(task.ErrorCode) {
				return arkvideo.Task{}, &flow.Error{Code: codeContentRejected}
			}
			return arkvideo.Task{}, &flow.Error{Code: codeVideoFailed}
		case task.Status == arkvideo.StatusExpired:
			return arkvideo.Task{}, &flow.Error{Code: codeVideoExpired}
		case task.Status == arkvideo.StatusCancelled:
			return arkvideo.Task{}, &flow.Error{Code: codeJobCancelled}
		default:
			failures = 0
		}
		tick, stop := d.h.clock.NewTimer(studioVideoPollInterval)
		select {
		case <-ctx.Done():
			stop()
			return arkvideo.Task{}, ctx.Err()
		case <-tick:
		}
	}
}

// cancelVideo cancels a task whose node was stopped, best effort. Ark
// refuses it once the task is running; the task then runs to the end, or to
// studioVideoExpiresAfter, and is not ledgered.
//
// cancelVideo 尽力取消一个节点已被停止的任务。任务开始执行后方舟会拒绝；该任务随后运行到
// 结束或 studioVideoExpiresAfter，且不记账。
func (d studioFlowDispatcher) cancelVideo(ctx context.Context, client arkVideoClient, taskID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), studioVideoCancelTimeout)
	defer cancel()
	if err := client.Delete(ctx, taskID); err != nil {
		d.h.logger.Warn("cancelling a studio video task failed", slog.String("run_id", d.session), slog.String("task_id", taskID), slog.Any("error", err))
	}
}

// ledgerVideo records a succeeded task's usage. The video tokens are not
// counted against the tenant's tokens-per-minute limit: that limit is sized
// for chat, and one video would exhaust it.
//
// ledgerVideo 记录一个成功任务的用量。视频 token 不计入租户的每分钟 token 限额：该限额按
// 对话设定，一个视频就会把它耗尽。
func (d studioFlowDispatcher) ledgerVideo(ctx context.Context, cred ArkCredential, model string, task arkvideo.Task) {
	credential := pricing.CredentialPlatform
	if cred.Source == ArkSourceTenant {
		credential = pricing.CredentialTenant
	}
	total := task.TotalTokens
	if total < task.CompletionTokens {
		total = task.CompletionTokens
	}
	d.h.ledgerUsage(ctx, "", runtime.Usage{
		CompletionTokens: int(task.CompletionTokens),
		TotalTokens:      int(total),
		Attributes:       map[string]string{pricing.AttrCredential: credential},
	}, UsageEndpointVideoGeneration, model)
}

// storeVideoResult copies one result URL into target as an artifact of the
// run, streaming.
//
// storeVideoResult 把一个结果地址流式复制进 target，作为本次运行的产物。
func (d studioFlowDispatcher) storeVideoResult(ctx context.Context, target fileTarget, url, kind string) (flow.Artifact, error) {
	identity, _ := IdentityFrom(ctx)
	body, size, contentType, err := arkvideo.Fetch(ctx, d.h.ark.fetch, url)
	if err != nil {
		if ctx.Err() != nil {
			return flow.Artifact{}, ctx.Err()
		}
		d.h.logger.Warn("downloading a studio video result failed", slog.String("run_id", d.session), slog.Any("error", err))
		if arkvideo.IsKind(err, arkvideo.KindNotFound) {
			return flow.Artifact{}, &flow.Error{Code: codeVideoExpired}
		}
		return flow.Artifact{}, &flow.Error{Code: flow.CodeUpstream}
	}
	defer body.Close()
	head := make([]byte, uploadSniffPeekBytes)
	n, _ := io.ReadFull(body, head)
	head = head[:n]
	if sniffed := sniffedMediaType(head); sniffed != "application/octet-stream" {
		contentType = sniffed
	}
	artifactKind, filename := flow.ArtifactVideo, "video"+videoExtension(contentType)
	if kind == fileKindImage {
		artifactKind, filename = flow.ArtifactImage, "last_frame"+imageExtension(contentType)
	}
	id := studioArtifactPrefix + idgen.New()
	key, ok := studioArtifactKey(identity.TenantID, d.session, id)
	if !ok {
		return flow.Artifact{}, &flow.Error{Code: flow.CodeUpstream}
	}
	if _, err := d.h.files.put(ctx, target, FileRecord{
		ID: id, TenantID: identity.TenantID, ObjectKey: key, BizType: fileBizStudioRun, BizID: d.session,
		Kind: kind, Filename: filename, ContentType: contentType,
	}, io.MultiReader(bytes.NewReader(head), body), size); err != nil {
		if ctx.Err() != nil {
			return flow.Artifact{}, ctx.Err()
		}
		d.h.logger.Warn("storing a studio video result failed", slog.String("run_id", d.session), slog.Any("error", err))
		return flow.Artifact{}, &flow.Error{Code: codeArtifactStorage}
	}
	return flow.Artifact{ID: id, Filename: filename, Kind: artifactKind}, nil
}

// videoExtension is the video file extension for contentType, ".mp4" when
// it names no video type this package treats as one.
//
// videoExtension 是 contentType 对应的视频扩展名；它不是本包视为视频的类型时为「.mp4」。
func videoExtension(contentType string) string {
	exts, _ := mime.ExtensionsByType(contentType)
	for _, ext := range exts {
		if videoArtifactExtensions[ext] {
			return ext
		}
	}
	return ".mp4"
}

// arkFailure maps a failed task creation to a node failure, or returns
// ctx's error when the node was stopped.
//
// arkFailure 把创建任务的失败映射为节点失败；节点被停止时返回 ctx 的错误。
func arkFailure(ctx context.Context, err error) error {
	switch {
	case ctx.Err() != nil:
		return ctx.Err()
	case arkvideo.IsKind(err, arkvideo.KindContentRejected):
		return &flow.Error{Code: codeContentRejected}
	case arkvideo.IsKind(err, arkvideo.KindInvalid):
		return &flow.Error{Code: codeInvalidInput}
	case arkvideo.IsKind(err, arkvideo.KindRateLimited):
		return &flow.Error{Code: codeRateLimited}
	case arkvideo.IsKind(err, arkvideo.KindAuth), arkvideo.IsKind(err, arkvideo.KindNotFound):
		// A rejected key, or a model the key may not use.
		// Key 被拒，或该 Key 不能使用这个模型。
		return &flow.Error{Code: codeVideoUnavailable}
	}
	return &flow.Error{Code: flow.CodeUpstream}
}

// studioVideoModelsJSON answers GET /studio/v1/video-models. Available is
// false when no Ark key is configured for the tenant; FreeForm is true when
// the tenant's own key is used and any model id may be typed.
//
// studioVideoModelsJSON 是 GET /studio/v1/video-models 的答复。租户没有可用的方舟 Key 时
// Available 为 false；使用租户自己的 Key 时 FreeForm 为 true，可以填写任意模型 id。
type studioVideoModelsJSON struct {
	Data      []studioModelJSON `json:"data"`
	Available bool              `json:"available"`
	Source    string            `json:"source,omitempty"`
	FreeForm  bool              `json:"free_form"`
}

// videoModels implements GET /studio/v1/video-models.
//
// videoModels 实现 GET /studio/v1/video-models。
func (s *studioFlows) videoModels(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	cred, err := s.h.ark.credential(r.Context(), identity.TenantID)
	if err != nil {
		writeStudioError(w, http.StatusServiceUnavailable, "unavailable", "the video models could not be read")
		return
	}
	out := studioVideoModelsJSON{Data: []studioModelJSON{}}
	if cred.Configured {
		out.Available, out.Source, out.FreeForm = true, cred.Source, cred.Source == ArkSourceTenant
		for _, m := range cred.Models {
			out.Data = append(out.Data, studioModelJSON{ID: m, Name: m})
		}
	}
	writeStudioJSON(w, http.StatusOK, out)
}

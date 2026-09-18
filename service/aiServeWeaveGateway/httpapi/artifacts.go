package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveGateway/objectstore"
	"AIServeWeave/service/aiServeWeaveGateway/scheduler"
)

// MaxArtifactFilenameInHeader bounds the filename echoed in
// Content-Disposition. The name comes from the backend, and through the
// workflow's own save-node prefix ultimately from the caller.
//
// MaxArtifactFilenameInHeader 限制 Content-Disposition 中回显的文件名长度。这个名字
// 来自后端，并且经由工作流自己的保存节点前缀，最终来自调用方。
const MaxArtifactFilenameInHeader = 128

type artifactJSON struct {
	ArtifactID string `json:"artifact_id"`
	Filename   string `json:"filename"`
	// Type is the backend's own bucket name (output, temp, input). It says
	// where an artifact sits in the run's lifecycle, which a caller deciding
	// what to keep needs.
	//
	// Type 是后端自己的分区名（output、temp、input）。它说明产物处在该次运行生命周期
	// 的哪个位置，正在决定留下什么的调用方需要它。
	Type string `json:"type,omitempty"`
}

type artifactsResponse struct {
	Object string         `json:"object"`
	Data   []artifactJSON `json:"data"`
}

// listArtifacts implements GET /v1/jobs/{job_id}/artifacts.
//
// The listing is where public artifact ids come from. The backend addresses an
// artifact by filename, subfolder and type — a path into its own disk layout —
// and that triple never reaches the caller as an identifier: ids are minted
// here and resolved back through the store, so a caller cannot forge one for a
// file this run did not produce.
//
// Ids are stable across listings. Minting a fresh set per call would grow the
// store on every poll and invalidate ids a caller is still holding.
//
// listArtifacts 实现 GET /v1/jobs/{job_id}/artifacts。
//
// 公开的产物 id 就产生于列举这一步。后端用 filename、subfolder 与 type 三元组定位
// 产物——那是通往它自己磁盘布局的一条路径——这个三元组绝不作为标识符抵达调用方：id 在
// 此铸造、经由存储解回，因此调用方无法伪造一个指向本次运行没有产出的文件的 id。
//
// id 在多次列举之间保持稳定。每次调用都铸一套新的，会让每轮轮询都把存储撑大一点，
// 也会让调用方手上还攥着的 id 失效。
func (h *handlers) listArtifacts(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	jobID := r.PathValue("job_id")
	j, ok := h.jobs.get(jobID, identity.TenantID)
	if !ok {
		h.listPersistedArtifacts(w, r, identity.TenantID, jobID)
		return
	}

	refs, err := h.sched.WorkflowArtifacts(r.Context(), j.Candidate, j.RunID)
	if err != nil {
		handleDispatchError(w, h.logger, err)
		return
	}

	ids := h.jobs.recordArtifacts(j.ID, refs)
	data := make([]artifactJSON, 0, len(refs))
	for i, ref := range refs {
		data = append(data, artifactJSON{ArtifactID: ids[i], Filename: ref.Filename, Type: ref.Type})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(artifactsResponse{Object: "list", Data: data})
}

// listPersistedArtifacts answers GET /v1/jobs/{job_id}/artifacts for a job
// this replica has no local record of — a terminal job the J06 recovery
// sweep never restored (that sweep only covers non-terminal jobs), one
// submitted to a different replica, or one from before this replica's own
// restart (STATUS.md's Gateway 故障切换收尾). Unlike the local-hit path
// above it never asks a node directly: the whole point is this replica may
// have no live route to one. It therefore only ever answers with artifacts a
// previous listing already minted and the control plane confirmed — a job
// whose artifacts were never listed anywhere answers an empty list here
// exactly as it would on the replica that ran it.
//
// listPersistedArtifacts 为一个本副本毫无本地记录的 job 应答
// GET /v1/jobs/{job_id}/artifacts——一个 J06 恢复扫描从未恢复过的终态 job
// （那次扫描只覆盖非终态 job）、一个提交给了另一个副本的 job，或者本副本
// 自己重启之前的 job（STATUS.md 的「Gateway 故障切换收尾」）。与上面的本地
// 命中路径不同，它绝不会直接去问节点——这条路径存在的意义正是本副本可能
// 压根没有通向该节点的活路由。因此它只会应答此前某次列举已经铸造、且控制
// 面已确认的产物——一个从未在任何地方被列举过的 job，在这里得到的答复与在
// 跑它的那个副本上被问起时一样，是一份空列表。
func (h *handlers) listPersistedArtifacts(w http.ResponseWriter, r *http.Request, tenantID, jobID string) {
	if h.artifactRecovery == nil {
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "job_not_found", "no such job")
		return
	}
	exists, err := h.artifactRecovery.JobExists(r.Context(), tenantID, jobID)
	if err != nil || !exists {
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "job_not_found", "no such job")
		return
	}
	persisted, err := h.artifactRecovery.ListPersistedArtifacts(r.Context(), tenantID, jobID)
	if err != nil {
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "job_not_found", "no such job")
		return
	}
	data := make([]artifactJSON, 0, len(persisted))
	for _, a := range persisted {
		data = append(data, artifactJSON{ArtifactID: a.ArtifactID, Filename: a.Filename, Type: a.Type})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(artifactsResponse{Object: "list", Data: data})
}

// downloadArtifact implements GET /v1/artifacts/{artifact_id}, streaming the
// body straight through: a large generation is never held whole in this
// process, per AGENTS.md's "任何一跳都不得无界缓冲".
//
// When jobPersister has already copied this artifact into h.storage
// (STATUS.md's P04), that copy is preferred — it survives after the node
// that produced the artifact disconnects, which a live tunnel pull cannot.
// Any failure reading it, not found included, falls back to pulling live
// from the node exactly as this handler always has: a storage hiccup should
// not break a download the node can still serve directly, and an artifact
// recorded before object storage was configured (or while it is disabled)
// has no StorageKey to try in the first place.
//
// downloadArtifact 实现 GET /v1/artifacts/{artifact_id}，把响应体直通转发：
// 一次大的生成从不被本进程完整持有，对应 AGENTS.md 的「任何一跳都不得无界
// 缓冲」。
//
// 当 jobPersister 已经把这个产物复制进 h.storage（STATUS.md 的 P04）时，
// 优先读取那份副本——它在产出该产物的节点断开之后依然可用，这是一次实时的
// 隧道拉取做不到的。读取它失败的任何情形，包括未找到，都会回退到照旧从节点
// 实时拉取：存储的一次小故障不该弄坏一个节点本就还能直接服务的下载，而在
// 对象存储配置之前（或被关闭期间）记录的产物，本就没有 StorageKey 可以尝试。
func (h *handlers) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	artifactID := r.PathValue("artifact_id")
	rec, ok := h.jobs.artifact(artifactID, identity.TenantID)
	if !ok {
		rec, ok = h.recoverArtifactRoute(r.Context(), artifactID, identity.TenantID)
	}
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "artifact_not_found", "no such artifact")
		return
	}

	if rec.StorageKey != "" && h.storage != nil {
		body, info, err := h.storage.Open(r.Context(), rec.StorageKey)
		if err == nil {
			defer body.Close()
			contentType := rec.ContentType
			if contentType == "" {
				contentType = info.ContentType
			}
			size := rec.Size
			if size < 0 {
				size = info.Size
			}
			h.streamArtifact(w, rec, "storage", body, contentType, size)
			return
		}
		if !errors.Is(err, objectstore.ErrNotFound) {
			h.logger.Warn("reading a persisted artifact from storage failed; falling back to a live pull from the node",
				slog.String("job_id", rec.JobID), slog.String("storage_key", rec.StorageKey), slog.Any("error", err))
		}
	}

	artifact, err := h.sched.OpenArtifact(r.Context(), rec.Candidate, rec.Ref)
	if err != nil {
		handleDispatchError(w, h.logger, err)
		return
	}
	defer artifact.Body.Close()
	h.streamArtifact(w, rec, "node", artifact.Body, artifact.ContentType, artifact.Size)
}

// recoverArtifactRoute answers a download this replica has no local record
// of by asking the control plane directly for the artifact's route binding
// (STATUS.md's Gateway 故障切换收尾) — the fallback the J06 recovery sweep
// does not itself provide, since that sweep only restores non-terminal jobs
// and never touches artifacts. The record it returns is not cached into
// h.jobs: unlike a job recovered by that sweep, it has no owning entry in
// h.jobs.byID to be evicted alongside, and inserting it as a bare artifact
// would leave it unboundedly outliving anything this store's own eviction
// tracks.
//
// A miss here — no ArtifactRecoveryClient configured, or the control plane
// has no record either — is reported exactly like any other unknown
// artifact id: downloadArtifact cannot tell "never existed" apart from
// "recovery found nothing", and should not try to.
//
// recoverArtifactRoute 为一次本副本毫无本地记录的下载作答，做法是直接向控制面
// 询问这个产物的路由绑定（STATUS.md 的「Gateway 故障切换收尾」）——这是 J06
// 恢复扫描自己不提供的回退，因为那次扫描只恢复非终态 job，从不触及产物。它
// 返回的记录不会被缓存进 h.jobs：与那次扫描恢复的 job 不同，它在 h.jobs.byID
// 里没有可供一同逐出的所属条目，若把它当作一条裸产物插入，会让它无边界地
// 活得比这张表自己的逐出机制所能追踪的任何东西都久。
//
// 这里的未命中——未配置 ArtifactRecoveryClient，或控制面同样没有记录——会被
// 汇报成与任何其他未知产物 id 完全相同的结果：downloadArtifact 无法区分
// 「从未存在过」与「恢复也一无所获」，也不该去区分。
func (h *handlers) recoverArtifactRoute(ctx context.Context, artifactID, tenantID string) (artifactRecord, bool) {
	if h.artifactRecovery == nil {
		return artifactRecord{}, false
	}
	route, err := h.artifactRecovery.ArtifactRoute(ctx, tenantID, artifactID)
	if err != nil {
		return artifactRecord{}, false
	}
	return artifactRecord{
		JobID:       route.JobID,
		TenantID:    route.TenantID,
		Candidate:   scheduler.Candidate{NodeID: route.NodeID, RuntimeID: route.RuntimeID},
		Ref:         runtime.ArtifactRef{Filename: route.Filename, Subfolder: route.Subfolder, Type: route.Type},
		StorageKey:  route.StorageKey,
		ContentType: route.ContentType,
		Size:        route.SizeBytes,
	}, true
}

// streamArtifact writes the common response headers and copies body to w,
// the shared tail of downloadArtifact's two sources (object storage or a
// live node pull) — source names which one, for the one log line a
// mid-stream failure produces.
//
// streamArtifact 写入通用的响应头并把 body 拷贝进 w，是 downloadArtifact
// 两个来源（对象存储或一次实时的节点拉取）共用的尾段——source 指出是哪一个，
// 用于流式传输中途失败时的那一行日志。
func (h *handlers) streamArtifact(w http.ResponseWriter, rec artifactRecord, source string, body io.Reader, contentType string, size int64) {
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	if size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	if name := safeFilename(rec.Ref.Filename); name != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}

	if _, err := io.Copy(w, body); err != nil {
		// The status and some bytes are already out, so there is no error
		// body to write; the caller sees a short read, which is what a
		// truncated download looks like at every other layer too.
		//
		// 状态码与部分字节已经发出，因此没有错误体可写；调用方看到的是一次短读，
		// 而在其他每一层上，被截断的下载看起来也正是这样。
		h.logger.Error("streaming an artifact failed",
			slog.String("job_id", rec.JobID),
			slog.String("source", source),
			slog.Any("error", err))
	}
}

// safeFilename reduces a backend-supplied filename to something that cannot
// alter the response. Everything structural is removed rather than escaped:
// any directory part, because the name is a label here and not a path; CR, LF
// and quotes, because they would end the header value or its quoted string;
// and every other control character. An empty result means no
// Content-Disposition is sent at all, which is a better answer than a header
// built from something unrecognizable.
//
// safeFilename 把后端给出的文件名削减成无法改变响应的东西。所有结构性字符都被移除而
// 不是转义：任何目录部分，因为这里的名字是标签而非路径；CR、LF 与引号，因为它们会
// 提前结束响应头的值或其中的带引号字符串；以及其余所有控制字符。结果为空表示干脆不发
// Content-Disposition，那比用一个已经面目全非的东西拼出一个响应头要好。
func safeFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == "/" {
		return ""
	}
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '\\' {
			return -1
		}
		return r
	}, name)
	if len(cleaned) > MaxArtifactFilenameInHeader {
		cleaned = cleaned[:MaxArtifactFilenameInHeader]
	}
	return cleaned
}

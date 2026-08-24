package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path"
	"strconv"
	"strings"
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
	j, ok := h.jobs.get(r.PathValue("job_id"), identity.TenantID)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "job_not_found", "no such job")
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

// downloadArtifact implements GET /v1/artifacts/{artifact_id}, streaming the
// body straight through: the artifact is read from the tunnel as the client
// reads it, so a large generation is never held whole in this process and
// backpressure reaches the Agent through the same read, per AGENTS.md's
// "任何一跳都不得无界缓冲".
//
// downloadArtifact 实现 GET /v1/artifacts/{artifact_id}，把响应体直通转发：产物随
// 客户端的读取而从隧道读出，因此一次大的生成从不被本进程完整持有，背压也经由同一次
// 读取抵达 Agent，对应 AGENTS.md 的「任何一跳都不得无界缓冲」。
func (h *handlers) downloadArtifact(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	rec, ok := h.jobs.artifact(r.PathValue("artifact_id"), identity.TenantID)
	if !ok {
		writeOpenAIError(w, http.StatusNotFound, "invalid_request_error", "artifact_not_found", "no such artifact")
		return
	}

	artifact, err := h.sched.OpenArtifact(r.Context(), rec.Candidate, rec.Ref)
	if err != nil {
		handleDispatchError(w, h.logger, err)
		return
	}
	defer artifact.Body.Close()

	if artifact.ContentType != "" {
		w.Header().Set("Content-Type", artifact.ContentType)
	}
	if artifact.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(artifact.Size, 10))
	}
	if name := safeFilename(rec.Ref.Filename); name != "" {
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	}

	if _, err := io.Copy(w, artifact.Body); err != nil {
		// The status and some bytes are already out, so there is no error
		// body to write; the caller sees a short read, which is what a
		// truncated download looks like at every other layer too.
		//
		// 状态码与部分字节已经发出，因此没有错误体可写；调用方看到的是一次短读，
		// 而在其他每一层上，被截断的下载看起来也正是这样。
		h.logger.Error("streaming an artifact failed",
			slog.String("job_id", rec.JobID),
			slog.String("node_id", rec.Candidate.NodeID),
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

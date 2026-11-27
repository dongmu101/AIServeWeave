package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"strings"

	"AIServeWeave/common/flowdef"
	"AIServeWeave/common/idgen"
	"AIServeWeave/common/objectstore"
)

// Image and file inputs of a Studio flow run.
//
// A run starts with them as multipart/form-data: a "request" field holding
// the same JSON body a run without files sends, and one file part per image
// or file input, named after the input. Each file is checked like a workflow
// upload (extension allowlist and byte sniff; an image input takes images
// only), then written to object storage under the run, as an artifact of
// the run with its own sart_ id, before the run is stored. The run's inputs
// record each one as a flowdef.FileValue.
//
// A run can instead reuse a file an earlier run of the same member was given,
// with {"run_id", "artifact_id"} as the input's JSON value, which is how a
// rerun keeps its files. The earlier run must list that artifact among its
// inputs; the file is copied under the new run, so each run's files live and
// expire with that run.
//
// Studio 编排运行的 image 与 file 输入。
//
// 带这些输入的运行以 multipart/form-data 启动：「request」字段放与不带文件的运行相同的
// JSON 请求体，每个 image 或 file 输入一个以输入名命名的文件分片。每个文件都像工作流上传
// 一样受检（扩展名允许列表与字节嗅探；image 输入只接受图片），然后在存下运行之前，作为
// 该运行带有自己 sart_ id 的产物写入对象存储、放在该运行之下。运行的输入把每个文件记为
// 一个 flowdef.FileValue。
//
// 运行也可以复用同一成员更早的运行收到过的文件：以 {"run_id", "artifact_id"} 作为该输入
// 的 JSON 取值，重跑就是这样保留文件的。更早的运行必须在其输入中列有该产物；文件被复制
// 到新运行之下，因此每次运行的文件随该运行存在与过期。

// studioRunRequest is the JSON body of POST /studio/v1/flows/{id}/runs, or
// its "request" field when files are uploaded with it.
// studioRunRequest 是 POST /studio/v1/flows/{id}/runs 的 JSON 请求体；带文件上传时是其
// 「request」字段。
type studioRunRequest struct {
	Inputs  map[string]json.RawMessage `json:"inputs"`
	Version int64                      `json:"version"`
}

// studioReusedFile is the JSON value of an image or file input that reuses
// a file an earlier run was given.
// studioReusedFile 是复用更早运行收到的文件时，image 或 file 输入的 JSON 取值。
type studioReusedFile struct {
	RunID      string `json:"run_id"`
	ArtifactID string `json:"artifact_id"`
}

// studioRunForm is a parsed multipart run request; nil for a JSON one.
// studioRunForm 是解析后的 multipart 运行请求；JSON 请求时为 nil。
type studioRunForm struct {
	form *multipart.Form
}

func (f *studioRunForm) file(name string) *multipart.FileHeader {
	if f == nil || len(f.form.File[name]) == 0 {
		return nil
	}
	return f.form.File[name][0]
}

func (f *studioRunForm) close() {
	if f != nil {
		_ = f.form.RemoveAll()
	}
}

// parseRunStart reads a run request in either shape, answering a malformed
// one itself. A multipart request is bounded like a workflow upload.
//
// parseRunStart 读取任一形状的运行请求，格式有误时自行应答。multipart 请求的上限与工作流
// 上传相同。
func (s *studioFlows) parseRunStart(w http.ResponseWriter, r *http.Request) (studioRunRequest, *studioRunForm, bool) {
	var req studioRunRequest
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "multipart/form-data" {
		return req, nil, decodeStudioBodyLimit(w, r, &req, maxStudioRunBodyBytes)
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxWorkflowUploadBytes)
	if err := r.ParseMultipartForm(maxWorkflowUploadMemory); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeStudioError(w, http.StatusRequestEntityTooLarge, "too_large", "the request body is too large")
		} else {
			writeStudioError(w, http.StatusBadRequest, "invalid_request", "the request body is not a valid multipart upload")
		}
		return req, nil, false
	}
	form := &studioRunForm{form: r.MultipartForm}
	if vals := r.MultipartForm.Value["request"]; len(vals) > 0 {
		decoder := json.NewDecoder(strings.NewReader(vals[0]))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			form.close()
			writeStudioError(w, http.StatusBadRequest, "invalid_request", `the "request" field is not valid JSON for this endpoint`)
			return req, nil, false
		}
	}
	return req, form, true
}

// stagedInputs is what stageInputFiles wrote for one run: the input values
// to bind, the run's input files by artifact id, the storage they were
// written to, and the keys to delete if the run does not start after all.
//
// stagedInputs 是 stageInputFiles 为一次运行写下的内容：要绑定的输入值、按产物 id 索引的
// 运行输入文件、写入的存储，以及运行最终未能启动时要删除的 key。
type stagedInputs struct {
	values map[string]json.RawMessage
	files  map[string]flowdef.FileValue
	target fileTarget
	// written lists the files staged so far, by id and key.
	// written 按 id 与 key 列出至今暂存的文件。
	written []stagedFile
}

type stagedFile struct{ id, key string }

// discard deletes the files staged for a run that did not start, with their
// records; one that cannot be deleted is left to the cleanup.
// discard 删除为未能启动的运行暂存的文件及其记录；删不掉的留给清理。
func (s *studioFlows) discard(ctx context.Context, staged stagedInputs) {
	for _, f := range staged.written {
		s.h.files.remove(ctx, staged.target, f.id, f.key)
	}
}

// stageInputFiles writes the run's uploaded and reused files to object
// storage under runID and returns the values to bind. Problems with the
// values given are issues, located on the input; ok is false when the
// request was answered.
//
// stageInputFiles 把运行上传与复用的文件写入对象存储中 runID 之下，并返回要绑定的取值。
// 所给取值的问题以问题列表的形式返回，定位到相应输入；请求已被应答时 ok 为 false。
func (s *studioFlows) stageInputFiles(w http.ResponseWriter, r *http.Request, owner StudioOwner, runID string, def flowdef.Definition, req studioRunRequest, form *studioRunForm) (stagedInputs, bool) {
	staged := stagedInputs{values: make(map[string]json.RawMessage, len(req.Inputs)), files: map[string]flowdef.FileValue{}}
	for name, raw := range req.Inputs {
		staged.values[name] = raw
	}
	var issues []flowdef.Issue
	issue := func(name, code, message string) {
		issues = append(issues, flowdef.Issue{Field: "inputs." + name, Code: flowdef.IssueCode(code), Message: message})
	}
	fileInputs := map[string]bool{}
	for _, in := range def.Inputs {
		if !flowdef.IsFileType(in.Type) {
			continue
		}
		fileInputs[in.Name] = true
		part, raw := form.file(in.Name), req.Inputs[in.Name]
		given := len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
		if part == nil && !given {
			continue
		}
		if part != nil && given {
			issue(in.Name, string(flowdef.CodeInvalidValue), "give either an uploaded file or an earlier run's file, not both")
			continue
		}
		if staged.target.backend == nil {
			// The tenant's own storage when it has one, else the
			// deployment's; settings that cannot be read refuse the run
			// rather than store its files where the tenant did not choose.
			// 租户有自有存储时用它，否则用部署存储；读不到设置时拒绝运行，而不是把文件存到
			// 租户没有选择的地方。
			target, err := s.h.files.writer(r.Context(), owner.TenantID)
			if err != nil {
				s.discard(r.Context(), staged)
				writeStudioError(w, http.StatusServiceUnavailable, "storage_unavailable", "uploaded files cannot be stored right now")
				return stagedInputs{}, false
			}
			if target.backend == nil {
				issue(in.Name, codeArtifactStorage, "this deployment has no artifact storage for uploaded files")
				continue
			}
			staged.target = target
		}
		var (
			value flowdef.FileValue
			code  string
			msg   string
		)
		if part != nil {
			value, code, msg = s.stageUpload(r.Context(), owner, runID, in.Type, part, &staged)
		} else {
			value, code, msg = s.stageReuse(r.Context(), owner, runID, in.Type, raw, &staged)
		}
		if code != "" {
			if code == "storage" {
				s.discard(r.Context(), staged)
				writeStudioError(w, http.StatusServiceUnavailable, "storage_unavailable", "uploaded files cannot be stored right now")
				return stagedInputs{}, false
			}
			issue(in.Name, code, msg)
			continue
		}
		encoded, _ := json.Marshal(value)
		staged.values[in.Name] = encoded
		staged.files[value.ArtifactID] = value
	}
	if form != nil {
		for name := range form.form.File {
			if !fileInputs[name] {
				issue(truncateStudioName(name), string(flowdef.CodeUnknownInput), "this file part names no image or file input")
			}
		}
	}
	if len(issues) > 0 {
		s.discard(r.Context(), staged)
		writeStudioIssues(w, "invalid_inputs", "the input values are not valid", issues, false)
		return stagedInputs{}, false
	}
	return staged, true
}

// truncateStudioName bounds a caller-chosen name echoed in an issue.
// truncateStudioName 限制问题中回显的调用方所选名称的长度。
func truncateStudioName(name string) string {
	if len(name) > flowdef.MaxIdentLength {
		return name[:flowdef.MaxIdentLength]
	}
	return name
}

// acceptsFile reports whether filename may be the value of an input of type
// t: an extension workflow uploads allow and, for an image input, an image
// one. It returns the issue code and message when it may not.
//
// acceptsFile 报告 filename 能否作为 t 类型输入的取值：工作流上传允许的扩展名，对 image
// 输入还须是图片扩展名。不能时返回问题码与说明。
func (s *studioFlows) acceptsFile(t flowdef.ValueType, filename string) (string, string) {
	if !flowdef.ValidFilename(filename) {
		return string(flowdef.CodeInvalidValue), "the file name is not valid"
	}
	if validateUploadFilename(filename, s.h.allowedUploadExtensions) != nil {
		return "unsupported_file_type", "files of this type cannot be uploaded"
	}
	if t == flowdef.TypeImage && !imageArtifactExtensions[strings.ToLower(path.Ext(filename))] {
		return "unsupported_file_type", "an image input takes an image file"
	}
	return "", ""
}

// stageUpload checks one uploaded file and writes it under runID. The code
// "storage" means object storage failed, which is not the caller's problem.
//
// stageUpload 检查一个上传的文件并把它写到 runID 之下。码「storage」表示对象存储出错，
// 这不是调用方的问题。
func (s *studioFlows) stageUpload(ctx context.Context, owner StudioOwner, runID string, t flowdef.ValueType, part *multipart.FileHeader, staged *stagedInputs) (flowdef.FileValue, string, string) {
	filename := path.Base(strings.ReplaceAll(part.Filename, "\\", "/"))
	if code, msg := s.acceptsFile(t, filename); code != "" {
		return flowdef.FileValue{}, code, msg
	}
	f, err := part.Open()
	if err != nil {
		return flowdef.FileValue{}, "storage", ""
	}
	defer f.Close()
	body, err := validateUploadContent(f, filename)
	if err != nil {
		if errors.Is(err, errUnsupportedUploadFormat) {
			return flowdef.FileValue{}, "unsupported_file_type", "the file's content does not match its type"
		}
		return flowdef.FileValue{}, "storage", ""
	}
	return s.storeInputFile(ctx, owner, runID, fileKindUpload, filename, body, part.Size, staged)
}

// stageReuse copies a file an earlier run of owner was given under runID.
// stageReuse 把 owner 更早的运行收到的文件复制到 runID 之下。
func (s *studioFlows) stageReuse(ctx context.Context, owner StudioOwner, runID string, t flowdef.ValueType, raw json.RawMessage, staged *stagedInputs) (flowdef.FileValue, string, string) {
	var ref studioReusedFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&ref) != nil || !strings.HasPrefix(ref.RunID, studioRunIDPrefix) || !flowdef.ValidArtifactID(ref.RunID) || !flowdef.ValidArtifactID(ref.ArtifactID) {
		return flowdef.FileValue{}, string(flowdef.CodeInvalidValue), "must be an uploaded file, or {run_id, artifact_id} naming a file an earlier run was given"
	}
	const gone = "the earlier run's file no longer exists"
	var earlier StudioRun
	if active, ok := s.runner.lookup(owner, ref.RunID); ok {
		earlier, _ = active.view()
	} else {
		var err error
		if earlier, err = s.store.GetRun(ctx, owner, ref.RunID); err != nil {
			if errors.Is(err, ErrStudioNotFound) {
				return flowdef.FileValue{}, codeStudioArtifactMissing, gone
			}
			return flowdef.FileValue{}, "storage", ""
		}
	}
	prior, ok := runInputFiles(earlier)[ref.ArtifactID]
	if !ok {
		return flowdef.FileValue{}, codeStudioArtifactMissing, gone
	}
	if code, msg := s.acceptsFile(t, prior.Filename); code != "" {
		return flowdef.FileValue{}, code, msg
	}
	key, ok := studioArtifactKey(owner.TenantID, ref.RunID, ref.ArtifactID)
	if !ok {
		return flowdef.FileValue{}, codeStudioArtifactMissing, gone
	}
	body, info, err := s.h.files.open(ctx, owner.TenantID, key)
	if errors.Is(err, objectstore.ErrNotFound) {
		return flowdef.FileValue{}, codeStudioArtifactMissing, gone
	}
	if err != nil {
		return flowdef.FileValue{}, "storage", ""
	}
	defer body.Close()
	return s.storeInputFile(ctx, owner, runID, fileKindCopy, prior.Filename, body, info.Size, staged)
}

// storeInputFile writes one input file under runID as a new artifact of that
// run, registered as kind (an upload or a copy of an earlier run's file).
// storeInputFile 把一个输入文件作为该运行的新产物写到 runID 之下，按 kind（上传，或更早
// 运行文件的副本）登记。
func (s *studioFlows) storeInputFile(ctx context.Context, owner StudioOwner, runID, kind, filename string, body io.Reader, size int64, staged *stagedInputs) (flowdef.FileValue, string, string) {
	id := studioArtifactPrefix + idgen.New()
	key, ok := studioArtifactKey(owner.TenantID, runID, id)
	if !ok {
		return flowdef.FileValue{}, "storage", ""
	}
	if _, err := s.h.files.put(ctx, staged.target, FileRecord{
		ID: id, TenantID: owner.TenantID, ObjectKey: key, BizType: fileBizStudioRun, BizID: runID,
		Kind: kind, Filename: filename, ContentType: mime.TypeByExtension(path.Ext(filename)),
	}, body, size); err != nil {
		s.logger.Warn("storing a studio input file failed", slog.String("run_id", runID), slog.Any("error", err))
		return flowdef.FileValue{}, "storage", ""
	}
	staged.written = append(staged.written, stagedFile{id: id, key: key})
	return flowdef.FileValue{ArtifactID: id, Filename: filename}, "", ""
}

// runInputFiles lists the files run was started with, by artifact id.
// runInputFiles 按产物 id 列出运行启动时收到的文件。
func runInputFiles(run StudioRun) map[string]flowdef.FileValue {
	var values map[string]json.RawMessage
	if json.Unmarshal(run.Inputs, &values) != nil {
		return nil
	}
	out := map[string]flowdef.FileValue{}
	for _, raw := range values {
		var f flowdef.FileValue
		if len(raw) > 0 && raw[0] == '{' && json.Unmarshal(raw, &f) == nil && flowdef.ValidArtifactID(f.ArtifactID) && strings.HasPrefix(f.ArtifactID, studioArtifactPrefix) {
			out[f.ArtifactID] = f
		}
	}
	return out
}

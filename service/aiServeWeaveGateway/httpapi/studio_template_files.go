package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"slices"
	"strings"

	"AIServeWeave/common/objectstore"
	"AIServeWeave/service/aiServeWeaveGateway/flow"
)

// A comfyui_template node whose template takes files submits the run as
// multipart/form-data, as POST /v1/workflows/{id}/runs takes it from an API
// key: the "inputs" field and one file part per file input. The handler then
// checks and uploads the files exactly as it does for that caller. The parts
// stream from where the run's files live — object storage for the run's
// inputs and inference images, a job's stored or live artifact otherwise —
// through a pipe, so no file is held whole here.
//
// 模板需要文件的 comfyui_template 节点以 multipart/form-data 提交运行，与 API Key 调用方
// 提交 POST /v1/workflows/{id}/runs 的方式相同：「inputs」字段，加每个文件输入一个文件
// 分片。随后 handler 像对那类调用方一样检查并上传这些文件。分片经管道从运行的文件所在处
// 流式读出——运行的输入与推理图片在对象存储中，其他则是 job 已存放的或节点上的产物——因此
// 任何文件都不会在这里被完整持有。

// templateFile is one file a template input takes, open for reading.
// templateFile 是模板某个输入接收的一个文件，已打开待读。
type templateFile struct {
	input    string
	filename string
	body     io.ReadCloser
}

type templateFiles []templateFile

func (fs templateFiles) close() {
	for _, f := range fs {
		_ = f.body.Close()
	}
}

// openFiles opens the run's file named by each artifact id in files, keyed
// by template input. On failure the files opened so far are returned too, to
// be closed.
//
// openFiles 打开 files 中每个产物 id 所指的运行文件，以模板输入为键。失败时也返回已打开
// 的文件，以便关闭。
func (d studioFlowDispatcher) openFiles(ctx context.Context, tenantID string, files map[string]string) (templateFiles, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	var out templateFiles
	for _, name := range names {
		body, filename, err := d.openFile(ctx, tenantID, files[name])
		if err != nil {
			return out, err
		}
		out = append(out, templateFile{input: name, filename: filename, body: body})
	}
	return out, nil
}

// openFile opens one file of the run by artifact id, with the name to upload
// it under.
// openFile 按产物 id 打开运行的一个文件，并给出上传时使用的文件名。
func (d studioFlowDispatcher) openFile(ctx context.Context, tenantID, id string) (io.ReadCloser, string, error) {
	missing := &flow.Error{Code: codeStudioArtifactMissing}
	if strings.HasPrefix(id, studioArtifactPrefix) {
		key, ok := studioArtifactKey(tenantID, d.session, id)
		if !ok {
			return nil, "", missing
		}
		if !d.h.files.hasAny(tenantID) {
			return nil, "", &flow.Error{Code: codeArtifactStorage}
		}
		body, _, err := d.h.files.open(ctx, tenantID, key)
		if errors.Is(err, objectstore.ErrNotFound) {
			return nil, "", missing
		}
		if err != nil {
			return nil, "", &flow.Error{Code: codeArtifactStorage}
		}
		if f, ok := d.files[id]; ok {
			return body, f.Filename, nil
		}
		// An inference image of this run: named by its type.
		// 本次运行的推理图片：按其类型命名。
		head := make([]byte, uploadSniffPeekBytes)
		n, _ := io.ReadFull(body, head)
		head = head[:n]
		return readCloser{io.MultiReader(bytes.NewReader(head), body), body}, "image" + imageExtension(sniffedMediaType(head)), nil
	}

	rec, ok := d.h.jobs.artifact(id, tenantID)
	if !ok {
		rec, ok = d.h.recoverArtifactRoute(ctx, id, tenantID)
	}
	if !ok {
		return nil, "", missing
	}
	if rec.StorageKey != "" && d.h.files.hasAny(tenantID) {
		if body, _, err := d.h.files.open(ctx, tenantID, rec.StorageKey); err == nil {
			return body, rec.Ref.Filename, nil
		}
	}
	artifact, err := d.h.sched.OpenArtifact(ctx, rec.Candidate, rec.Ref)
	if err != nil {
		return nil, "", dispatchFailure(ctx, err)
	}
	return artifact.Body, rec.Ref.Filename, nil
}

type readCloser struct {
	io.Reader
	io.Closer
}

// errTemplateUploadAbandoned ends the part writer once the handler has
// stopped reading the body.
// errTemplateUploadAbandoned 在 handler 不再读取请求体后结束分片写入。
var errTemplateUploadAbandoned = errors.New("httpapi: workflow run handler stopped reading its upload")

// submitWithFileParts calls handler with req and files as a multipart body
// written through a pipe as the handler reads it.
//
// submitWithFileParts 以 multipart 请求体调用 handler，req 与 files 经管道在 handler
// 读取时写入。
func submitWithFileParts(ctx context.Context, handler http.HandlerFunc, route string, pathValues map[string]string, req runRequest, files templateFiles) *inprocResponse {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	encoded, _ := json.Marshal(req)
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := mw.WriteField("inputs", string(encoded))
		for _, f := range files {
			if err != nil {
				break
			}
			var part io.Writer
			if part, err = mw.CreateFormFile(f.input, f.filename); err == nil {
				_, err = io.Copy(part, f.body)
			}
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	resp := inprocBody(ctx, handler, http.MethodPost, route, pathValues, mw.FormDataContentType(), pr, -1)
	_ = pr.CloseWithError(errTemplateUploadAbandoned)
	<-done
	return resp
}

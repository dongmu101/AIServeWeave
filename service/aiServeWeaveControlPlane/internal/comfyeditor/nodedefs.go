package comfyeditor

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

//go:embed catalog/core.json
var coreCatalog []byte

//go:embed catalog/meta.json
var coreCatalogMeta []byte

// CatalogMeta records where the compiled-in node catalogue came from.
//
// CatalogMeta 记录编译进来的节点目录的来源。
type CatalogMeta struct {
	// ComfyUIVersion is the ComfyUI release the export was taken from. / ComfyUIVersion 是导出所取自的 ComfyUI 版本。
	ComfyUIVersion string `json:"comfyui_version"`
	// FrontendVersion is the ComfyUI_frontend release that ComfyUI version requires. / FrontendVersion 是该 ComfyUI 版本要求的前端版本。
	FrontendVersion string `json:"frontend_version"`
	// Generated is false while the catalogue is still the empty placeholder. / Generated 在目录仍是空占位时为 false。
	Generated bool `json:"generated"`
	// NodeCount is the number of node definitions kept. / NodeCount 是保留的节点定义数。
	NodeCount int `json:"node_count"`
	// Modules counts kept nodes by top-level python_module. / Modules 按顶层 python_module 统计保留的节点数。
	Modules map[string]int `json:"modules,omitempty"`
}

// Catalog returns the compiled-in core node catalogue and its provenance.
//
// Catalog 返回编译进来的核心节点目录及其来源。
func Catalog() ([]byte, CatalogMeta) {
	var meta CatalogMeta
	_ = json.Unmarshal(coreCatalogMeta, &meta)
	return coreCatalog, meta
}

// TrimOptions selects what TrimCatalog keeps. / TrimOptions 选择 TrimCatalog 保留哪些内容。
type TrimOptions struct {
	// IncludeAPINodes keeps comfy_api_nodes.*, Comfy's paid hosted-API nodes,
	// which nothing on this platform can run. Off by default.
	//
	// IncludeAPINodes 保留 comfy_api_nodes.*，即 Comfy 的付费托管 API 节点，
	// 本平台上没有任何东西能运行它们。默认关闭。
	IncludeAPINodes bool
}

// TrimReport summarizes one TrimCatalog run. / TrimReport 汇总一次 TrimCatalog 运行。
type TrimReport struct {
	Kept           int            `json:"kept"`
	DroppedCustom  int            `json:"dropped_custom"`
	DroppedAPI     int            `json:"dropped_api"`
	DroppedUnknown int            `json:"dropped_unknown"`
	StrippedCombos int            `json:"stripped_combos"`
	Modules        map[string]int `json:"modules"`
}

// fileExtensions are suffixes that mark a COMBO option as a file on the
// exporting machine (a model, an input image, a config) rather than a fixed
// enumeration such as a sampler name.
//
// fileExtensions 是把一个 COMBO 选项标记为导出机器上的文件（模型、输入图片、配置）
// 而不是固定枚举（例如采样器名）的后缀。
var fileExtensions = map[string]bool{
	".safetensors": true, ".sft": true, ".ckpt": true, ".pt": true, ".pth": true, ".bin": true, ".gguf": true,
	".onnx": true, ".pkl": true, ".yaml": true, ".yml": true, ".json": true, ".txt": true,
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".bmp": true, ".tif": true, ".tiff": true,
	".mp4": true, ".webm": true, ".mov": true, ".avi": true, ".mkv": true,
	".wav": true, ".mp3": true, ".flac": true, ".ogg": true, ".m4a": true,
	".glb": true, ".gltf": true, ".obj": true, ".fbx": true, ".ply": true, ".stl": true, ".splat": true,
}

// TrimCatalog turns a raw /object_info export into the catalogue this
// package serves. It keeps only core nodes (python_module not under
// custom_nodes, and not comfy_api_nodes unless asked), removes free-text
// descriptions and tooltips, and empties every COMBO whose options are files
// on the exporting machine, so neither third-party nodes nor anyone's local
// file names are compiled into the binary. Key order is preserved throughout:
// input order is widget order.
//
// TrimCatalog 把一份原始 /object_info 导出转换为本包提供的目录。它只保留核心节点
// （python_module 不在 custom_nodes 之下，且除非显式要求，也不是 comfy_api_nodes），
// 删除自由文本的描述与提示，并清空每个选项是导出机器上文件的 COMBO，使第三方节点与
// 任何人的本地文件名都不会被编译进二进制。全程保留键顺序：输入顺序就是 widget 顺序。
func TrimCatalog(raw []byte, opt TrimOptions) ([]byte, TrimReport, error) {
	report := TrimReport{Modules: map[string]int{}}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	root, err := decodeOrdered(dec)
	if err != nil {
		return nil, report, fmt.Errorf("comfyeditor: object_info is not valid JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, report, errors.New("comfyeditor: trailing data after object_info")
	}
	if root.kind != kindObject {
		return nil, report, errors.New("comfyeditor: object_info is not a JSON object")
	}
	kept := make([]member, 0, len(root.obj))
	for _, m := range root.obj {
		mod := ""
		if v := m.val.get("python_module"); v != nil && v.kind == kindScalar {
			_ = json.Unmarshal(v.raw, &mod)
		}
		top, _, _ := strings.Cut(mod, ".")
		switch {
		case m.val.kind != kindObject || mod == "":
			report.DroppedUnknown++
			continue
		case top == "custom_nodes":
			report.DroppedCustom++
			continue
		case top == "comfy_api_nodes" && !opt.IncludeAPINodes:
			report.DroppedAPI++
			continue
		}
		report.StrippedCombos += trimNode(m.val)
		report.Modules[top]++
		report.Kept++
		kept = append(kept, m)
	}
	root.obj = kept
	var buf bytes.Buffer
	root.encode(&buf)
	return buf.Bytes(), report, nil
}

func trimNode(node *ordered) int {
	node.del("description")
	node.del("output_tooltips")
	inputs := node.get("input")
	if inputs == nil || inputs.kind != kindObject {
		return 0
	}
	stripped := 0
	for _, group := range inputs.obj {
		if group.val.kind != kindObject {
			continue
		}
		for _, in := range group.val.obj {
			stripped += trimSpec(in.val)
		}
	}
	return stripped
}

// trimSpec walks one input spec to any depth. V3 dynamic inputs nest whole
// input groups inside a spec (COMFY_DYNAMICCOMBO_V3 options, autogrow
// templates), so tooltips and file lists can sit several levels down.
//
// trimSpec 递归遍历一个输入规格。V3 动态输入会在规格里嵌套整组输入
// （COMFY_DYNAMICCOMBO_V3 的 options、autogrow 的 template），因此提示与文件列表
// 可能藏在好几层之下。
func trimSpec(v *ordered) int {
	stripped := 0
	switch v.kind {
	case kindObject:
		v.del("tooltip")
		for _, m := range v.obj {
			stripped += trimSpec(m.val)
		}
	case kindArray:
		if hasFileOption(v) {
			v.arr = nil
			return 1
		}
		for _, item := range v.arr {
			stripped += trimSpec(item)
		}
	}
	return stripped
}

func hasFileOption(list *ordered) bool {
	for _, item := range list.arr {
		if item.kind != kindScalar {
			continue
		}
		var s string
		if json.Unmarshal(item.raw, &s) == nil && fileExtensions[strings.ToLower(path.Ext(s))] {
			return true
		}
	}
	return false
}

// ordered is a JSON value that keeps object member order, which
// encoding/json's maps would lose. / ordered 是保留对象成员顺序的 JSON 值，encoding/json 的 map 会丢失这个顺序。
type ordered struct {
	kind byte
	obj  []member
	arr  []*ordered
	raw  json.RawMessage
}

type member struct {
	key string
	val *ordered
}

const (
	kindScalar byte = iota
	kindObject
	kindArray
)

func decodeOrdered(dec *json.Decoder) (*ordered, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			v := &ordered{kind: kindObject}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				child, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				v.obj = append(v.obj, member{key: key, val: child})
			}
			_, err := dec.Token()
			return v, err
		case '[':
			v := &ordered{kind: kindArray}
			for dec.More() {
				child, err := decodeOrdered(dec)
				if err != nil {
					return nil, err
				}
				v.arr = append(v.arr, child)
			}
			_, err := dec.Token()
			return v, err
		}
		return nil, fmt.Errorf("unexpected delimiter %v", t)
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return nil, err
		}
		return &ordered{kind: kindScalar, raw: raw}, nil
	}
}

func (v *ordered) get(key string) *ordered {
	for _, m := range v.obj {
		if m.key == key {
			return m.val
		}
	}
	return nil
}

func (v *ordered) del(key string) {
	out := v.obj[:0]
	for _, m := range v.obj {
		if m.key != key {
			out = append(out, m)
		}
	}
	v.obj = out
}

func (v *ordered) encode(buf *bytes.Buffer) {
	switch v.kind {
	case kindObject:
		buf.WriteByte('{')
		for i, m := range v.obj {
			if i > 0 {
				buf.WriteByte(',')
			}
			key, _ := json.Marshal(m.key)
			buf.Write(key)
			buf.WriteByte(':')
			m.val.encode(buf)
		}
		buf.WriteByte('}')
	case kindArray:
		buf.WriteByte('[')
		for i, item := range v.arr {
			if i > 0 {
				buf.WriteByte(',')
			}
			item.encode(buf)
		}
		buf.WriteByte(']')
	default:
		buf.Write(v.raw)
	}
}

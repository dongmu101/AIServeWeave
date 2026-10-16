package apidocs

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

//go:embed seed/*.json
var seedFS embed.FS

// Seed is one compiled-in document, one file under seed/. To document a new
// endpoint, add a file; to change a seeded page, edit its file and raise
// Revision — a row still holding an older revision of an untouched seed is
// refreshed on the control plane's next start.
//
// Seed 是一份编译进二进制的文档，对应 seed/ 下的一个文件。要为新端点写文档，
// 新增一个文件即可；要修改某个种子页面，编辑其文件并提高 Revision——仍持有该
// 种子旧 revision、且未被运维改动过的行，会在控制面下次启动时被刷新。
type Seed struct {
	// Slug is the document key and must equal the file's base name.
	// / Slug 是文档键，必须与文件名（去扩展名）一致。
	Slug string `json:"slug"`
	// Revision orders versions of one seed; it must be at least 1.
	// / Revision 为同一种子的各版本排序，至少为 1。
	Revision int64 `json:"revision"`
	Meta
	Content Content `json:"content"`
}

// Seeds decodes and validates every compiled-in document, sorted by slug. A
// malformed seed is a build defect, so it is reported rather than skipped.
//
// Seeds 解码并校验所有编译进来的文档，按 slug 排序。格式有误的种子属于构建缺陷，
// 因此会被报告而不是被跳过。
func Seeds() ([]Seed, error) {
	entries, err := seedFS.ReadDir("seed")
	if err != nil {
		return nil, err
	}
	out := make([]Seed, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		body, err := seedFS.ReadFile(path.Join("seed", name))
		if err != nil {
			return nil, err
		}
		var s Seed
		decoder := json.NewDecoder(bytes.NewReader(body))
		// A misspelled field in a seed would otherwise vanish silently.
		// 否则种子里拼错的字段会悄无声息地消失。
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&s); err != nil {
			return nil, fmt.Errorf("apidocs: seed %s: %w", name, err)
		}
		if s.Slug != strings.TrimSuffix(name, ".json") || !ValidSlug(s.Slug) || s.Revision < 1 {
			return nil, fmt.Errorf("apidocs: seed %s: slug must match the file name and revision must be at least 1", name)
		}
		if _, err := Validate(s.Meta, s.Content); err != nil {
			return nil, fmt.Errorf("apidocs: seed %s: %w", name, err)
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

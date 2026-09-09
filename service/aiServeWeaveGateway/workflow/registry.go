package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"AIServeWeave/common/workflowtemplate"
)

// MaxTemplateBytes bounds one template manifest. An API-format graph is a
// few hundred kilobytes at the outside; anything larger is a mistake worth
// failing on at startup rather than carrying in memory for the process's
// lifetime.
//
// MaxTemplateBytes 限制单份模板清单的大小。一张 API Format 的图撑死几百 KB，更大的
// 是错误，值得在启动时失败，而不是在进程的余生里一直驻留内存。
const MaxTemplateBytes = 4 << 20

// Registry is the loaded catalogue of templates. It is built once at startup
// and read-only afterwards, so it needs no lock.
//
// Registry 是已加载的模板目录。它在启动时构建一次，此后只读，因此无需加锁。
type Registry struct {
	byID map[string]*Template
	ids  []string
}

// Load reads every template named by paths. A path may be a single manifest
// file or a directory, in which case its *.json entries are read and anything
// else is ignored. Each manifest holds one Template, and every template is
// validated before the registry is returned: a catalogue that half-loaded
// would leave the operator guessing which half.
//
// Load 读取 paths 指定的所有模板。path 可以是单份清单文件，也可以是目录——目录下的
// *.json 会被读取，其余一律忽略。每份清单存放一个 Template，且所有模板都在返回目录
// 前完成校验：一份加载了一半的目录只会让运维去猜是哪一半。
func Load(paths ...string) (*Registry, error) {
	reg := &Registry{byID: make(map[string]*Template)}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("workflow: reading %s: %w", path, err)
		}
		if !info.IsDir() {
			if err := reg.loadFile(path); err != nil {
				return nil, err
			}
			continue
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("workflow: reading %s: %w", path, err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
				continue
			}
			if err := reg.loadFile(filepath.Join(path, entry.Name())); err != nil {
				return nil, err
			}
		}
	}
	sort.Strings(reg.ids)
	return reg, nil
}

func (r *Registry) loadFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("workflow: reading %s: %w", path, err)
	}
	if info.Size() > MaxTemplateBytes {
		return fmt.Errorf("workflow: %s is %d bytes, over the %d limit", path, info.Size(), MaxTemplateBytes)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("workflow: reading %s: %w", path, err)
	}
	var tpl Template
	if err := json.Unmarshal(body, &tpl); err != nil {
		return fmt.Errorf("workflow: %s is not a valid template manifest: %w", path, err)
	}
	if err := tpl.Validate(); err != nil {
		return fmt.Errorf("workflow: %s: %w", path, err)
	}
	if _, dup := r.byID[tpl.ID]; dup {
		return fmt.Errorf("workflow: %s declares id %q, which another manifest already registered", path, tpl.ID)
	}
	r.byID[tpl.ID] = &tpl
	r.ids = append(r.ids, tpl.ID)
	return nil
}

// FromBundle builds a Registry from a set of published template revisions
// pulled from the control plane (P03's controlplane source), applying the
// same rules Load applies to files: every template is validated, a duplicate
// id fails the whole build, and Version/VisibleTenantIDs are carried onto
// each Template from its Snapshot.
//
// FromBundle 从一组自控制面拉取的已发布模板版本（P03 的 controlplane 来源）构建
// Registry，套用与 Load 对文件相同的规则：所有模板都会被校验，重复 id 会让整体
// 构建失败，且 Version/VisibleTenantIDs 从各自的 Snapshot 带到对应的 Template 上。
func FromBundle(snapshots []workflowtemplate.Snapshot) (*Registry, error) {
	reg := &Registry{byID: make(map[string]*Template)}
	for _, snap := range snapshots {
		tpl := &Template{
			ID:               snap.TemplateID,
			Description:      snap.Description,
			Inputs:           snap.Inputs,
			Outputs:          snap.Outputs,
			Dependencies:     snap.Dependencies,
			Graph:            snap.Graph,
			Version:          strconv.FormatInt(snap.Revision, 10),
			VisibleTenantIDs: snap.VisibleTenantIDs,
		}
		if err := tpl.Validate(); err != nil {
			return nil, fmt.Errorf("workflow: template %q from control plane: %w", tpl.ID, err)
		}
		if _, dup := reg.byID[tpl.ID]; dup {
			return nil, fmt.Errorf("workflow: control plane bundle declares id %q more than once", tpl.ID)
		}
		reg.byID[tpl.ID] = tpl
		reg.ids = append(reg.ids, tpl.ID)
	}
	sort.Strings(reg.ids)
	return reg, nil
}

// BundleDigest fingerprints every template currently in the registry,
// independently of iteration order, for status reporting (P03). A
// file-loaded template carries no revision, so it contributes Revision 0;
// this is the same registry either source builds, so both report through
// this one method rather than each computing its own notion of a digest.
//
// BundleDigest 为目录中当前的每个模板取指纹，不受遍历顺序影响，用于状态上报
// （P03）。文件加载的模板不携带版本，因此贡献 Revision 0；两种来源构建的是同一种
// Registry，因此都通过这一个方法上报，而不是各自计算一套摘要。
func (r *Registry) BundleDigest() (string, error) {
	if r == nil {
		return workflowtemplate.BundleDigest(nil)
	}
	infos := make([]workflowtemplate.RevisionInfo, 0, len(r.byID))
	for _, id := range r.ids {
		tpl := r.byID[id]
		digest, err := workflowtemplate.Digest(tpl.ID, workflowtemplate.Content{
			Description:  tpl.Description,
			Inputs:       tpl.Inputs,
			Outputs:      tpl.Outputs,
			Dependencies: tpl.Dependencies,
			Graph:        tpl.Graph,
		}, tpl.VisibleTenantIDs)
		if err != nil {
			return "", err
		}
		revision, _ := strconv.ParseInt(tpl.Version, 10, 64)
		infos = append(infos, workflowtemplate.RevisionInfo{TemplateID: tpl.ID, Revision: revision, Digest: digest})
	}
	return workflowtemplate.BundleDigest(infos)
}

// Lookup returns the template registered under id.
//
// Lookup 返回以 id 注册的模板。
func (r *Registry) Lookup(id string) (*Template, bool) {
	if r == nil {
		return nil, false
	}
	tpl, ok := r.byID[id]
	return tpl, ok
}

// IDs returns every registered template id, sorted.
//
// IDs 返回所有已注册的模板 id，已排序。
func (r *Registry) IDs() []string {
	if r == nil {
		return nil
	}
	out := make([]string, len(r.ids))
	copy(out, r.ids)
	return out
}

// Len is how many templates are registered.
//
// Len 是已注册模板的数量。
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	return len(r.byID)
}

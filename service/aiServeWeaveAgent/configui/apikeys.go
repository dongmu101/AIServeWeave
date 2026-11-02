package configui

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"AIServeWeave/common/runtime"
	"AIServeWeave/service/aiServeWeaveAgent/agentconfig"
)

// Form field prefixes carrying a hosted runtime's key and its clear request.
// The page names the fields after the runtime id, so they never share the
// runtimes text area, which is echoed back into the HTML.
//
// 携带托管运行时 Key 及清除请求的表单字段前缀。页面按运行时 id 命名这些字段，它们
// 从不进入会回显到 HTML 的 runtimes 文本框。
const (
	apiKeyField      = "api_key:"
	apiKeyClearField = "api_key_clear:"
)

// applyAPIKeys writes each key the form carries for a hosted runtime to its
// managed key file and points the runtime at it, or detaches the runtime's
// key file when the form asks to clear it. An empty key field keeps whatever
// keepFileOnlyRuntimeFields already carried over. Files are only written
// here; removing replaced ones is removeUnusedKeyFiles' job after the YAML
// has been saved.
//
// applyAPIKeys 把表单为托管运行时携带的每个 Key 写进它的受管 Key 文件并让运行时指向
// 它；表单要求清除时解除运行时的 Key 文件。Key 字段为空时保留
// keepFileOnlyRuntimeFields 已经带过来的值。这里只写文件；被替换的文件由
// removeUnusedKeyFiles 在 YAML 保存之后删除。
func applyAPIKeys(configPath string, runtimes []agentconfig.RuntimeConfig, form url.Values) error {
	for i := range runtimes {
		rc := &runtimes[i]
		if !runtime.Kind(rc.Kind).IsHosted() {
			rc.APIKeyFile = ""
			continue
		}
		if form.Get(apiKeyClearField+rc.ID) != "" {
			rc.APIKeyFile = ""
		}
		key := strings.TrimSpace(form.Get(apiKeyField + rc.ID))
		if key == "" {
			continue
		}
		path, err := filepath.Abs(agentconfig.KeyFilePath(configPath, rc.ID))
		if err != nil {
			return err
		}
		if err := agentconfig.WriteAPIKeyFile(path, key); err != nil {
			return err
		}
		rc.APIKeyFile = path
	}
	return nil
}

// removeUnusedKeyFiles deletes the managed key files saved referenced and
// next no longer does: a removed runtime, a cleared key or a changed
// endpoint. Files outside the managed folder were written by hand and stay.
//
// removeUnusedKeyFiles 删除 saved 引用过、而 next 不再引用的受管 Key 文件：运行时被
// 移除、Key 被清除或端点改变。受管目录之外的文件是手写的，保持不动。
func removeUnusedKeyFiles(configPath string, saved, next []agentconfig.RuntimeConfig) {
	used := make(map[string]bool, len(next))
	for _, rc := range next {
		if rc.APIKeyFile != "" {
			used[filepath.Clean(rc.APIKeyFile)] = true
		}
	}
	for _, rc := range saved {
		path := filepath.Clean(rc.APIKeyFile)
		if rc.APIKeyFile == "" || used[path] || !agentconfig.IsManagedKeyFile(configPath, path) {
			continue
		}
		// A file that cannot be removed only wastes space; the YAML no
		// longer points at it. / 删不掉的文件只占空间，YAML 已不再指向它。
		_ = os.Remove(path)
	}
}

// keyedRuntimes returns the ids of hosted runtimes with a key file.
// keyedRuntimes 返回带 Key 文件的托管运行时 id。
func keyedRuntimes(runtimes []agentconfig.RuntimeConfig) []string {
	ids := []string{}
	for _, rc := range runtimes {
		if rc.APIKeyFile != "" && runtime.Kind(rc.Kind).IsHosted() {
			ids = append(ids, rc.ID)
		}
	}
	return ids
}

package comfyuimanaged

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"AIServeWeave/common/comfyuimanagedstatus"
)

// versionMarkerFile is the file this package writes inside a custom node's
// own directory after a successful install, recording the ref that is
// actually on disk. Its presence and content — not the allowlist's declared
// ref — is what ListCustomNodes reports back as Version.
const versionMarkerFile = ".aisw-version"

// customNodeTokenPattern bounds NodeSpec.RepoURL, NodeSpec.Ref, and the
// install name to characters that cannot change the shape of the shell
// script buildInstallScript builds. The allowlist's source is this node's
// own trusted local flag config (see the package doc), not tunnel input,
// but the check costs little and removes an entire class of "a stray space
// in an operator's flag value quietly did something unintended" bugs.
//
// customNodeTokenPattern 限制 NodeSpec.RepoURL、NodeSpec.Ref 与安装名的字符
// 集，使其不可能改变 buildInstallScript 构造的脚本结构。允许列表的来源是本
// 节点自己可信的本地 flag 配置（见包文档），不是隧道输入，但这项检查成本很
// 低，且消除了整整一类"运维 flag 值里一个多余空格悄悄产生意外行为"的缺陷。
var customNodeTokenPattern = regexp.MustCompile(`^[A-Za-z0-9._/:-]+$`)

// NodeSpec is one allowlisted custom node's pinned install source, entirely
// local to this Agent (STATUS.md's P2 ComfyUI Managed Docker deployment,
// subtask 4). It is never accepted from the Gateway or control plane —
// InstallCustomNode only ever receives a name to look up in a caller-owned
// allowlist, never a NodeSpec pushed remotely.
//
// NodeSpec 是本 Agent 本地的一个自定义节点允许列表条目的固定安装源
// （STATUS.md 的 P2 ComfyUI Managed Docker 部署子任务四）。它从不接受来自
// Gateway 或控制面的输入——InstallCustomNode 只接受一个要在调用方持有的允
// 许列表里查找的名字，从不接受远程下发的 NodeSpec。
type NodeSpec struct {
	RepoURL string // git remote, cloned with --depth 1 / git 远程地址，以 --depth 1 克隆
	Ref     string // branch, tag, or commit; recorded back as the installed version / 分支、tag 或 commit；作为已安装版本记录回去
}

// validate rejects a NodeSpec whose fields could change buildInstallScript's
// shell structure, before any docker command runs.
func (spec NodeSpec) validate(name string) error {
	if !customNodeTokenPattern.MatchString(name) {
		return fmt.Errorf("comfyuimanaged: custom node name %q contains characters outside %s", name, customNodeTokenPattern.String())
	}
	if !customNodeTokenPattern.MatchString(spec.RepoURL) {
		return fmt.Errorf("comfyuimanaged: custom node %q RepoURL contains characters outside %s", name, customNodeTokenPattern.String())
	}
	if !customNodeTokenPattern.MatchString(spec.Ref) {
		return fmt.Errorf("comfyuimanaged: custom node %q Ref contains characters outside %s", name, customNodeTokenPattern.String())
	}
	return nil
}

// InstallCustomNode installs name — looked up in allowlist, never accepted
// as a literal URL — into containerName's customNodesDir via `docker exec`
// and git. Idempotent: a directory that already exists is fetched and
// checked out to Ref rather than re-cloned, so a repeated install (e.g. a
// retried trigger) converges rather than erroring on an existing directory.
// An unknown name is refused before any docker command runs, the same
// "before any network call" discipline modelpull.Puller applies to an
// unknown model name.
//
// InstallCustomNode 经 docker exec 与 git，把 name（在 allowlist 里查找，从
// 不接受字面 URL）安装进 containerName 的 customNodesDir。幂等：已存在的目
// 录会被 fetch 并检出到 Ref，而不是重新克隆，因此重复安装（例如一次被重试
// 的触发）会收敛而不是在已存在目录上报错。未知名字在任何 docker 命令执行前
// 就被拒绝，与 modelpull.Puller 对未知模型名"先于任何网络调用"拒绝同一纪律。
func (l *Launcher) InstallCustomNode(ctx context.Context, containerName, customNodesDir, name string, allowlist map[string]NodeSpec) error {
	spec, ok := allowlist[name]
	if !ok {
		return fmt.Errorf("comfyuimanaged: custom node %q is not in the local allowlist", name)
	}
	if !customNodeTokenPattern.MatchString(customNodesDir) {
		return fmt.Errorf("comfyuimanaged: custom nodes dir %q contains characters outside %s", customNodesDir, customNodeTokenPattern.String())
	}
	if err := spec.validate(name); err != nil {
		return err
	}
	script := buildInstallScript(customNodesDir, name, spec)
	if _, err := l.runDocker(ctx, "exec", containerName, "sh", "-c", script); err != nil {
		return fmt.Errorf("comfyuimanaged: installing custom node %q into %s: %w", name, containerName, err)
	}
	l.logger.Info("comfyuimanaged: custom node installed",
		"container", containerName, "name", name, "ref", spec.Ref)
	return nil
}

// buildInstallScript is a pure function: it never touches the network or a
// process, so its output is easy to assert in tests without a real docker
// daemon or container. It clones dir/name fresh if absent, or fetches and
// checks out Ref in place if the directory already exists, then records Ref
// into dir/name/versionMarkerFile so ListCustomNodes can read it back.
func buildInstallScript(dir, name string, spec NodeSpec) string {
	nodeDir := dir + "/" + name
	return strings.Join([]string{
		"set -e",
		fmt.Sprintf("mkdir -p %s", dir),
		fmt.Sprintf(
			"if [ -d %s/.git ]; then git -C %s fetch --depth 1 origin %s && git -C %s checkout FETCH_HEAD; "+
				"else git clone --depth 1 --branch %s %s %s; fi",
			nodeDir, nodeDir, spec.Ref, nodeDir, spec.Ref, spec.RepoURL, nodeDir,
		),
		fmt.Sprintf("echo %s > %s/%s", spec.Ref, nodeDir, versionMarkerFile),
	}, " && ")
}

// ListCustomNodes reads back, via a single `docker exec`, every
// subdirectory of customNodesDir and the version this package's own
// install marker recorded for it. A subdirectory with no marker — installed
// by something other than InstallCustomNode, or an install that failed
// before writing the marker — reports Version "unknown" rather than an
// error, matching Status's own "declared but not yet realized" pattern for
// container state.
//
// ListCustomNodes 经一次 docker exec 读回 customNodesDir 下的每一个子目录，
// 以及本包自己的安装标记为它记录的版本。一个没有标记的子目录——被
// InstallCustomNode 以外的东西安装，或一次在写标记前失败的安装——报告
// Version "unknown" 而不是错误，与 Status 对容器状态"已声明但尚未实现"的
// 处理是同一种模式。
func (l *Launcher) ListCustomNodes(ctx context.Context, containerName, customNodesDir string) ([]comfyuimanagedstatus.CustomNodeStatus, error) {
	// The script prints "<name>\t<version>" per line; a missing
	// customNodesDir (Managed mode configured but nothing ever installed)
	// produces no output rather than an error, since `for` over a
	// non-matching glob just iterates zero times under `set +f`... guarded
	// explicitly with a directory-exists check instead, so a genuinely
	// broken container path doesn't stay silent.
	script := fmt.Sprintf(
		`[ -d %s ] || exit 0; for d in %s/*/; do n=$(basename "$d"); v=$(cat "$d%s" 2>/dev/null || echo unknown); printf '%%s\t%%s\n' "$n" "$v"; done`,
		customNodesDir, customNodesDir, versionMarkerFile,
	)
	out, err := l.runDocker(ctx, "exec", containerName, "sh", "-c", script)
	if err != nil {
		return nil, fmt.Errorf("comfyuimanaged: listing custom nodes in %s: %w", containerName, err)
	}
	var statuses []comfyuimanagedstatus.CustomNodeStatus
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		name, version, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		statuses = append(statuses, comfyuimanagedstatus.CustomNodeStatus{Name: name, Version: version})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses, nil
}

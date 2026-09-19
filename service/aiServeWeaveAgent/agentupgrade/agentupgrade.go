// Package agentupgrade is STATUS.md's P2 Agent auto-upgrade: a local, signed
// manifest of known Agent versions, and a Checker that compares it against
// the Agent's own running version on demand, downloads and verifies a named
// version, drains this Agent's tunnel connections, and replaces its own
// process image. It is Agent-local and config-driven: the manifest comes
// from a local flag, not from the Gateway or control plane, the same
// restraint modelpull's manifest applies to model artifacts — an Action
// naming a version can only ever be answered against what this Agent
// already declared for itself.
//
// Subtask 1 implemented only the CHECK action. Subtask 2 adds UPGRADE's
// full chain: download the named version's binary, verify its SHA256
// against the value the manifest's Ed25519 signature already authenticated
// at load time, drain every tunnel connection this Agent holds (the
// Drainer, typically *tunnel.Manager), and syscall.Exec into the verified
// binary. ROLLBACK still reports ReasonNotImplemented — see the design
// doc's subtask breakdown,
// docs/superpowers/specs/2026-09-19-p2-agent-auto-upgrade-design.md.
//
// agentupgrade 是 STATUS.md P2 Agent 自动升级：一份已签名的本地已知 Agent 版
// 本清单，以及一个 Checker——按需把清单与 Agent 自己运行的版本比较，下载并
// 校验某个指名版本，排空本 Agent 的隧道连接，并替换自己的进程镜像。它是纯
// Agent 本地、配置驱动的：清单来自本地 flag，不来自 Gateway 或控制面，与
// modelpull 的清单对模型制品的同一种克制——一个指名版本的 Action 永远只能
// 针对本 Agent 自己已经声明过的东西来回答。
//
// 子任务一只实现了 CHECK 动作。子任务二补上了 UPGRADE 的完整链路：下载指
// 名版本的二进制、对照清单 Ed25519 签名在加载时已经验证过的值校验其
// SHA256、排空本 Agent 持有的每一条隧道连接（Drainer，通常是
// *tunnel.Manager），然后 syscall.Exec 进入校验通过的二进制。ROLLBACK 仍然
// 报告 ReasonNotImplemented——子任务拆分见设计文档
// docs/superpowers/specs/2026-09-19-p2-agent-auto-upgrade-design.md。
package agentupgrade

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"AIServeWeave/common/agentupgradestatus"
	"AIServeWeave/common/runtime"
)

// defaultDrainTimeout bounds how long an UPGRADE waits for this Agent's
// in-flight requests, across every tunnel connection it holds, to reach
// zero before proceeding anyway. It matches tunnel.Client's own
// DrainTimeout default (service/aiServeWeaveAgent/tunnel/client.go) — the
// design doc flags this as a conservative placeholder pending real
// production load data (section 六), not a value derived from measurement.
//
// defaultDrainTimeout 限定一次 UPGRADE 为等待本 Agent 持有的每一条隧道连接
// 上的在途请求归零最多等待多久，超时后仍会继续。它与 tunnel.Client 自己的
// DrainTimeout 默认值一致——设计文档（第六节）明确标注这是一个保守的占位
// 值，不是从真实测量得出的。
const defaultDrainTimeout = 30 * time.Second

// Entry describes one known Agent version.
//
// Entry 描述一个已知的 Agent 版本。
type Entry struct {
	Version string `json:"version"`
	// SourceURL is where the binary for this version is downloaded from
	// (STATUS.md P2 Agent auto-upgrade subtask 2: hosted on GitHub
	// Releases, per the maintainer's decision recorded in the design doc's
	// follow-up). It is never sent to the Agent by the Gateway or control
	// plane — only read from this local manifest.
	//
	// SourceURL 是这个版本二进制的下载地址（STATUS.md P2 Agent 自动升级子
	// 任务二：托管在 GitHub Releases，见设计文档后续记录的维护者决定）。它
	// 从不由 Gateway 或控制面下发给 Agent——只从这份本地清单读取。
	SourceURL string `json:"source_url,omitempty"`
	// SHA256 is the expected hex-encoded digest of the downloaded binary.
	// It is part of what Manifest.Signature authenticates (SignedContent),
	// so a downloaded file that does not match it means the transport
	// delivered the wrong bytes, not that the manifest was forged.
	//
	// SHA256 是下载二进制的期望十六进制摘要。它是 Manifest.Signature 认证
	// 内容（SignedContent）的一部分，因此下载文件与它不符意味着传输环节送
	// 错了字节，而不是清单被伪造。
	SHA256 string `json:"sha256,omitempty"`
}

// Manifest is the on-disk contract for -agent-upgrade-manifest: every known
// Agent version's (Version, SourceURL, SHA256) tuple, plus one Ed25519
// signature over the whole set of (Version, SHA256) pairs — not one
// signature per entry. The design doc's section 3.1 explains why: signing
// the set as a whole means an entry being silently deleted, or a hash being
// swapped for a different one, breaks the signature, not just a mismatched
// download. SourceURL is deliberately excluded from what is signed (see
// SignedContent).
//
// Manifest 是 -agent-upgrade-manifest 的落盘契约：每个已知 Agent 版本的
// (Version, SourceURL, SHA256) 三元组，加上一份对整组 (Version, SHA256) 的
// Ed25519 签名——不是每条各签一次。设计文档 3.1 节说明了理由：对整组签名意
// 味着一个条目被悄悄删除、或者某个哈希被换成另一个，都会破坏签名本身，而
// 不只是造成一次不匹配的下载。SourceURL 被刻意排除在签名内容之外（见
// SignedContent）。
type Manifest struct {
	Entries []Entry `json:"entries"`
	// Signature is the base64-encoded Ed25519 signature over
	// SignedContent(Entries), verified against a public key compiled into
	// the Agent binary.
	//
	// Signature 是 SignedContent(Entries) 的 base64 编码 Ed25519 签名，校
	// 验对象是编译进 Agent 二进制的公钥。
	Signature string `json:"signature"`
}

// SignedContent returns the canonical bytes a Manifest's Signature covers:
// every entry's (Version, SHA256) pair, sorted by Version so the signature
// does not depend on the JSON array's on-disk order. SourceURL is excluded
// — it says only where to fetch a version's bytes, which SHA256 already
// authenticates independently of the source, so relocating a version's
// download host never requires re-signing the manifest.
//
// SignedContent 返回一份 Manifest 的 Signature 所覆盖的规范字节：每个条目
// 的 (Version, SHA256) 对，按 Version 排序，使签名不依赖 JSON 数组在磁盘
// 上的排列顺序。SourceURL 被排除在外——它只说明去哪里获取某个版本的字节，
// 而 SHA256 已经独立于来源对内容做了身份验证，因此迁移某个版本的下载主机
// 从不需要重新签名清单。
func SignedContent(entries []Entry) []byte {
	sorted := append([]Entry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Version < sorted[j].Version })

	var buf bytes.Buffer
	for _, e := range sorted {
		buf.WriteString(e.Version)
		buf.WriteByte('\n')
		buf.WriteString(strings.ToLower(e.SHA256))
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// LoadManifest reads a JSON-encoded Manifest from path and verifies its
// Signature against pubKey before returning its entries. A missing or
// unparseable file, an undersized pubKey, an unparseable Signature, or a
// signature that does not verify are all reported as an error with no
// entries returned — this package trusts a manifest only when it can prove
// the entries came from whoever holds the private key, never merely because
// a file exists at a configured path (the design doc's threat model,
// section 二 points 2 and 3).
//
// LoadManifest 从 path 读取 JSON 编码的 Manifest，并在返回其条目之前用
// pubKey 校验它的 Signature。文件缺失或无法解析、pubKey 长度不对、
// Signature 无法解析，或者签名校验不通过，都会报告为错误且不返回任何条
// 目——本包只在能证明这些条目确实来自私钥持有者时才信任一份清单，绝不仅
// 仅因为配置路径上存在一个文件（设计文档威胁模型第二节第 2、3 条）。
func LoadManifest(path string, pubKey ed25519.PublicKey) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("agentupgrade: read manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("agentupgrade: parse manifest %s: %w", path, err)
	}
	if len(pubKey) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("agentupgrade: no agent upgrade public key configured; rejecting manifest %s", path)
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil {
		return nil, fmt.Errorf("agentupgrade: manifest %s signature is not valid base64: %w", path, err)
	}
	if !ed25519.Verify(pubKey, SignedContent(m.Entries), sig) {
		return nil, fmt.Errorf("agentupgrade: manifest %s failed signature verification", path)
	}
	return m.Entries, nil
}

// Drainer lets a Checker ask every connection this Agent holds to stop
// accepting new dispatch and wait for its in-flight work to finish, before
// the Agent replaces itself (design doc section 一.6/二 point 5). It is
// declared here as an interface rather than a direct dependency on the
// tunnel package: tunnel already imports agentupgrade for the AgentUpgrader
// interface, so a direct reverse dependency would cycle, and an interface
// lets tests fake it. *tunnel.Manager implements it via its own DrainAll
// method.
//
// Drainer 让 Checker 能要求本 Agent 持有的每一条连接停止接受新派发、等待
// 其在途工作结束，然后 Agent 才替换自己（设计文档一.6/二第 5 条）。这里声
// 明成接口而不是直接依赖 tunnel 包：tunnel 已经为了 AgentUpgrader 接口导
// 入了 agentupgrade，直接反向依赖会成环，用接口也能让测试用假实现替换。
// *tunnel.Manager 通过自己的 DrainAll 方法实现它。
type Drainer interface {
	// DrainAll stops every held connection from accepting new dispatch and
	// waits, up to timeout applied independently to each, for its
	// in-flight requests to reach zero.
	DrainAll(timeout time.Duration)
}

// execFunc replaces the calling process's image, the same shape as
// syscall.Exec (Unix-only — the design doc's section 3.3 excludes Windows
// from subtask 2's scope, since there is no equivalent syscall there). It
// is a struct field rather than a direct call so tests can observe an
// invocation instead of actually replacing the test binary's process
// image.
//
// execFunc 替换调用进程的镜像，与 syscall.Exec 同一个形状（仅限
// Unix——设计文档 3.3 节把 Windows 排除在子任务二范围之外，因为那里没有等
// 价的系统调用）。它是一个结构体字段而不是直接调用，好让测试观察到一次调
// 用，而不是真的替换测试二进制自己的进程镜像。
type execFunc func(argv0 string, argv []string, envv []string) error

// Checker compares a fixed local manifest against the Agent's own running
// version on demand, and — once EnableExecution and SetDrainer have been
// called — carries out ActionUpgrade's full chain. It is safe to call
// Trigger repeatedly and concurrently (from the tunnel Control stream's own
// goroutine); Trigger never blocks, and ActionUpgrade's work runs on its
// own goroutine so it cannot stall the Control stream.
//
// A Checker built by NewChecker alone, without EnableExecution, behaves
// exactly as subtask 1 shipped it: ActionUpgrade and ActionRollback both
// report StateFailed/ReasonNotImplemented. This keeps a bare Checker (e.g.
// one built in a test, or on a node with no signing key configured) safe by
// construction — it can never execute a downloaded binary unless
// EnableExecution was explicitly called with a work directory.
//
// Checker 按需把一份固定的本地清单与 Agent 自己的运行版本比较，并且——一旦
// 调用过 EnableExecution 与 SetDrainer——承担 ActionUpgrade 的完整链路。
// Trigger 可以被反复、并发调用（来自隧道 Control 流自己的 goroutine）；
// Trigger 从不阻塞，ActionUpgrade 的工作跑在自己的 goroutine 上，不会卡住
// Control 流。
//
// 一个只用 NewChecker 构造、没调用过 EnableExecution 的 Checker，行为与子
// 任务一交付时完全一致：ActionUpgrade 与 ActionRollback 都报告
// StateFailed/ReasonNotImplemented。这让一个裸 Checker（比如测试里构造
// 的，或者节点没配置签名公钥时）在构造上就是安全的——除非显式调用过带工作
// 目录的 EnableExecution，否则它永远无法执行任何下载到的二进制。
type Checker struct {
	currentVersion string
	versions       map[string]struct{}
	entries        map[string]Entry
	clock          runtime.Clock

	mu           sync.Mutex
	status       agentupgradestatus.Status
	workDir      string
	httpClient   *http.Client
	drainTimeout time.Duration
	execFn       execFunc
	drainer      Drainer
	// upgrading is true from the moment Trigger accepts an ActionUpgrade
	// until its background goroutine finishes, so a second trigger arriving
	// mid-upgrade is ignored rather than racing the first over the same
	// destination path.
	upgrading bool
}

// NewChecker builds a Checker over entries, comparing against
// currentVersion (the Agent's own main.version). A duplicate version string
// in entries collapses harmlessly to one known version, the same as a Go
// map literal would. Real UPGRADE execution stays disabled until
// EnableExecution is called.
//
// NewChecker 基于 entries 构造一个 Checker，比较对象是 currentVersion
// （Agent 自己的 main.version）。entries 里重复的版本号会无害地合并成一个已
// 知版本，与 Go map 字面量的行为一样。真正的 UPGRADE 执行在调用
// EnableExecution 之前保持关闭。
func NewChecker(entries []Entry, currentVersion string, clock runtime.Clock) *Checker {
	versions := make(map[string]struct{}, len(entries))
	byVersion := make(map[string]Entry, len(entries))
	for _, e := range entries {
		if e.Version == "" {
			continue
		}
		versions[e.Version] = struct{}{}
		byVersion[e.Version] = e
	}
	return &Checker{
		currentVersion: currentVersion,
		versions:       versions,
		entries:        byVersion,
		clock:          clock,
		drainTimeout:   defaultDrainTimeout,
		execFn:         syscall.Exec,
		status: agentupgradestatus.Status{
			CurrentVersion: currentVersion,
			State:          agentupgradestatus.StateIdle,
			UpdatedAt:      clock.Now(),
		},
	}
}

// EnableExecution turns on ActionUpgrade's real download/verify/exec chain.
// Until it is called, Trigger answers every ActionUpgrade with
// StateFailed/ReasonNotImplemented — see Checker's package doc for why this
// is a separate opt-in rather than always-on.
//
// workDir is where the downloaded binary is written and executed from; it
// must be non-empty to enable execution. httpClient fetches it, defaulting
// to http.DefaultClient when nil. drainTimeout bounds how long an upgrade
// waits for in-flight requests to drain before proceeding anyway;
// non-positive defaults to defaultDrainTimeout.
//
// EnableExecution 打开 ActionUpgrade 真正的下载/校验/exec 链路。在调用它
// 之前，Trigger 对每一次 ActionUpgrade 都回答
// StateFailed/ReasonNotImplemented——为什么这是一个单独的开关而不是默认打
// 开，见 Checker 的包文档。
//
// workDir 是下载二进制写入并执行的位置；必须非空才能打开执行。httpClient
// 用于获取它，nil 时默认为 http.DefaultClient。drainTimeout 限定一次升级
// 为等待在途请求排空最多等多久，超时后仍会继续；非正数时默认为
// defaultDrainTimeout。
func (c *Checker) EnableExecution(workDir string, httpClient *http.Client, drainTimeout time.Duration) {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if drainTimeout <= 0 {
		drainTimeout = defaultDrainTimeout
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.workDir = workDir
	c.httpClient = httpClient
	c.drainTimeout = drainTimeout
}

// SetDrainer installs the Drainer an UPGRADE drains before replacing this
// process. It is a separate call from EnableExecution because the Drainer
// (*tunnel.Manager) does not exist until after the tunnel connection table
// is constructed, which itself takes this Checker as a ClientConfig field —
// see main.go's startTunnel for the exact two-phase ordering this resolves.
// A Checker with no Drainer installed still executes an UPGRADE (skipping
// the drain step, logged as such by the caller if it wants), rather than
// failing outright — a missing Drainer is a startup-ordering bug, not a
// reason to refuse an operator-triggered upgrade.
//
// SetDrainer 安装一次 UPGRADE 在替换本进程之前要排空的 Drainer。它与
// EnableExecution 分开调用，是因为 Drainer（*tunnel.Manager）要到隧道连接
// 表构造完成之后才存在，而连接表本身又把这个 Checker 当作一个
// ClientConfig 字段——main.go 的 startTunnel 展示了这个两阶段构造具体如何
// 解决顺序问题。没有安装 Drainer 的 Checker 仍然会执行一次 UPGRADE（跳过排
// 空这一步，调用方如果需要可以自行记日志），而不是直接失败——缺少
// Drainer 是启动顺序上的缺陷，不该成为拒绝一次运维触发的升级的理由。
func (c *Checker) SetDrainer(d Drainer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.drainer = d
}

// Trigger asks the Checker to apply action against targetVersion.
//
// ActionCheck ignores targetVersion and scans the manifest for any known
// version other than currentVersion, reporting StateIdle either way (this
// package does not distinguish "checked, found an update" from "checked,
// found nothing" as separate States — a caller reads that distinction off
// Status.CurrentVersion vs. the manifest it already holds, the same
// separation of concerns Status's package doc describes for why it carries
// no source URL).
//
// ActionUpgrade, once EnableExecution has been called, resolves
// targetVersion against the manifest (ReasonUnknownVersion if it does not
// name a known entry, without any network request) and, if none is already
// running, starts the download/verify/drain/exec chain on its own
// goroutine. A second ActionUpgrade arriving while one is already in
// progress is silently ignored — the caller observes progress through the
// next status report, the same as any other trigger in this package never
// getting a dedicated ack frame. Before EnableExecution is called, every
// ActionUpgrade reports StateFailed/ReasonNotImplemented regardless of
// targetVersion, matching subtask 1's behavior.
//
// ActionRollback always reports StateFailed/ReasonNotImplemented — there is
// no execution path for it yet (design doc subtask three).
//
// Trigger 要求 Checker 对 targetVersion 施加 action。
//
// ActionCheck 忽略 targetVersion，扫描清单寻找任何不同于 currentVersion 的
// 已知版本，无论结果如何都报告 StateIdle（本包不把"检查后发现更新"和"检
// 查后一无所获"区分成两个不同的 State——调用方通过 Status.CurrentVersion
// 与自己已持有的清单对比来得出这个区别，与 Status 包文档说明它为什么不携带
// 来源 URL 是同一种关注点分离）。
//
// ActionUpgrade 在调用过 EnableExecution 之后，会把 targetVersion 对照清
// 单解析（不命中已知条目就报告 ReasonUnknownVersion，不发起任何网络请
// 求），如果当前没有正在进行的升级，就在自己的 goroutine 上开始下载/校
// 验/排空/exec 链路。一次已有升级进行中时到达的第二次 ActionUpgrade 会被
// 静默忽略——调用方通过下一次状态报告观察进度，与本包里其他触发从不设专
// 门 ack 帧是同一个道理。在调用 EnableExecution 之前，每一次 ActionUpgrade
// 都无视 targetVersion，报告 StateFailed/ReasonNotImplemented，与子任务一
// 的行为一致。
//
// ActionRollback 永远报告 StateFailed/ReasonNotImplemented——目前没有执行
// 路径（设计文档子任务三）。
func (c *Checker) Trigger(action agentupgradestatus.Action, targetVersion string) {
	switch action {
	case agentupgradestatus.ActionCheck:
		c.mu.Lock()
		c.status = agentupgradestatus.Status{
			CurrentVersion: c.currentVersion,
			State:          agentupgradestatus.StateIdle,
			UpdatedAt:      c.clock.Now(),
		}
		c.mu.Unlock()
	case agentupgradestatus.ActionUpgrade:
		c.triggerUpgrade(targetVersion)
	case agentupgradestatus.ActionRollback:
		c.setFailed(agentupgradestatus.ReasonNotImplemented)
	default:
		// ActionUnspecified or an unrecognized value: no-op, matching
		// ModelPullTrigger's "an empty/unrecognized trigger changes
		// nothing" precedent.
		//
		// ActionUnspecified 或未识别的取值：不做任何事，与 ModelPullTrigger
		// "空的/未识别的触发不改变任何东西"是同一先例。
	}
}

// triggerUpgrade validates targetVersion and, if execution is enabled and
// no upgrade is already running, starts runUpgrade on its own goroutine.
//
// triggerUpgrade 校验 targetVersion，如果执行已启用且当前没有正在运行的
// 升级，就在自己的 goroutine 上启动 runUpgrade。
func (c *Checker) triggerUpgrade(targetVersion string) {
	c.mu.Lock()
	entry, known := c.entries[targetVersion]
	executionEnabled := c.workDir != ""
	busy := c.upgrading

	switch {
	case !executionEnabled:
		c.status = agentupgradestatus.Status{
			CurrentVersion: c.currentVersion,
			State:          agentupgradestatus.StateFailed,
			Reason:         agentupgradestatus.ReasonNotImplemented,
			UpdatedAt:      c.clock.Now(),
		}
		c.mu.Unlock()
		return
	case !known:
		c.status = agentupgradestatus.Status{
			CurrentVersion: c.currentVersion,
			State:          agentupgradestatus.StateFailed,
			Reason:         agentupgradestatus.ReasonUnknownVersion,
			UpdatedAt:      c.clock.Now(),
		}
		c.mu.Unlock()
		return
	case busy:
		c.mu.Unlock()
		return
	}

	c.upgrading = true
	workDir, httpClient, drainTimeout, drainer, execFn := c.workDir, c.httpClient, c.drainTimeout, c.drainer, c.execFn
	c.status = agentupgradestatus.Status{
		CurrentVersion: c.currentVersion,
		State:          agentupgradestatus.StateDownloading,
		UpdatedAt:      c.clock.Now(),
	}
	c.mu.Unlock()

	go c.runUpgrade(entry, workDir, httpClient, drainTimeout, drainer, execFn)
}

// runUpgrade downloads entry's binary, verifies it, drains every held
// tunnel connection, and execs into it. It always clears c.upgrading on
// return — which, for a successful exec, never happens: the process image
// is gone.
//
// runUpgrade 下载 entry 的二进制、校验它、排空每一条持有的隧道连接，然后
// exec 进入它。它总是在返回时清除 c.upgrading——对于一次成功的 exec 来说，
// 这从不会发生：进程镜像已经不在了。
func (c *Checker) runUpgrade(entry Entry, workDir string, httpClient *http.Client, drainTimeout time.Duration, drainer Drainer, execFn execFunc) {
	defer func() {
		c.mu.Lock()
		c.upgrading = false
		c.mu.Unlock()
	}()

	destPath := filepath.Join(workDir, "agent-"+entry.Version)
	if err := download(context.Background(), httpClient, entry.SourceURL, destPath); err != nil {
		c.setFailed(agentupgradestatus.ReasonDownloadFailed)
		return
	}

	c.setState(agentupgradestatus.StateVerifying)
	sum, err := sha256File(destPath)
	if err != nil {
		os.Remove(destPath)
		c.setFailed(agentupgradestatus.ReasonDownloadFailed)
		return
	}
	if !strings.EqualFold(sum, entry.SHA256) {
		os.Remove(destPath)
		c.setFailed(agentupgradestatus.ReasonVerificationFailed)
		return
	}
	if err := os.Chmod(destPath, 0o755); err != nil {
		c.setFailed(agentupgradestatus.ReasonDownloadFailed)
		return
	}

	c.setState(agentupgradestatus.StateDraining)
	if drainer != nil {
		drainer.DrainAll(drainTimeout)
	}

	c.setState(agentupgradestatus.StateRestarting)
	if err := execFn(destPath, os.Args, os.Environ()); err != nil {
		c.setFailed(agentupgradestatus.ReasonExecFailed)
		return
	}
	// execFn only returns on failure: a successful syscall.Exec replaces
	// this process's image and never returns at all, so there is nothing
	// left to report here.
}

// setFailed records a StateFailed status with reason.
//
// setFailed 记录一个带 reason 的 StateFailed 状态。
func (c *Checker) setFailed(reason agentupgradestatus.FailureReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = agentupgradestatus.Status{
		CurrentVersion: c.currentVersion,
		State:          agentupgradestatus.StateFailed,
		Reason:         reason,
		UpdatedAt:      c.clock.Now(),
	}
}

// setState records a non-failure status with no reason.
//
// setState 记录一个不带 reason 的非失败状态。
func (c *Checker) setState(state agentupgradestatus.State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = agentupgradestatus.Status{
		CurrentVersion: c.currentVersion,
		State:          state,
		UpdatedAt:      c.clock.Now(),
	}
}

// HasUpdate reports whether the manifest known to the Checker names any
// version other than currentVersion. It is exported separately from Status
// so a caller (or a test) can ask the question without needing a State
// dedicated to the answer, matching Trigger's doc on why ActionCheck does
// not encode this in State.
//
// HasUpdate 报告 Checker 已知的清单是否点到任何不同于 currentVersion 的版
// 本。它与 Status 分开导出，让调用方（或测试）不需要一个专门的 State 就能
// 问出这个问题，与 Trigger 文档说明的"ActionCheck 为什么不把这个编码进
// State"是同一个理由。
func (c *Checker) HasUpdate() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for v := range c.versions {
		if v != c.currentVersion {
			return true
		}
	}
	return false
}

// Status returns the Checker's current status.
//
// Status 返回 Checker 当前的状态。
func (c *Checker) Status() agentupgradestatus.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// download fetches sourceURL into destPath via "<destPath>.part", verifying
// the result's SHA256 is unnecessary at this layer — the caller
// (runUpgrade) does that once the file is in place, so a caller comparing
// digests only ever reads a fully-written file, never a partial one. It
// atomically renames the part file into place on success, the same
// resumeless-but-atomic shape modelpull.pullOne uses for the final step (it
// does not need modelpull's own resume-from-partial-file logic: an Agent
// binary is downloaded rarely enough that restarting a failed fetch from
// scratch is an acceptable simplification the design doc's "no delta
// patching" stance already covers).
//
// download 把 sourceURL 的内容通过 "<destPath>.part" 拉取到 destPath；在
// 这一层校验结果的 SHA256 是不必要的——调用方（runUpgrade）会在文件落地之
// 后做这件事，因此一个比对摘要的调用方读到的永远是完整写入的文件，从不是
// 部分文件。成功时原子改名到位，与 modelpull.pullOne 最后一步同一种"不续
// 传但原子"的形状（它不需要 modelpull 自己的续传逻辑：Agent 二进制下载得
// 足够少，一次失败的获取直接从头重来是设计文档"不做增量补丁"立场本就覆盖
// 的简化）。
func download(ctx context.Context, client *http.Client, sourceURL, destPath string) error {
	partPath := destPath + ".part"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return fmt.Errorf("agentupgrade: build download request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("agentupgrade: fetch agent binary: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("agentupgrade: unexpected status %d fetching agent binary", resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(partPath), 0o755); err != nil {
		return fmt.Errorf("agentupgrade: create work directory: %w", err)
	}
	f, err := os.OpenFile(partPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("agentupgrade: open partial file: %w", err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(partPath)
		return fmt.Errorf("agentupgrade: download agent binary: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(partPath)
		return fmt.Errorf("agentupgrade: flush partial file: %w", err)
	}
	if err := os.Rename(partPath, destPath); err != nil {
		return fmt.Errorf("agentupgrade: finalize agent binary: %w", err)
	}
	return nil
}

// sha256File returns the hex-encoded SHA-256 digest of the file at path.
//
// sha256File 返回 path 处文件的十六进制编码 SHA-256 摘要。
func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

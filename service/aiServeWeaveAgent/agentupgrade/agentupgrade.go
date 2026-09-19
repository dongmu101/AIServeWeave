// Package agentupgrade is STATUS.md's P2 Agent auto-upgrade subtask one: a
// local manifest of known Agent versions, and a Checker that compares it
// against the Agent's own running version on demand. It is Agent-local and
// config-driven: the manifest comes from a local flag, not from the Gateway
// or control plane, the same restraint modelpull's manifest applies to
// model artifacts — an Action naming a version can only ever be answered
// against what this Agent already declared for itself.
//
// This subtask implements only the CHECK action. Trigger reports
// ReasonNotImplemented for ActionUpgrade and ActionRollback without
// touching the network or the local filesystem beyond the manifest already
// loaded at construction — downloading, signature verification, draining,
// and process replacement are later subtasks (see the design doc's subtask
// breakdown, docs/superpowers/specs/2026-09-19-p2-agent-auto-upgrade-design.md).
//
// agentupgrade 是 STATUS.md P2 Agent 自动升级子任务一：一份已知 Agent 版本
// 的本地清单，以及一个按需把它与 Agent 自己运行版本比较的 Checker。它是纯
// Agent 本地、配置驱动的：清单来自本地 flag，不来自 Gateway 或控制面，与
// modelpull 的清单对模型制品的同一种克制——一个指名版本的 Action 永远只能
// 针对本 Agent 自己已经声明过的东西来回答。
//
// 本子任务只实现 CHECK 动作。Trigger 对 ActionUpgrade 与 ActionRollback 一
// 律报告 ReasonNotImplemented，除了构造时已经加载的清单之外，不触碰网络或
// 本地文件系统——下载、签名校验、排空与进程替换留给后续子任务（子任务拆分
// 见设计文档
// docs/superpowers/specs/2026-09-19-p2-agent-auto-upgrade-design.md）。
package agentupgrade

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"AIServeWeave/common/agentupgradestatus"
	"AIServeWeave/common/runtime"
)

// Entry describes one known Agent version. SourceURL, SHA256, and Signature
// are recorded here so later subtasks can download and verify against them,
// but Checker itself (subtask 1) never reads any field but Version — it
// only needs to know which version strings are known, not how to fetch
// them.
//
// Entry 描述一个已知的 Agent 版本。SourceURL、SHA256 与 Signature 记录在这
// 里是为了让后续子任务可以据此下载并校验，但 Checker 本身（子任务一）除了
// Version 之外不读取任何其他字段——它只需要知道哪些版本号是已知的，不需要
// 知道怎么获取它们。
type Entry struct {
	Version string `json:"version"`
	// SourceURL is where a later subtask downloads this version's binary
	// from. Unused in subtask 1.
	//
	// SourceURL 是后续子任务下载这个版本二进制的地址。子任务一未使用。
	SourceURL string `json:"source_url,omitempty"`
	// SHA256 is the expected digest of the downloaded binary. Unused in
	// subtask 1.
	//
	// SHA256 是下载二进制的期望摘要。子任务一未使用。
	SHA256 string `json:"sha256,omitempty"`
	// Signature is the base64-encoded Ed25519 signature over the manifest
	// entry, verified against a public key compiled into the Agent binary.
	// Unused in subtask 1 — see the design doc's section 3.1 for why a
	// checksum alone is not enough for code that will be exec'd.
	//
	// Signature 是清单条目的 base64 编码 Ed25519 签名，校验对象是编译进
	// Agent 二进制的公钥。子任务一未使用——为什么校验和本身对将被 exec 的
	// 代码不够用，见设计文档 3.1 节。
	Signature string `json:"signature,omitempty"`
}

// LoadManifest reads a JSON-encoded []Entry from path.
//
// LoadManifest 从 path 读取 JSON 编码的 []Entry。
func LoadManifest(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("agentupgrade: read manifest: %w", err)
	}
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("agentupgrade: parse manifest %s: %w", path, err)
	}
	return entries, nil
}

// Checker compares a fixed local manifest against the Agent's own running
// version on demand. It is safe to call Trigger repeatedly and concurrently
// (from the tunnel Control stream's own goroutine).
//
// Checker 按需把一份固定的本地清单与 Agent 自己的运行版本比较。Trigger 可
// 以被反复、并发调用（来自隧道 Control 流自己的 goroutine）。
type Checker struct {
	currentVersion string
	versions       map[string]struct{}
	clock          runtime.Clock

	mu     sync.Mutex
	status agentupgradestatus.Status
}

// NewChecker builds a Checker over entries, comparing against
// currentVersion (the Agent's own main.version). A duplicate version string
// in entries collapses harmlessly to one known version, the same as a Go
// map literal would.
//
// NewChecker 基于 entries 构造一个 Checker，比较对象是 currentVersion
// （Agent 自己的 main.version）。entries 里重复的版本号会无害地合并成一个已
// 知版本，与 Go map 字面量的行为一样。
func NewChecker(entries []Entry, currentVersion string, clock runtime.Clock) *Checker {
	versions := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if e.Version == "" {
			continue
		}
		versions[e.Version] = struct{}{}
	}
	return &Checker{
		currentVersion: currentVersion,
		versions:       versions,
		clock:          clock,
		status: agentupgradestatus.Status{
			CurrentVersion: currentVersion,
			State:          agentupgradestatus.StateIdle,
			UpdatedAt:      clock.Now(),
		},
	}
}

// Trigger asks the Checker to apply action against targetVersion.
//
// ActionCheck ignores targetVersion and scans the manifest for any known
// version other than currentVersion, reporting StateIdle either way (this
// subtask does not distinguish "checked, found an update" from "checked,
// found nothing" as separate States — a caller reads that distinction off
// Status.CurrentVersion vs. the manifest it already holds, the same
// separation of concerns Status's package doc describes for why it carries
// no source URL).
//
// ActionUpgrade and ActionRollback both report StateFailed/
// ReasonNotImplemented for any targetVersion, known or not — subtask 1 has
// no execution path for either, so there is nothing an unknown-vs-known
// distinction would change about the answer.
//
// Trigger 要求 Checker 对 targetVersion 施加 action。
//
// ActionCheck 忽略 targetVersion，扫描清单寻找任何不同于 currentVersion 的
// 已知版本，无论结果如何都报告 StateIdle（本子任务不把"检查后发现更新"和
// "检查后一无所获"区分成两个不同的 State——调用方通过 Status.CurrentVersion
// 与自己已持有的清单对比来得出这个区别，与 Status 包文档说明它为什么不携带
// 来源 URL 是同一种关注点分离）。
//
// ActionUpgrade 与 ActionRollback 对任何 targetVersion（无论是否已知）都报
// 告 StateFailed/ReasonNotImplemented——子任务一对两者都没有执行路径，一个
// "已知/未知"的区分不会改变这个答案。
func (c *Checker) Trigger(action agentupgradestatus.Action, targetVersion string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch action {
	case agentupgradestatus.ActionCheck:
		c.status = agentupgradestatus.Status{
			CurrentVersion: c.currentVersion,
			State:          agentupgradestatus.StateIdle,
			UpdatedAt:      c.clock.Now(),
		}
	case agentupgradestatus.ActionUpgrade, agentupgradestatus.ActionRollback:
		c.status = agentupgradestatus.Status{
			CurrentVersion: c.currentVersion,
			State:          agentupgradestatus.StateFailed,
			Reason:         agentupgradestatus.ReasonNotImplemented,
			UpdatedAt:      c.clock.Now(),
		}
	default:
		// ActionUnspecified or an unrecognized value: no-op, matching
		// ModelPullTrigger's "an empty/unrecognized trigger changes
		// nothing" precedent.
		//
		// ActionUnspecified 或未识别的取值：不做任何事，与 ModelPullTrigger
		// "空的/未识别的触发不改变任何东西"是同一先例。
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

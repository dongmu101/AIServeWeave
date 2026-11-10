// Package identitystore implements the Registry's node-identity ledger: which
// public key a node_id was last issued a certificate for
// (RegisterRequest/RenewRequest in api/proto/tunnel/v1/tunnel.proto).
//
// Without this ledger, Register (internal/registryserver/identity.go) had no
// way to tell three cases apart: a node_id registering for the first time, a
// node re-presenting the same key it always has (a harmless reconnect), and a
// node_id reappearing with a different key — which is either an operator
// reinstalling that node, or someone else registering under a name that is
// not theirs. Recording the fingerprint of the key each node_id was last
// bound to is the minimum state needed to tell the first two apart from the
// third; distinguishing "reinstall" from "spoofing" within that third case
// needs an authorization signal this package's own comparison against
// history cannot produce. STATUS.md's S02 supplies that signal from outside
// this package instead: an operator minting a node_id-bound bootstrap token
// (tunnelv1.TokenAdmin.MintToken) is itself the authorization, so Register
// calls Set — unconditionally, bypassing Reserve's comparison — for a
// request that consumed one. A request that did not still goes through
// Reserve, and this package still treats a mismatch there as one bucket its
// caller must refuse; that path exists for whichever registration an
// operator has not (yet) chosen to authorize with a bound token.
//
// Like tokenstore, this assumes a single Registry process (Registry high
// availability is explicit later-phase work in the top-level README's
// roadmap) and gets its consistency from a mutex plus an atomically-written
// file rather than a distributed store.
//
// Package identitystore 实现 Registry 的节点身份账本：某个 node_id 最近一次被
// 签发证书时绑定的是哪把公钥（对应 api/proto/tunnel/v1/tunnel.proto 的
// RegisterRequest/RenewRequest）。
//
// 没有这份账本，Register（internal/registryserver/identity.go）就无法分辨三种
// 情形：一个 node_id 第一次注册、一个节点重新递交了它一直持有的同一把 key
// （无害的重连），以及一个 node_id 带着不同的 key 再次出现——这要么是运维在
// 重装那个节点，要么是有人在冒用一个不属于自己的名字注册。记录每个 node_id
// 最近绑定的公钥指纹，是分辨前两者与第三者所需的最小状态；在第三种情形内部
// 进一步分辨「重装」与「冒用」，需要一个本包自己与历史比对所产生不了的授权
// 信号。STATUS.md 的 S02 从本包之外补上了这个信号：运维铸造一枚 node_id
// 绑定的引导令牌（tunnelv1.TokenAdmin.MintToken）这件事本身就是授权，因此
// Register 对消耗了这种令牌的请求会调用 Set——无条件，跳过 Reserve 的比对。
// 没有消耗这种令牌的请求仍然走 Reserve，本包仍把那条路径上的一次不匹配归为
// 同一类，交给调用方一律拒绝；这条路径留给运维尚未（或选择不）用绑定令牌去
// 授权的那些注册。
//
// 与 tokenstore 一样，本包假定单个 Registry 进程（Registry 的高可用是顶层
// README 路线图中明确的后续阶段工作），其一致性来自一个互斥锁加一份原子写入的
// 文件，而不是分布式存储。
package identitystore

import (
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// ErrDisabled is returned by Reserve and Set for a node_id that is disabled at
// the moment of the write. Callers check IsDisabled earlier as well, but only
// this check — made under the same lock as the write — closes the window in
// which a concurrent Disable would otherwise be overwritten or ignored.
//
// ErrDisabled 由 Reserve 与 Set 返回，表示写入那一刻该 node_id 处于禁用状态。
// 调用方更早也会检查 IsDisabled，但只有这一次——与写入在同一把锁下完成——才能
// 堵住并发的 Disable 被覆盖或被忽略的那个窗口。
var ErrDisabled = errors.New("identitystore: node_id is disabled")

// Outcome reports what Reserve found for one node_id.
//
// Outcome 报告 Reserve 对某个 node_id 的检查结果。
type Outcome int

const (
	// OutcomeNew means this node_id had no prior record; it is now bound to
	// the given fingerprint.
	//
	// OutcomeNew 表示这个 node_id 此前没有记录；现在已绑定到给定的指纹。
	OutcomeNew Outcome = iota
	// OutcomeMatch means this node_id already carried the given fingerprint
	// — a harmless re-registration, most likely a node that never persisted
	// the certificate Register handed it the first time.
	//
	// OutcomeMatch 表示这个 node_id 此前已经绑定着给定的指纹——一次无害的
	// 重复注册，多半是某个节点从未保存下第一次 Register 交给它的证书。
	OutcomeMatch
	// OutcomeConflict means this node_id is bound to a different
	// fingerprint. The ledger is left unchanged: Reserve never lets a
	// conflicting request overwrite a prior binding, so the caller's
	// decision to refuse is not a race against a second refusal doing the
	// same overwrite.
	//
	// OutcomeConflict 表示这个 node_id 绑定着另一个指纹。账本保持不变：
	// Reserve 绝不允许一次冲突的请求覆盖此前的绑定，因此调用方拒绝该请求的
	// 决定，不会与另一次拒绝去做同一次覆盖发生竞争。
	OutcomeConflict
)

// record is one node_id's binding, as stored on disk.
type record struct {
	NodeID      string    `json:"node_id"`
	Fingerprint string    `json:"fingerprint"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	// Disabled marks a node_id revoked (STATUS.md's S03): Register and
	// RenewCertificate both refuse it. It lives on the same record as the
	// fingerprint binding rather than in a separate file because the two are
	// facets of one ledger entry per node_id, and a node_id can be disabled
	// before it has ever registered — in which case Fingerprint stays empty
	// until, if ever, it is enabled again and does register.
	Disabled bool `json:"disabled,omitempty"`
	// PendingApproval marks a node_id as having attempted registration with
	// an unbound bootstrap token without an operator's prior approval
	// (STATUS.md's P01), so the attempt surfaces in ListNodeStates. It is
	// informational: whether Register may sign is decided by Approved.
	//
	// PendingApproval 标记某个 node_id 曾在未经运维批准的情况下用未绑定的引导
	// 令牌尝试注册（STATUS.md 的 P01），好让这次尝试出现在 ListNodeStates 里。
	// 它只是信息性的：Register 能否签发由 Approved 决定。
	PendingApproval bool `json:"pending_approval,omitempty"`
	// Approved records that Register may sign for this node_id under an
	// unbound bootstrap token: set by Approve, or by a certificate actually
	// having been issued (Reserve/Set). Records created only to carry a
	// Disable or SetMaintenance flag leave it false, so those admin actions do
	// not approve a node_id as a side effect. A record written before this
	// field existed has no "approved" key; Open derives it as
	// !PendingApproval, which is exactly how such a record was judged before.
	//
	// Approved 记录在未绑定引导令牌下 Register 能否为这个 node_id 签发：由
	// Approve 设置，或在真正签发过证书时（Reserve/Set）设置。仅为承载 Disable 或
	// SetMaintenance 标记而创建的记录保持 false，这些管理动作因此不会顺带批准
	// node_id。此字段出现之前写入的记录没有 "approved" 键，Open 按
	// !PendingApproval 推导——这正是此前判断这类记录的方式。
	Approved bool `json:"approved"`
	// Maintenance marks a node_id as under operator-forced maintenance
	// (STATUS.md's P01): the Registry broadcasts it to every joined Gateway
	// replica the same way Disabled is, but it does not revoke the node_id's
	// identity — a replica stops assigning new work to it without closing
	// its existing Control stream.
	Maintenance bool `json:"maintenance,omitempty"`
	// Deleted marks a node_id an operator deleted (TokenAdmin.DeleteNode): it
	// is always Disabled as well, and States leaves it out. The record is
	// kept, rather than removed, so the node_id stays in the revoked set
	// while certificates issued before the delete are still valid.
	//
	// Deleted 标记运维删除的 node_id（TokenAdmin.DeleteNode）：它总是同时
	// Disabled，States 不再列出它。保留这条记录而不是移除它，是为了在删除之前
	// 签发的证书仍然有效期间，这个 node_id 继续留在吊销集合里。
	Deleted bool `json:"deleted,omitempty"`
}

// fileMode matches tokenstore's: this file is not a secret, but there is no
// reason to make it world-readable either.
const fileMode = 0o600

// snapshot is one immutable generation of the ledger. Once published through
// Store.snap it is never modified, so readers use it without any lock.
//
// snapshot 是账本的一个不可变世代。经 Store.snap 发布后就不再被修改，读者无需加锁即可使用。
type snapshot struct {
	records map[string]record
}

// Store is the on-disk node-identity ledger. The zero value is not usable;
// construct one with Open.
//
// Writers are serialized by wmu and follow copy, mutate, persist, publish: the
// change is applied to a private copy of the current snapshot, the copy is
// written to disk, and only after the write succeeds is it published through
// snap. A failed write therefore never advances the in-memory state, and
// readers (IsDisabled, IsApproved, HasRecord, States, ...) read the published
// snapshot without taking wmu, so they never wait on an fsync. Reserve, Set and
// the other writers still re-check the record they act on inside the wmu
// critical section, against the latest published snapshot, which is what keeps
// ErrDisabled airtight against a concurrent Disable.
//
// Store 是落盘的节点身份账本。零值不可用，须通过 Open 构造。
//
// 写者由 wmu 串行化，遵循「复制、修改、落盘、发布」：变更先作用在当前快照的私有副本上，
// 副本写盘成功后才通过 snap 发布。因此写盘失败绝不会让内存状态前进；读者（IsDisabled、
// IsApproved、HasRecord、States 等）不取 wmu，直接读已发布的快照，不会等待 fsync。
// Reserve、Set 等写者仍在 wmu 临界区内、基于最新发布的快照复核它们所作用的记录，
// 这正是 ErrDisabled 能与并发的 Disable 严丝合缝的原因。
type Store struct {
	path string

	// writeFile persists data; tests replace it through export_test.go to
	// block or fail a write.
	// writeFile 负责落盘；测试通过 export_test.go 替换它来阻塞或使写入失败。
	writeFile func(path string, data []byte, mode os.FileMode) error

	wmu  sync.Mutex // serializes writers / 串行化写者
	snap atomic.Pointer[snapshot]
}

// load returns the latest published snapshot.
// load 返回最新发布的快照。
func (s *Store) load() map[string]record { return s.snap.Load().records }

// Open loads path if it exists, or starts empty if it does not. path's
// parent directory is created if it does not exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path, writeFile: writeFileAtomic}
	records := make(map[string]record)
	s.snap.Store(&snapshot{records: records})
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("identitystore: cannot create %s: %w", filepath.Dir(path), err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, fmt.Errorf("identitystore: cannot read %s: %w", path, err)
	}
	if len(data) == 0 {
		return s, nil
	}
	var recs []*record
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("identitystore: cannot parse %s: %w", path, err)
	}
	// A second, narrow decode tells "approved": false apart from a missing
	// key, which only a ledger written before Approved existed lacks.
	// 第二次窄解码用来分辨 "approved": false 与键缺失——只有 Approved 出现前写入的账本才缺这个键。
	var presence []struct {
		Approved *bool `json:"approved"`
	}
	if err := json.Unmarshal(data, &presence); err != nil {
		return nil, fmt.Errorf("identitystore: cannot parse %s: %w", path, err)
	}
	for i, r := range recs {
		if presence[i].Approved == nil {
			r.Approved = !r.PendingApproval
		}
		records[r.NodeID] = *r
	}
	return s, nil
}

// Reserve checks nodeID against the ledger and, unless it conflicts, records
// fingerprint as its current binding. Register calls this after signing is
// otherwise ready to succeed but before the response is handed back, so a
// conflict costs the caller nothing it has not already spent on the consumed
// bootstrap token — see identity.go's Register for why that ordering is
// deliberate.
//
// A record with no fingerprint yet is treated the same as no record at all:
// Disable and Approve both create a record administratively, before a
// node_id has ever registered, and leave Fingerprint empty — the first real
// registration must bind it, not be refused as conflicting with an empty
// string nobody ever proved they hold.
//
// A node_id disabled at the moment of the call returns ErrDisabled and leaves
// the ledger unchanged; every other flag on an existing record is preserved.
//
// Reserve 检查 nodeID 相对于账本的状态，除非发生冲突，否则把 fingerprint
// 记录为它当前的绑定。Register 在签发已经准备就绪、只差把响应交回去之前调用
// 本方法，因此一次冲突不会让调用方额外损失什么——它在到这一步之前已经花掉的
// 只是那个引导令牌；为什么这个调用顺序是刻意的，见 identity.go 的 Register。
//
// 一条尚无指纹的记录，与完全没有记录同等对待：Disable 与 Approve 都会在一个
// node_id 从未注册过的情况下管理性地创建记录，且把 Fingerprint 留空——第一次
// 真正的注册必须绑定它，而不应被当作与一个从没人证明持有过的空字符串冲突而拒绝。
//
// 调用那一刻已被禁用的 node_id 返回 ErrDisabled，账本保持不变；已有记录上的
// 其他标记一律保留。
func (s *Store) Reserve(nodeID, fingerprint string, now time.Time) (Outcome, error) {
	var outcome Outcome
	err := s.update(func(next map[string]record) (bool, error) {
		r, ok := next[nodeID]
		if ok && r.Disabled {
			return false, ErrDisabled
		}
		if !ok {
			next[nodeID] = record{
				NodeID: nodeID, Fingerprint: fingerprint, FirstSeenAt: now, LastSeenAt: now, Approved: true,
			}
			outcome = OutcomeNew
			return true, nil
		}
		if r.Fingerprint != "" && r.Fingerprint != fingerprint {
			outcome = OutcomeConflict
			return false, nil
		}
		outcome = OutcomeMatch
		if r.Fingerprint == "" {
			outcome = OutcomeNew
		}
		// A certificate is being issued, so the node_id is approved and no longer
		// pending — including a pending mark a RecordPending racing ApproveNode
		// left on an already approved record.
		// 证书即将签发，node_id 因此已批准、不再待审批——包括 RecordPending 与
		// ApproveNode 竞争时留在已批准记录上的待审批标记。
		r.Fingerprint = fingerprint
		r.LastSeenAt = now
		r.Approved = true
		r.PendingApproval = false
		next[nodeID] = r
		return true, nil
	})
	if err != nil {
		return 0, err
	}
	return outcome, nil
}

// Set unconditionally records nodeID's current fingerprint, for a caller
// (RenewCertificate) whose own authentication already proved the request
// speaks for nodeID via possession of its current private key — no
// comparison against history is needed, and a renewed certificate is
// routinely issued for a freshly generated key, which Reserve would
// otherwise refuse as a conflict.
//
// Set 无条件记录 nodeID 当前的指纹，供一个（RenewCertificate）调用方使用——
// 它自己的认证已经通过持有 nodeID 当前私钥证明了这次请求确实代表 nodeID，
// 因此无需与历史比对；而一次续期常规地会为一把新生成的 key 签发证书，若用
// Reserve 处理，反而会被当作冲突拒绝。
//
// Set only rebinds the fingerprint: Maintenance survives it, and a certificate
// having been issued marks the node_id approved and no longer pending. A
// node_id disabled at the moment of the call returns ErrDisabled and leaves
// the ledger unchanged, so a renewal racing DisableNode cannot erase the
// disable.
//
// Set 只重新绑定指纹：Maintenance 标记原样保留；既然证书已经签发，node_id 随之
// 标记为已批准、不再待审批。调用那一刻已被禁用的 node_id 返回 ErrDisabled，
// 账本保持不变——与 DisableNode 竞争的续期因此抹不掉那次禁用。
func (s *Store) Set(nodeID, fingerprint string, now time.Time) error {
	return s.update(func(next map[string]record) (bool, error) {
		r, ok := next[nodeID]
		if ok && r.Disabled {
			return false, ErrDisabled
		}
		if !ok {
			r = record{NodeID: nodeID, FirstSeenAt: now}
		}
		r.Fingerprint = fingerprint
		r.LastSeenAt = now
		r.Approved = true
		r.PendingApproval = false
		next[nodeID] = r
		return true, nil
	})
}

// IsDisabled reports whether nodeID is currently disabled (STATUS.md's S03).
// A node_id with no record at all is not disabled — Disable always creates
// one, so the absence of a record means nobody ever disabled this node_id.
func (s *Store) IsDisabled(nodeID string) bool {
	r, ok := s.load()[nodeID]
	return ok && r.Disabled
}

// Disable marks nodeID revoked, creating a record for it if none exists yet
// — an operator may disable a node_id pre-emptively, before it has ever
// registered, to block a bootstrap token that leaked before it was consumed.
// It is idempotent: disabling an already-disabled node_id succeeds without
// changing anything observable.
func (s *Store) Disable(nodeID string, now time.Time) error {
	return s.update(func(next map[string]record) (bool, error) {
		r, ok := next[nodeID]
		if !ok {
			r = record{NodeID: nodeID, FirstSeenAt: now}
		}
		r.Disabled = true
		r.LastSeenAt = now
		next[nodeID] = r
		return true, nil
	})
}

// Enable clears a prior Disable, and a prior Delete with it, so the node_id
// is listed by States again. Enabling a node_id that is not currently
// disabled, including one with no record at all, is not an error — both
// already guarantee the node_id is not revoked.
//
// Enable 清除此前的 Disable，并一并清除此前的 Delete，使 States 重新列出该
// node_id。启用一个当前未被禁用的 node_id（包括完全没有记录的）不是错误——两者
// 都已经保证该 node_id 没有被吊销。
func (s *Store) Enable(nodeID string, now time.Time) error {
	return s.update(func(next map[string]record) (bool, error) {
		r, ok := next[nodeID]
		if !ok || !r.Disabled {
			return false, nil
		}
		r.Disabled, r.Deleted = false, false
		r.LastSeenAt = now
		next[nodeID] = r
		return true, nil
	})
}

// Delete disables nodeID and hides it from States, creating a record for it
// if none exists yet, the same way Disable may revoke a node_id that has
// never registered. Pending approval and maintenance are cleared: a deleted
// node_id is neither waiting on an operator nor taking work. Deleting a
// node_id that is already deleted succeeds without changing anything
// observable.
//
// Delete 禁用 nodeID 并让 States 不再列出它；没有记录时新建一条，与 Disable
// 可以吊销从未注册过的 node_id 一样。待审批与维护标记一并清除：已删除的
// node_id 既不在等待运维，也不再接活。删除一个已删除的 node_id 会成功，且不改变
// 任何可观察的状态。
func (s *Store) Delete(nodeID string, now time.Time) error {
	return s.update(func(next map[string]record) (bool, error) {
		r, ok := next[nodeID]
		if ok && r.Deleted {
			return false, nil
		}
		if !ok {
			r = record{NodeID: nodeID, FirstSeenAt: now}
		}
		r.Disabled, r.Deleted = true, true
		r.PendingApproval, r.Maintenance = false, false
		r.LastSeenAt = now
		next[nodeID] = r
		return true, nil
	})
}

// DisabledNodeIDs returns every currently disabled node_id, in no particular
// order. The Registry uses it to seed a freshly started process's revoked set
// before the first GatewayRoster broadcast, so a Gateway replica that joins
// right after a restart still learns about nodes disabled in a prior run.
func (s *Store) DisabledNodeIDs() []string {
	ids := make([]string, 0)
	for id, r := range s.load() {
		if r.Disabled {
			ids = append(ids, id)
		}
	}
	return ids
}

// IsPending reports whether nodeID has attempted registration and is waiting
// on operator approval (STATUS.md's P01). It describes the queue
// ListNodeStates shows; whether Register may sign is IsApproved's question.
//
// IsPending 报告 nodeID 是否尝试过注册、正在等待运维批准（STATUS.md 的 P01）。
// 它描述的是 ListNodeStates 展示的那个队列；Register 能否签发由 IsApproved 回答。
func (s *Store) IsPending(nodeID string) bool {
	r, ok := s.load()[nodeID]
	return ok && r.PendingApproval
}

// HasRecord reports whether nodeID has any ledger entry at all. Having one
// does not imply approval — Disable and SetMaintenance create records too;
// Register asks IsApproved instead.
//
// HasRecord 报告 nodeID 在账本里是否有任何条目。有条目并不意味着已批准——
// Disable 与 SetMaintenance 同样会建记录；Register 改问 IsApproved。
func (s *Store) HasRecord(nodeID string) bool {
	_, ok := s.load()[nodeID]
	return ok
}

// IsApproved reports whether Register may sign for nodeID under an unbound
// bootstrap token (STATUS.md's P01): an operator approved it, or it was
// already issued a certificate. A node_id with no record is not approved.
//
// IsApproved 报告在未绑定引导令牌下 Register 能否为 nodeID 签发（STATUS.md 的
// P01）：运维批准过它，或它已经被签发过证书。没有记录的 node_id 视为未批准。
func (s *Store) IsApproved(nodeID string) bool {
	r, ok := s.load()[nodeID]
	return ok && r.Approved
}

// RecordPending upserts nodeID's ledger entry as pending approval, so an
// operator reviewing the pending queue (ListNodeStates) can see the attempt.
// It deliberately does not touch Fingerprint: the Agent generates a fresh
// key on every bootstrap attempt (tunnel.IdentityManager.bootstrap), so
// binding the CSR's fingerprint here would make the retry that follows
// approval collide with it as a conflict (Reserve) instead of succeeding —
// exactly the retry this gate exists to eventually let through. It does not
// clear an existing Disabled or Maintenance flag either: those are
// independent facets Register's caller already checks before ever reaching
// the approval gate.
func (s *Store) RecordPending(nodeID string, now time.Time) error {
	return s.update(func(next map[string]record) (bool, error) {
		r, ok := next[nodeID]
		if ok && r.Approved {
			// ApproveNode won a race with the caller's IsApproved check; marking
			// the record pending now would contradict the approval.
			// ApproveNode 抢在了调用方的 IsApproved 检查之后；此时再标记待审批会与批准矛盾。
			return false, nil
		}
		if !ok {
			r = record{NodeID: nodeID, FirstSeenAt: now}
		}
		r.PendingApproval = true
		r.LastSeenAt = now
		next[nodeID] = r
		return true, nil
	})
}

// Approve clears nodeID's pending-approval mark, creating a record for it if
// none exists yet — an operator may approve a node_id before it has ever
// attempted to register, the same way Disable may pre-emptively revoke one.
// Approving a node_id that is not currently pending, including one with no
// record at all, is not an error.
func (s *Store) Approve(nodeID string, now time.Time) error {
	return s.update(func(next map[string]record) (bool, error) {
		r, ok := next[nodeID]
		if !ok {
			r = record{NodeID: nodeID, FirstSeenAt: now}
		}
		r.PendingApproval = false
		r.Approved = true
		r.LastSeenAt = now
		next[nodeID] = r
		return true, nil
	})
}

// SetMaintenance marks nodeID under operator-forced maintenance
// (STATUS.md's P01), creating a record for it if none exists yet. Setting
// maintenance on a node_id that is already under it is not an error.
func (s *Store) SetMaintenance(nodeID string, now time.Time) error {
	return s.update(func(next map[string]record) (bool, error) {
		r, ok := next[nodeID]
		if !ok {
			r = record{NodeID: nodeID, FirstSeenAt: now}
		}
		r.Maintenance = true
		r.LastSeenAt = now
		next[nodeID] = r
		return true, nil
	})
}

// ClearMaintenance clears a prior SetMaintenance. Clearing maintenance on a
// node_id that is not currently under it, including one with no record at
// all, is not an error.
func (s *Store) ClearMaintenance(nodeID string, now time.Time) error {
	return s.update(func(next map[string]record) (bool, error) {
		r, ok := next[nodeID]
		if !ok || !r.Maintenance {
			return false, nil
		}
		r.Maintenance = false
		r.LastSeenAt = now
		next[nodeID] = r
		return true, nil
	})
}

// MaintenanceNodeIDs returns every node_id currently under maintenance, in no
// particular order, the same way DisabledNodeIDs does for disabled node_ids.
func (s *Store) MaintenanceNodeIDs() []string {
	ids := make([]string, 0)
	for id, r := range s.load() {
		if r.Maintenance {
			ids = append(ids, id)
		}
	}
	return ids
}

// State is one node_id's ledger entry as States reports it — the same shape
// TokenAdmin.ListNodeStates exposes over the wire, kept in this package so
// registryserver does not have to reach into record's unexported fields.
type State struct {
	NodeID          string
	PendingApproval bool
	Disabled        bool
	Maintenance     bool
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
	// KeyFingerprint is Fingerprint of the key the node registered or last
	// renewed with; empty before its first registration.
	//
	// KeyFingerprint 是节点注册或最近一次续期所用密钥的 Fingerprint；首次注册前为空。
	KeyFingerprint string
}

// States returns every node_id the ledger has an opinion about, in no
// particular order, except those an operator deleted.
//
// States 返回账本里有记录的每一个 node_id（顺序不定），运维已删除的除外。
func (s *Store) States() []State {
	records := s.load()
	states := make([]State, 0, len(records))
	for _, r := range records {
		if r.Deleted {
			continue // revoked but hidden / 仍吊销，但不再列出
		}
		states = append(states, State{
			NodeID:          r.NodeID,
			PendingApproval: r.PendingApproval,
			Disabled:        r.Disabled,
			Maintenance:     r.Maintenance,
			FirstSeenAt:     r.FirstSeenAt,
			LastSeenAt:      r.LastSeenAt,
			KeyFingerprint:  r.Fingerprint,
		})
	}
	return states
}

// update is the single write path. It serializes writers on wmu, hands mutate a
// private copy of the latest published snapshot, and, if mutate reports a
// change, writes the copy to disk and only then publishes it. mutate must only
// touch the map it is given. If mutate returns an error, or persisting fails,
// nothing is published, so the in-memory ledger never gets ahead of the file.
//
// update 是唯一的写路径。它在 wmu 上串行化写者，把最新发布快照的私有副本交给 mutate；
// 若 mutate 报告有变更，先把副本写盘，成功后才发布。mutate 只能改动交给它的这张 map。
// mutate 返回错误或落盘失败时都不会发布任何东西，内存账本因此不会领先于文件。
func (s *Store) update(mutate func(next map[string]record) (changed bool, err error)) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()

	next := maps.Clone(s.load())
	changed, err := mutate(next)
	if err != nil || !changed {
		return err
	}
	if err := s.persist(next); err != nil {
		return err
	}
	s.snap.Store(&snapshot{records: next})
	return nil
}

// persist writes every record to s.path atomically. It touches no Store state
// and needs no lock; update calls it with wmu held so writes never interleave.
//
// persist 把所有记录原子地写入 s.path。它不触碰 Store 状态、无需加锁；update 在持有
// wmu 时调用它，写入因此不会交错。
func (s *Store) persist(records map[string]record) error {
	recs := make([]record, 0, len(records))
	for _, r := range records {
		recs = append(recs, r)
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return fmt.Errorf("identitystore: cannot encode identity file: %w", err)
	}
	return s.writeFile(s.path, data, fileMode)
}

// Fingerprint returns a stable, opaque identifier for pub: the hex-encoded
// SHA-256 of its DER-encoded SubjectPublicKeyInfo. Two CSRs presenting the
// same key produce the same fingerprint regardless of how the CSR itself was
// re-serialized, which is the property Reserve's comparison depends on.
//
// Fingerprint 返回 pub 的一个稳定、不透明的标识：其 DER 编码
// SubjectPublicKeyInfo 的十六进制 SHA-256。两份呈递同一把 key 的 CSR，无论
// CSR 本身如何被重新序列化，都会得到相同的指纹——Reserve 的比较正依赖这条
// 性质。
func Fingerprint(pub crypto.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return "", fmt.Errorf("identitystore: cannot encode public key: %w", err)
	}
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:]), nil
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory, so a reader never observes a partially written identity file —
// the same helper tokenstore.go and ca.go each keep their own copy of, per
// this package's small size not justifying a shared internal dependency.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeded

	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir fsyncs dir so the rename that just replaced the ledger file survives
// a crash or power loss: without it the new directory entry may still be only
// in the page cache and the file can revert to its previous contents on reboot.
// Windows cannot fsync a directory handle, and the rename there is already
// journaled by NTFS, so it is skipped.
//
// syncDir 对目录 fsync，使刚刚替换账本文件的 rename 能挺过崩溃或掉电：否则新的目录项
// 可能仍只在页缓存里，重启后文件会退回到旧内容。Windows 不能对目录句柄 fsync，且 NTFS
// 已对该 rename 记日志，因此跳过。
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return err
	}
	return d.Close()
}

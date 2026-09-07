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
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

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
}

// fileMode matches tokenstore's: this file is not a secret, but there is no
// reason to make it world-readable either.
const fileMode = 0o600

// Store is the on-disk, mutex-guarded node-identity ledger. The zero value is
// not usable; construct one with Open.
type Store struct {
	path string

	mu      sync.Mutex
	records map[string]*record
}

// Open loads path if it exists, or starts empty if it does not. path's
// parent directory is created if it does not exist.
func Open(path string) (*Store, error) {
	s := &Store{path: path, records: make(map[string]*record)}
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
	for _, r := range recs {
		s.records[r.NodeID] = r
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
// Reserve 检查 nodeID 相对于账本的状态，除非发生冲突，否则把 fingerprint
// 记录为它当前的绑定。Register 在签发已经准备就绪、只差把响应交回去之前调用
// 本方法，因此一次冲突不会让调用方额外损失什么——它在到这一步之前已经花掉的
// 只是那个引导令牌；为什么这个调用顺序是刻意的，见 identity.go 的 Register。
func (s *Store) Reserve(nodeID, fingerprint string, now time.Time) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.records[nodeID]
	if !ok {
		s.records[nodeID] = &record{
			NodeID: nodeID, Fingerprint: fingerprint, FirstSeenAt: now, LastSeenAt: now,
		}
		if err := s.persistLocked(); err != nil {
			delete(s.records, nodeID)
			return 0, err
		}
		return OutcomeNew, nil
	}
	if r.Fingerprint != fingerprint {
		return OutcomeConflict, nil
	}
	prevSeen := r.LastSeenAt
	r.LastSeenAt = now
	if err := s.persistLocked(); err != nil {
		r.LastSeenAt = prevSeen
		return 0, err
	}
	return OutcomeMatch, nil
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
func (s *Store) Set(nodeID, fingerprint string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	firstSeen := now
	if prev, ok := s.records[nodeID]; ok {
		firstSeen = prev.FirstSeenAt
	}
	prev := s.records[nodeID]
	s.records[nodeID] = &record{
		NodeID: nodeID, Fingerprint: fingerprint, FirstSeenAt: firstSeen, LastSeenAt: now,
	}
	if err := s.persistLocked(); err != nil {
		if prev != nil {
			s.records[nodeID] = prev
		} else {
			delete(s.records, nodeID)
		}
		return err
	}
	return nil
}

// IsDisabled reports whether nodeID is currently disabled (STATUS.md's S03).
// A node_id with no record at all is not disabled — Disable always creates
// one, so the absence of a record means nobody ever disabled this node_id.
func (s *Store) IsDisabled(nodeID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[nodeID]
	return ok && r.Disabled
}

// Disable marks nodeID revoked, creating a record for it if none exists yet
// — an operator may disable a node_id pre-emptively, before it has ever
// registered, to block a bootstrap token that leaked before it was consumed.
// It is idempotent: disabling an already-disabled node_id succeeds without
// changing anything observable.
func (s *Store) Disable(nodeID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.records[nodeID]
	if !ok {
		r = &record{NodeID: nodeID, FirstSeenAt: now}
		s.records[nodeID] = r
	}
	prevDisabled, prevSeen := r.Disabled, r.LastSeenAt
	r.Disabled = true
	r.LastSeenAt = now
	if err := s.persistLocked(); err != nil {
		if !ok {
			delete(s.records, nodeID)
		} else {
			r.Disabled, r.LastSeenAt = prevDisabled, prevSeen
		}
		return err
	}
	return nil
}

// Enable clears a prior Disable. Enabling a node_id that is not currently
// disabled, including one with no record at all, is not an error — both
// already guarantee the node_id is not revoked.
func (s *Store) Enable(nodeID string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.records[nodeID]
	if !ok || !r.Disabled {
		return nil
	}
	prevSeen := r.LastSeenAt
	r.Disabled = false
	r.LastSeenAt = now
	if err := s.persistLocked(); err != nil {
		r.Disabled, r.LastSeenAt = true, prevSeen
		return err
	}
	return nil
}

// DisabledNodeIDs returns every currently disabled node_id, in no particular
// order. The Registry uses it to seed a freshly started process's revoked set
// before the first GatewayRoster broadcast, so a Gateway replica that joins
// right after a restart still learns about nodes disabled in a prior run.
func (s *Store) DisabledNodeIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0)
	for id, r := range s.records {
		if r.Disabled {
			ids = append(ids, id)
		}
	}
	return ids
}

// persistLocked writes every record to s.path atomically. Callers must hold
// s.mu.
func (s *Store) persistLocked() error {
	recs := make([]*record, 0, len(s.records))
	for _, r := range s.records {
		recs = append(recs, r)
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return fmt.Errorf("identitystore: cannot encode identity file: %w", err)
	}
	return writeFileAtomic(s.path, data, fileMode)
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
	return os.Rename(tmpName, path)
}

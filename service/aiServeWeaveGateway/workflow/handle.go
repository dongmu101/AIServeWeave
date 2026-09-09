package workflow

import "sync/atomic"

// Handle holds the currently active Registry behind an atomic pointer, so a
// registry loaded once from files and a registry hot-swapped by a
// control-plane sync (P03's workflowsync) go through the exact same read
// path: a request handler never needs to know which source is behind it, and
// a swap in progress is never observed half-applied.
//
// Handle 用一个原子指针持有当前生效的 Registry，这样一次性从文件加载的目录，与
// 由控制面同步（P03 的 workflowsync）热替换的目录，走的是完全同一条读取路径：
// 请求处理方从不需要知道背后是哪种来源，进行中的替换也不会被观察到只换了一半。
type Handle struct {
	p atomic.Pointer[Registry]
}

// NewHandle wraps an already-built Registry. The file source uses this: the
// registry is set once at startup and never replaced, so the atomic pointer
// exists only to share one read path with the control-plane source.
//
// NewHandle 包装一个已经构建好的 Registry。文件来源用它：目录在启动时设置一次，
// 此后从不替换，原子指针的存在只是为了与控制面来源共享同一条读取路径。
func NewHandle(reg *Registry) *Handle {
	h := &Handle{}
	h.Store(reg)
	return h
}

// NewEmptyHandle returns a Handle with nothing stored yet. The
// control-plane source uses this: it hands the Handle's Store method to a
// syncer as the activation callback before that syncer has pulled anything,
// so a read that lands before the first activation reaches an empty
// Registry — see Registry's nil-receiver methods — rather than a nil
// pointer.
//
// NewEmptyHandle 返回一个尚未存入任何内容的 Handle。控制面来源用它：在同步器
// 拉取任何东西之前，就把 Handle 的 Store 方法作为启用回调交给它，因此一次落在
// 首次启用之前的读取，触达的是一个空 Registry（见 Registry 的 nil 接收者方法），
// 而不是一个 nil 指针。
func NewEmptyHandle() *Handle { return NewHandle(&Registry{byID: make(map[string]*Template)}) }

// Store atomically replaces the active registry.
//
// Store 原子替换当前生效的目录。
func (h *Handle) Store(reg *Registry) { h.p.Store(reg) }

// Load returns the currently active registry.
//
// Load 返回当前生效的目录。
func (h *Handle) Load() *Registry { return h.p.Load() }

// Lookup returns the template registered under id in the currently active
// registry.
//
// Lookup 返回当前生效目录中以 id 注册的模板。
func (h *Handle) Lookup(id string) (*Template, bool) { return h.Load().Lookup(id) }

// IDs returns every registered template id in the currently active registry,
// sorted.
//
// IDs 返回当前生效目录中所有已注册模板 id，已排序。
func (h *Handle) IDs() []string { return h.Load().IDs() }

// Len is how many templates are registered in the currently active registry.
//
// Len 是当前生效目录中已注册模板的数量。
func (h *Handle) Len() int { return h.Load().Len() }

// BundleDigest fingerprints the currently active registry; see
// Registry.BundleDigest.
//
// BundleDigest 为当前生效目录取指纹；见 Registry.BundleDigest。
func (h *Handle) BundleDigest() (string, error) { return h.Load().BundleDigest() }

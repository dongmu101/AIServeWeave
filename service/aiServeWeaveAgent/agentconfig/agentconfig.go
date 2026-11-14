// Package agentconfig is the on-disk shape of -config: the two settings an
// operator needs most often to bring a new node online — which local
// inference backends to register, and how to reach the remote Gateway —
// expressed as YAML for low editing friction. It is intentionally narrow:
// STATUS.md's later Agent-local features (model pull, ComfyUI Managed,
// Agent auto-upgrade) each already have their own flag-driven manifest and
// stay that way here; folding them into this file too is a separate
// decision for whoever picks up "运行时配置文件加载" next, not something
// this package should pre-empt.
//
// Connection and discovery fields map onto -gateway/-registry/... flags documented
// in main.go; a flag explicitly passed on the command line always wins over
// the same setting from this file, so an operator can still override one
// value without editing the file (see main.go's applyConfig). YAML is
// decoded into Config's Go field names via viper (github.com/spf13/viper),
// using the same "yaml" struct tags a plain YAML decoder would — unlike the
// rest of this codebase's local manifests (modelpull.Spec,
// agentupgrade.Entry), which are pure JSON read by non-human tooling, this
// file's whole reason to exist is to be hand-edited or produced by the
// configui package's local setup page, so YAML alone is the on-disk
// contract; nothing here is ever sent to or accepted from the Gateway or
// control plane. viper is a deliberate, recorded exception to the Agent's
// otherwise minimal dependency footprint — see docs/agents/dependencies.md
// for why.
//
// agentconfig 是 -config 的落盘契约：运维把新节点接进来最常需要的两件事——
// 声明哪些本地推理后端、以及怎么联系远程 Gateway——用 YAML 表达以降低编辑
// 门槛。它刻意收得很窄：STATUS.md 后续的 Agent 本地功能（模型分发、
// ComfyUI Managed、Agent 自动升级）各自已经有自己的 flag 驱动清单，在这里
// 保持不变；要不要把它们也并进这份配置文件，留给后续接手"运行时配置文件加
// 载"的人决定，这个包不代为决定。
//
// 连接与发现字段对应 main.go 里既有的 -gateway/-registry/... flag；命令行显
// 式传入的 flag 永远优先于这份文件里的同名设置，运维仍可以只在命令行覆盖
// 一个值而不用改文件（见 main.go 的 applyConfig）。YAML 经由 viper
// （github.com/spf13/viper）解码进 Config 的 Go 字段，标签沿用普通 YAML 解
// 码器会用的同一套 "yaml" 结构体标签——与本代码库其余本地清单（modelpull.
// Spec、agentupgrade.Entry）不同，那些是给非人工工具读的纯 JSON，而这份文
// 件存在的全部意义就是给人手改、或者由 configui 包的本地设置页面生成，因
// 此 YAML 就是唯一的落盘约定；这里的任何字段都从不发给、也从不接受
// Gateway 或控制面下发。viper 是 Agent 本来克制的依赖footprint 上一次刻意、
// 记录在案的例外——理由见 docs/agents/dependencies.md。
package agentconfig

import (
	"AIServeWeave/service/aiServeWeaveAgent/attachments"
	"AIServeWeave/service/aiServeWeaveAgent/internal/localstate"
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// structTagName is passed to viper.Unmarshal so it decodes using Config's
// existing "yaml" struct tags instead of adding a second "mapstructure" tag
// vocabulary to every field purely to satisfy viper's own default.
//
// structTagName 传给 viper.Unmarshal，让它复用 Config 已有的 "yaml" 结构体
// 标签解码，而不是单为满足 viper 自己的默认值再给每个字段加一套
// "mapstructure" 标签。
const structTagName = "yaml"

// Config is the top-level shape of a -config file.
//
// Config 是 -config 文件的顶层结构。
type Config struct {
	// Attachments binds local conversion tools; changes require an Agent restart.
	// Attachments 绑定本地附件转换工具，修改后需要重启 Agent。
	Attachments attachments.Config `yaml:"attachments,omitempty"`
	// Gateway holds every setting needed to reach and authenticate to the
	// remote Gateway, mirroring tunnelOptions in main.go.
	//
	// Gateway 保存联系并认证远程 Gateway 所需的全部设置，对应 main.go 里的
	// tunnelOptions。
	Gateway GatewayConfig `yaml:"gateway"`

	// Runtimes declares local inference backends to register at startup,
	// in addition to whatever -ollama-url/-ollama-id and auto-discovery
	// already add. Unlike those two, a declared runtime here may be any
	// kind common/runtime's registry knows (local servers, local CLIs and
	// hosted providers; see common/runtime.Kind).
	//
	// Runtimes 声明启动时要注册的本地推理后端，是 -ollama-url/-ollama-id
	// 与自动发现之外的补充。与那两者不同，这里声明的运行时可以是
	// common/runtime 注册表认识的任意种类（本地服务、本机 CLI 与托管供应商，
	// 见 common/runtime.Kind）。
	Runtimes []RuntimeConfig `yaml:"runtimes,omitempty"`

	// AutoDiscover overrides -auto-discover when set. A nil value leaves
	// the flag's own value (default true) untouched.
	//
	// AutoDiscover 在被设置时覆盖 -auto-discover；为 nil 时保留 flag 自己
	// 的值（默认 true）不变。
	AutoDiscover *bool `yaml:"auto_discover,omitempty"`

	// AutoDiscoverInterval overrides -auto-discover-interval when
	// non-empty, parsed with time.ParseDuration (e.g. "30s", "2m").
	//
	// AutoDiscoverInterval 在非空时覆盖 -auto-discover-interval，用
	// time.ParseDuration 解析（如 "30s"、"2m"）。
	AutoDiscoverInterval string `yaml:"auto_discover_interval,omitempty"`

	// MetricsAddr overrides -metrics-addr when set. An empty string is a
	// meaningful value (disables the metrics endpoint), so this is a
	// pointer rather than a plain string: nil means "leave the flag
	// alone", "" means "the file explicitly disables metrics".
	//
	// MetricsAddr 在被设置时覆盖 -metrics-addr。空字符串是一个有意义的值
	// （关闭指标端点），因此这里用指针而不是普通字符串：nil 表示"不动
	// flag"，"" 表示"这份文件显式关闭指标"。
	MetricsAddr *string `yaml:"metrics_addr,omitempty"`

	// LogLevel overrides -log-level when non-empty.
	//
	// LogLevel 在非空时覆盖 -log-level。
	LogLevel string `yaml:"log_level,omitempty"`

	// Admission configures node-wide admission (resource-pressure gate and
	// local semaphores); each set field overrides the matching -admission-*
	// flag. Changes require an Agent restart.
	//
	// Admission 配置整机准入（资源紧张闸门与本地信号量）；每个已设置的字段覆盖对应的
	// -admission-* flag。修改后需要重启 Agent。
	Admission AdmissionConfig `yaml:"admission,omitempty"`
}

// AdmissionConfig mirrors the -admission-* flags. A zero field leaves its
// flag alone, so a duration is a string: "0s" explicitly sets zero (for
// queue_max_wait, refuse at once) while an absent value keeps the flag.
//
// AdmissionConfig 对应 -admission-* flag。字段为零值时不改动对应 flag，因此时长用字符串
// 表示："0s" 明确设为零（对 queue_max_wait 即立刻拒绝），缺席则保留 flag 的值。
type AdmissionConfig struct {
	SampleInterval    string  `yaml:"sample_interval,omitempty"`
	CPUHighPercent    float64 `yaml:"cpu_high_percent,omitempty"`
	MemoryLowPercent  float64 `yaml:"memory_low_percent,omitempty"`
	HysteresisPercent float64 `yaml:"hysteresis_percent,omitempty"`
	CLIMax            int     `yaml:"cli_max,omitempty"`
	GeneralMax        int     `yaml:"general_max,omitempty"`
	QueueMaxWait      string  `yaml:"queue_max_wait,omitempty"`
	QueueMaxWaiters   int     `yaml:"queue_max_waiters,omitempty"`
}

// Validate reports the first invalid admission setting.
//
// Validate 报告第一个无效的准入设置。
func (a AdmissionConfig) Validate() error {
	for name, value := range map[string]string{"sample_interval": a.SampleInterval, "queue_max_wait": a.QueueMaxWait} {
		if value == "" {
			continue
		}
		d, err := time.ParseDuration(value)
		if err != nil || d < 0 {
			return fmt.Errorf("admission.%s: invalid duration %q", name, value)
		}
		// A shorter interval would busy-read /proc and make the CPU delta
		// mostly scheduler-tick noise.
		// 更短的间隔会忙读 /proc，并使 CPU 增量几乎全是调度时钟噪声。
		if name == "sample_interval" && d != 0 && d < time.Second {
			return fmt.Errorf("admission.sample_interval: must be at least 1s")
		}
	}
	for name, value := range map[string]float64{
		"cpu_high_percent": a.CPUHighPercent, "memory_low_percent": a.MemoryLowPercent, "hysteresis_percent": a.HysteresisPercent,
	} {
		if !(value >= 0 && value <= 100) { // also rejects NaN / 同时拒绝 NaN
			return fmt.Errorf("admission.%s: must be between 0 and 100", name)
		}
	}
	// The exit mark must be reachable, or pressure once entered never clears.
	// 0 hysteresis means the default of 5 (admission.DefaultHysteresisPercent).
	// 退出线必须可达，否则一旦进入压力就永远无法解除。迟滞为 0 表示默认值 5。
	hysteresis := a.HysteresisPercent
	if hysteresis == 0 {
		hysteresis = 5
	}
	if a.CPUHighPercent > 0 && a.CPUHighPercent <= hysteresis {
		return fmt.Errorf("admission.cpu_high_percent: must exceed hysteresis_percent (%g)", hysteresis)
	}
	if a.MemoryLowPercent > 0 && a.MemoryLowPercent+hysteresis >= 100 {
		return fmt.Errorf("admission.memory_low_percent: plus hysteresis_percent (%g) must stay below 100", hysteresis)
	}
	for name, value := range map[string]int{"cli_max": a.CLIMax, "general_max": a.GeneralMax, "queue_max_waiters": a.QueueMaxWaiters} {
		if value < 0 {
			return fmt.Errorf("admission.%s: must not be negative", name)
		}
	}
	return nil
}

// GatewayConfig is the connection half of Config, mirroring tunnelOptions.
//
// GatewayConfig 是 Config 里负责连接的那一半，对应 tunnelOptions。
type GatewayConfig struct {
	// Endpoints is the seed list of Gateway replicas, host:port each; only
	// one has to be reachable, matching -gateway's own contract.
	//
	// Endpoints 是 Gateway 副本的种子列表，每项 host:port；只需一个可达，
	// 与 -gateway 本身的约定一致。
	Endpoints []string `yaml:"endpoints,omitempty"`

	// Registry is the Registry endpoint used for node certificate
	// issuance, mirroring -registry.
	//
	// Registry 是签发节点证书所用的 Registry 端点，对应 -registry。
	Registry string `yaml:"registry,omitempty"`

	// NodeID mirrors -node-id; empty lets the Registry assign one.
	//
	// NodeID 对应 -node-id；留空由 Registry 分配。
	NodeID string `yaml:"node_id,omitempty"`

	// CertFile, KeyFile, CAFile and BootstrapTokenFile mirror the flags of
	// the same purpose (-cert-file, -key-file, -ca-file,
	// -bootstrap-token-file). They are file paths, never key material
	// itself — nothing in this struct ever holds a private key or token
	// value.
	//
	// CertFile、KeyFile、CAFile、BootstrapTokenFile 分别对应同名 flag
	// （-cert-file、-key-file、-ca-file、-bootstrap-token-file）。它们是
	// 文件路径，从不是密钥本身——这个结构体里任何字段都不持有私钥或 token
	// 的值。
	CertFile           string `yaml:"cert_file,omitempty"`
	KeyFile            string `yaml:"key_file,omitempty"`
	CAFile             string `yaml:"ca_file,omitempty"`
	BootstrapTokenFile string `yaml:"bootstrap_token_file,omitempty"`

	// AllowedRuntimes mirrors -allowed-runtimes: the runtime ids the
	// Gateway may dispatch to. Empty means every configured runtime.
	//
	// AllowedRuntimes 对应 -allowed-runtimes：Gateway 可以下发调度的运行
	// 时 id 列表；为空表示放行所有已配置的运行时。
	AllowedRuntimes []string `yaml:"allowed_runtimes,omitempty"`

	// Labels mirrors -labels: routing facts about this node.
	//
	// Labels 对应 -labels：关于本节点的路由事实。
	Labels map[string]string `yaml:"labels,omitempty"`

	// MaxGateways mirrors -max-gateways; zero keeps the flag's own default.
	//
	// MaxGateways 对应 -max-gateways；零值保留 flag 自己的默认值。
	MaxGateways int `yaml:"max_gateways,omitempty"`
}

// RuntimeConfig declares one local inference backend to register at
// startup. Kind must name a kind common/runtime's registry knows (a local
// server, a local CLI or a hosted provider; see common/runtime.Kind); an
// unrecognized value is not validated here — it
// surfaces as runtime.ErrRuntimeKindUnsupported from manager.Add, the same
// error an unrecognized -kind would produce anywhere else in this codebase.
//
// RuntimeConfig 声明一个启动时要注册的本地推理后端。Kind 必须是
// common/runtime 注册表认识的种类（本地服务、本机 CLI 或托管供应商，见
// common/runtime.Kind）；这里不
// 校验未知取值——它会从 manager.Add 那里以
// runtime.ErrRuntimeKindUnsupported 的形式暴露出来，与本代码库其他地方一
// 个不认识的 -kind 产生的错误相同。
type RuntimeConfig struct {
	ID   string `yaml:"id"`
	Kind string `yaml:"kind"`
	// BaseURL is empty for local CLI kinds, which use the CLI's own login,
	// and optional for hosted providers, which default to the official
	// endpoint.
	// BaseURL 对本机 CLI 种类留空（使用 CLI 自己的登录），对托管供应商可选
	// （默认使用官方端点）。
	BaseURL string `yaml:"base_url,omitempty"`
	// APIKeyEnv names the environment variable holding a hosted provider's
	// API key. The file stores only that name, never the key itself. It is
	// read for hosted kinds only.
	// APIKeyEnv 指定保存托管供应商 API Key 的环境变量名。文件只保存变量名，
	// 从不保存 Key 本身。只对托管种类读取。
	APIKeyEnv string `yaml:"api_key_env,omitempty"`
	// APIKeyFile is the path of a private file holding a hosted provider's
	// API key, the same file-path convention as cert_file: the YAML stores
	// the path, never the key. The setup page writes such files under the
	// config directory's keys/ folder. When set it takes precedence over
	// APIKeyEnv. It is read for hosted kinds only.
	//
	// APIKeyFile 是保存托管供应商 API Key 的私有文件路径，与 cert_file 同一约定：YAML
	// 只保存路径，从不保存 Key。设置页面把这类文件写在配置目录的 keys/ 下。设置后
	// 优先于 APIKeyEnv。只对托管种类读取。
	APIKeyFile string `yaml:"api_key_file,omitempty"`
	// Models declares the catalogue for a hosted endpoint that cannot list
	// its own models; when set it is authoritative. See ModelList for the
	// accepted spellings.
	// Models 为无法列出自身模型的托管端点声明模型目录；设置后以它为准。可接受的
	// 写法见 ModelList。
	Models ModelList `yaml:"models,omitempty"`
	// ReasoningParser is runtime.Config.ReasoningParser: "think_tags" or
	// "think_tags_prefilled" splits inline <think> reasoning out of the
	// replies of an OpenAI-compatible backend that does not separate it
	// itself. The setup page has no field for it; a save from the page keeps
	// whatever the file already says.
	//
	// ReasoningParser 即 runtime.Config.ReasoningParser："think_tags" 或
	// "think_tags_prefilled" 为不自己分离思考的 OpenAI 兼容后端把回复里内联的 <think>
	// 思考切分出来。设置页面没有这个字段；从页面保存时保留文件里已有的值。
	ReasoningParser string `yaml:"reasoning_parser,omitempty"`
	// AccountDir is runtime.Config.AccountDir: the configuration directory
	// whose login a codex or claude runtime uses (CODEX_HOME /
	// CLAUDE_CONFIG_DIR). Leave it empty for the Agent's own login; give two
	// runtimes of the same kind different directories to serve two accounts.
	// A leading "~/" is expanded to the Agent user's home directory.
	//
	// AccountDir 即 runtime.Config.AccountDir：codex 或 claude 运行时所用登录所在的
	// 配置目录（CODEX_HOME / CLAUDE_CONFIG_DIR）。留空使用 Agent 自己的登录；给两个
	// 同种类运行时配置不同目录即可服务两个账户。开头的 "~/" 会展开为 Agent 用户的主目录。
	AccountDir string `yaml:"account_dir,omitempty"`
	// ProbeTimeout overrides runtime.Config.ProbeTimeout, the deadline of one
	// identity probe, health check or catalogue read, as a Go duration such
	// as "45s". Empty keeps the kind's default. Raise it when a local CLI
	// starts slowly, for example because its sessions directory has grown
	// large; it must lie in (0, MaxProbeTimeout]. The setup page has no field
	// for it; a save from the page keeps whatever the file already says.
	//
	// ProbeTimeout 覆盖 runtime.Config.ProbeTimeout，即单次身份探测、健康检查或目录
	// 读取的期限，写成 Go duration，如 "45s"。留空使用该种类的默认值。本机 CLI 启动
	// 较慢时（例如其 sessions 目录变得很大）调大它；取值须在 (0, MaxProbeTimeout] 内。
	// 设置页面没有这个字段；从页面保存时保留文件里已有的值。
	ProbeTimeout string `yaml:"probe_timeout,omitempty"`
}

// MaxProbeTimeout caps RuntimeConfig.ProbeTimeout. A probe holds the
// runtime's serial check loop, so an unbounded one would leave a hung CLI
// undetected; ten minutes covers any plausible cold start.
//
// MaxProbeTimeout 是 RuntimeConfig.ProbeTimeout 的上限。探测会占住运行时串行的
// 检查循环，不设上限会让卡住的 CLI 始终不被发现；十分钟足以覆盖任何合理的冷启动。
const MaxProbeTimeout = 10 * time.Minute

// ProbeTimeoutDuration parses ProbeTimeout. It returns 0 for an empty value,
// meaning the kind's default, and an error for anything outside
// (0, MaxProbeTimeout] or not a Go duration.
//
// ProbeTimeoutDuration 解析 ProbeTimeout。空值返回 0（即使用该种类的默认值）；
// 不是 Go duration 或不在 (0, MaxProbeTimeout] 内时返回错误。
func (rc RuntimeConfig) ProbeTimeoutDuration() (time.Duration, error) {
	value := strings.TrimSpace(rc.ProbeTimeout)
	if value == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 || d > MaxProbeTimeout {
		return 0, fmt.Errorf("runtime %q: probe_timeout must be a duration such as 45s, greater than 0 and at most %s", rc.ID, MaxProbeTimeout)
	}
	return d, nil
}

// ModelList is a model catalogue written either as one space- or
// comma-separated string or as a YAML list; Load turns a list into the
// string form. It is a string rather than a slice so RuntimeConfig stays
// comparable with ==, which callers rely on.
//
// ModelList 是模型目录，既可以写成一个以空格或逗号分隔的字符串，也可以写成
// YAML 列表；Load 会把列表转成字符串形式。它用字符串而不是切片，是为了让
// RuntimeConfig 仍可用 == 比较，调用方依赖这一点。
type ModelList string

// IDs splits the list on spaces and commas.
// IDs 按空格与逗号拆分列表。
func (m ModelList) IDs() []string {
	return strings.FieldsFunc(string(m), func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
}

// modelListHook decodes a YAML list into a ModelList.
// modelListHook 把 YAML 列表解码为 ModelList。
func modelListHook(from, to reflect.Type, data any) (any, error) {
	if to != reflect.TypeOf(ModelList("")) || from.Kind() != reflect.Slice {
		return data, nil
	}
	items, ok := data.([]any)
	if !ok {
		return data, nil
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("models entries must be strings")
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " "), nil
}

// Load reads and parses a -config file at path. A parse error is wrapped
// with path so a multi-node deployment's operator can tell which file is
// broken from the error alone.
//
// Load 读取并解析 path 处的 -config 文件。解析错误会带上 path 包装，这样多
// 节点部署的运维仅凭错误信息就能分辨是哪份文件出了问题。
func Load(path string) (*Config, error) {
	v := viper.New()
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return nil, err
	}

	var cfg Config
	if err := v.Unmarshal(&cfg, withYAMLTags); err != nil {
		return nil, fmt.Errorf("agentconfig: parse %s: %w", path, err)
	}
	if _, err := attachments.New(cfg.Attachments); err != nil {
		return nil, fmt.Errorf("agentconfig: invalid attachments configuration")
	}
	if err := cfg.Admission.Validate(); err != nil {
		return nil, fmt.Errorf("agentconfig: %s: %w", path, err)
	}
	for _, rc := range cfg.Runtimes {
		if _, err := rc.ProbeTimeoutDuration(); err != nil {
			return nil, fmt.Errorf("agentconfig: %s: %w", path, err)
		}
	}
	return &cfg, nil
}

// Save writes cfg to path as YAML, used by the configui package's local
// setup page. The file is written mode 0600, matching this codebase's
// convention for -cert-file/-key-file: this file holds file *paths* to
// credentials, never the credentials themselves, but there is no reason to
// make it any more readable than that.
//
// Save 把 cfg 以 YAML 形式写入 path，供 configui 包的本地设置页面使用。文
// 件以 0600 权限写入，与本代码库对 -cert-file/-key-file 的约定一致：这份
// 文件保存的是凭据的文件*路径*，从不是凭据本身，但也没有理由让它的可读权
// 限比这更宽。
func Save(path string, cfg *Config) error {
	if _, err := attachments.New(cfg.Attachments); err != nil {
		return fmt.Errorf("agentconfig: invalid attachments configuration")
	}
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.SetConfigPermissions(0o600)

	gateway := map[string]any{
		"endpoints":            cfg.Gateway.Endpoints,
		"registry":             cfg.Gateway.Registry,
		"node_id":              cfg.Gateway.NodeID,
		"cert_file":            cfg.Gateway.CertFile,
		"key_file":             cfg.Gateway.KeyFile,
		"ca_file":              cfg.Gateway.CAFile,
		"bootstrap_token_file": cfg.Gateway.BootstrapTokenFile,
		"allowed_runtimes":     cfg.Gateway.AllowedRuntimes,
		"labels":               cfg.Gateway.Labels,
		"max_gateways":         cfg.Gateway.MaxGateways,
	}
	v.Set("gateway", gateway)
	if cfg.Attachments != (attachments.Config{}) {
		v.Set("attachments", nonZeroYAMLFields(cfg.Attachments))
	}
	if cfg.Admission != (AdmissionConfig{}) {
		if err := cfg.Admission.Validate(); err != nil {
			return fmt.Errorf("agentconfig: %w", err)
		}
		v.Set("admission", nonZeroYAMLFields(cfg.Admission))
	}

	runtimes := make([]map[string]any, len(cfg.Runtimes))
	for i, rc := range cfg.Runtimes {
		entry := map[string]any{"id": rc.ID, "kind": rc.Kind, "base_url": rc.BaseURL}
		if rc.APIKeyEnv != "" {
			entry["api_key_env"] = rc.APIKeyEnv
		}
		if rc.APIKeyFile != "" {
			entry["api_key_file"] = rc.APIKeyFile
		}
		if rc.Models != "" {
			entry["models"] = string(rc.Models)
		}
		if rc.ReasoningParser != "" {
			entry["reasoning_parser"] = rc.ReasoningParser
		}
		if rc.AccountDir != "" {
			entry["account_dir"] = rc.AccountDir
		}
		if rc.ProbeTimeout != "" {
			entry["probe_timeout"] = rc.ProbeTimeout
		}
		runtimes[i] = entry
	}
	v.Set("runtimes", runtimes)

	if cfg.AutoDiscover != nil {
		v.Set("auto_discover", *cfg.AutoDiscover)
	}
	if cfg.AutoDiscoverInterval != "" {
		v.Set("auto_discover_interval", cfg.AutoDiscoverInterval)
	}
	if cfg.MetricsAddr != nil {
		v.Set("metrics_addr", *cfg.MetricsAddr)
	}
	if cfg.LogLevel != "" {
		v.Set("log_level", cfg.LogLevel)
	}

	var data bytes.Buffer
	if err := v.WriteConfigTo(&data); err != nil {
		return fmt.Errorf("agentconfig: encode YAML: %w", err)
	}
	if err := localstate.WriteFile(path, data.Bytes(), 0o600); err != nil {
		return fmt.Errorf("agentconfig: write %s: %w", path, err)
	}
	return nil
}

// nonZeroYAMLFields maps a flat struct's set fields to their YAML names, so a
// section writes only what the operator configured.
//
// nonZeroYAMLFields 把扁平结构体中已设置的字段映射到它们的 YAML 名称，使一个区块只写出
// 运维实际配置过的内容。
func nonZeroYAMLFields(section any) map[string]any {
	value := reflect.ValueOf(section)
	fields := make(map[string]any)
	for i := 0; i < value.NumField(); i++ {
		name := strings.Split(value.Type().Field(i).Tag.Get("yaml"), ",")[0]
		if !value.Field(i).IsZero() {
			fields[name] = value.Field(i).Interface()
		}
	}
	return fields
}

// withYAMLTags is the viper.DecoderConfigOption that makes Load's
// v.Unmarshal read Config's "yaml" struct tags (see structTagName) instead
// of viper's own default "mapstructure" tag, and adds modelListHook in front
// of viper's own decode hooks.
//
// withYAMLTags 是让 Load 的 v.Unmarshal 读取 Config 的 "yaml" 结构体标签
// （见 structTagName）、而不是 viper 自己默认的 "mapstructure" 标签的
// viper.DecoderConfigOption，并在 viper 自带的解码钩子之前加上 modelListHook。
func withYAMLTags(dc *mapstructure.DecoderConfig) {
	dc.TagName = structTagName
	if dc.DecodeHook == nil {
		dc.DecodeHook = modelListHook
		return
	}
	dc.DecodeHook = mapstructure.ComposeDecodeHookFunc(modelListHook, dc.DecodeHook)
}

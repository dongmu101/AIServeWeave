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
// Every field maps onto an existing -gateway/-registry/... flag documented
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
// otherwise minimal dependency footprint — see AGENTS.md's dependency
// section for why.
//
// agentconfig 是 -config 的落盘契约：运维把新节点接进来最常需要的两件事——
// 声明哪些本地推理后端、以及怎么联系远程 Gateway——用 YAML 表达以降低编辑
// 门槛。它刻意收得很窄：STATUS.md 后续的 Agent 本地功能（模型分发、
// ComfyUI Managed、Agent 自动升级）各自已经有自己的 flag 驱动清单，在这里
// 保持不变；要不要把它们也并进这份配置文件，留给后续接手"运行时配置文件加
// 载"的人决定，这个包不代为决定。
//
// 每个字段对应 main.go 里一个既有的 -gateway/-registry/... flag；命令行显
// 式传入的 flag 永远优先于这份文件里的同名设置，运维仍可以只在命令行覆盖
// 一个值而不用改文件（见 main.go 的 applyConfig）。YAML 经由 viper
// （github.com/spf13/viper）解码进 Config 的 Go 字段，标签沿用普通 YAML 解
// 码器会用的同一套 "yaml" 结构体标签——与本代码库其余本地清单（modelpull.
// Spec、agentupgrade.Entry）不同，那些是给非人工工具读的纯 JSON，而这份文
// 件存在的全部意义就是给人手改、或者由 configui 包的本地设置页面生成，因
// 此 YAML 就是唯一的落盘约定；这里的任何字段都从不发给、也从不接受
// Gateway 或控制面下发。viper 是 Agent 本来克制的依赖footprint 上一次刻意、
// 记录在案的例外——理由见 AGENTS.md 依赖那一节。
package agentconfig

import (
	"fmt"

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
	// Gateway holds every setting needed to reach and authenticate to the
	// remote Gateway, mirroring tunnelOptions in main.go.
	//
	// Gateway 保存联系并认证远程 Gateway 所需的全部设置，对应 main.go 里的
	// tunnelOptions。
	Gateway GatewayConfig `yaml:"gateway"`

	// Runtimes declares local inference backends to register at startup,
	// in addition to whatever -ollama-url/-ollama-id and auto-discovery
	// already add. Unlike those two, a declared runtime here may be any
	// kind common/runtime's registry knows (ollama, vllm, sglang, comfyui).
	//
	// Runtimes 声明启动时要注册的本地推理后端，是 -ollama-url/-ollama-id
	// 与自动发现之外的补充。与那两者不同，这里声明的运行时可以是
	// common/runtime 注册表认识的任意种类（ollama、vllm、sglang、
	// comfyui）。
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
// startup. Kind must name a kind common/runtime's registry knows (ollama,
// vllm, sglang, comfyui); an unrecognized value is not validated here — it
// surfaces as runtime.ErrRuntimeKindUnsupported from manager.Add, the same
// error an unrecognized -kind would produce anywhere else in this codebase.
//
// RuntimeConfig 声明一个启动时要注册的本地推理后端。Kind 必须是
// common/runtime 注册表认识的种类（ollama、vllm、sglang、comfyui）；这里不
// 校验未知取值——它会从 manager.Add 那里以
// runtime.ErrRuntimeKindUnsupported 的形式暴露出来，与本代码库其他地方一
// 个不认识的 -kind 产生的错误相同。
type RuntimeConfig struct {
	ID      string `yaml:"id"`
	Kind    string `yaml:"kind"`
	BaseURL string `yaml:"base_url"`
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
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigPermissions(0o600)

	v.Set("gateway", map[string]any{
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
	})

	runtimes := make([]map[string]any, len(cfg.Runtimes))
	for i, rc := range cfg.Runtimes {
		runtimes[i] = map[string]any{"id": rc.ID, "kind": rc.Kind, "base_url": rc.BaseURL}
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

	if err := v.WriteConfigAs(path); err != nil {
		return fmt.Errorf("agentconfig: write %s: %w", path, err)
	}
	return nil
}

// withYAMLTags is the viper.DecoderConfigOption that makes Load's
// v.Unmarshal read Config's "yaml" struct tags (see structTagName) instead
// of viper's own default "mapstructure" tag.
//
// withYAMLTags 是让 Load 的 v.Unmarshal 读取 Config 的 "yaml" 结构体标签
// （见 structTagName）、而不是 viper 自己默认的 "mapstructure" 标签的
// viper.DecoderConfigOption。
func withYAMLTags(dc *mapstructure.DecoderConfig) { dc.TagName = structTagName }

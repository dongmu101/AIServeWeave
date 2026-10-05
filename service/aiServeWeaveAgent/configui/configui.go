// Package configui is the Agent's local setup page, served alongside the Agent
// by default or on its own with -config-ui. A single HTML form, bound to
// loopback only at -config-ui-addr (default "127.0.0.1:8899"), fills in the settings
// agentconfig.Config holds — the remote Gateway to connect to and which
// local inference backends to register — and saves them to the -config
// file agentconfig.Load reads at normal startup. It exists because
// hand-writing YAML and reading main.go's -help output is real friction for
// a first-time node operator; a form the Agent itself serves is the lowest
// operation cost available without adding a whole separate UI project.
//
// The page edits the file, without live connection state or hot reload.
// Saving a change requires restarting the Agent to take effect. The standalone
// -config-ui mode never starts the tunnel or any runtime (see main.go).
//
// Binding anywhere but loopback is refused outright, not just discouraged
// in a flag's help text: this form can repoint the Agent at a different
// Gateway and Registry, so exposing it beyond 127.0.0.1 would hand a
// network attacker exactly the kind of remote-control surface AGENTS.md's
// security line "Agent 只主动出站建连，从不监听公网端口" exists to prevent.
//
// configui 是 Agent 的本地设置页面，默认随 Agent 启动，也可通过 -config-ui
// 独立运行。单页 HTML 表单只绑定回环地址，监听在 -config-ui-addr
// （默认 "127.0.0.1:8899"），用来填写 agentconfig.Config 持
// 有的同一批设置——要连接的远程 Gateway、以及要注册的本地推理后端——并把
// 它们保存到 agentconfig.Load 在正常启动时读取的 -config 文件。它存在的理
// 由是：让第一次上手的节点运维手写 YAML、再翻 main.go 的 -help 输出，是真
// 实存在的门槛；由 Agent 自己提供一个表单，是不额外起一个独立 UI 项目情况
// 下能做到的最低操作成本。
//
// 页面只编辑文件，不展示实时连接状态、不做热加载，保存改动后需要重启 Agent
// 才会生效。独立的 -config-ui 模式不启动隧道或任何运行时（见 main.go）。
//
// 绑定到回环地址之外的地址会被直接拒绝，而不只是在 flag 的帮助文本里劝
// 阻：这个表单能让 Agent 改去连接不同的 Gateway 和 Registry，把它暴露在
// 127.0.0.1 之外，等于把网络攻击者最想要的那种远程控制入口双手奉上——这
// 正是 AGENTS.md 安全红线「Agent 只主动出站建连，从不监听公网端口」要挡住
// 的事。
package configui

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"AIServeWeave/service/aiServeWeaveAgent/agentconfig"
)

// shutdownTimeout bounds how long Serve waits for an in-flight request to
// finish once ctx is canceled, mirroring main.go's shutdownTimeout for the
// same reason: a page nobody is using anymore should not hold up exit.
const shutdownTimeout = 5 * time.Second

// Serve runs the local setup page at addr until ctx is canceled, then shuts
// it down gracefully. addr must be a loopback address (see validateLoopback);
// any other address is refused before a listener is ever opened.
//
// configPath is where the form's current values are read from (if the file
// exists and parses) and where a save writes to. An empty configPath is
// rejected: the whole point of this mode is to produce a file the Agent's
// normal run will later load with -config, so there must be somewhere to
// put it.
//
// Serve 在 addr 上运行本地设置页面，直到 ctx 被取消后优雅关闭。addr 必须
// 是回环地址（见 validateLoopback）；其他地址在开监听之前就会被拒绝。
//
// configPath 是表单当前值的读取来源（如果文件存在且能解析）以及保存的写
// 入目标。configPath 为空会被拒绝：这个模式存在的全部意义就是产出一份
// Agent 正常运行时会用 -config 加载的文件，因此必须有地方可写。
func Serve(ctx context.Context, logger *slog.Logger, addr, configPath string) error {
	if configPath == "" {
		return errors.New("configui: a config path is required; there is nothing to save to otherwise")
	}
	if err := validateLoopback(addr); err != nil {
		return fmt.Errorf("configui: %w", err)
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("configui: %w", err)
	}
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/", newIndexHandler(logger, configPath))
	server := &http.Server{Addr: addr, Handler: mux}
	defer server.Close()

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()

	logger.Info("config setup page listening", slog.String("addr", listener.Addr().String()), slog.String("config_path", configPath))

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		<-serveErr
		return nil
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// validateLoopback rejects any addr that is not unambiguously loopback-only.
// A bare port ("" host, e.g. ":8080") binds every interface and is refused;
// "localhost" is accepted by name since it is conventionally loopback; any
// other host must parse as a literal IP that IsLoopback reports true for. A
// hostname that merely tends to resolve to loopback today is refused, since
// that mapping is not this package's to trust.
//
// validateLoopback 拒绝任何不能明确判定为「仅回环」的 addr。空 host（如
// ":8080"）会绑定所有网卡，直接拒绝；"localhost" 按惯例视为回环予以放行；
// 其他 host 必须能解析成一个 IsLoopback 判定为真的字面 IP。一个"今天恰好
// 解析到回环"的主机名会被拒绝，因为这层映射是否可信不该由这个包来断定。
func validateLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid -config-ui-addr %q: %w", addr, err)
	}
	if host == "" {
		return fmt.Errorf("-config-ui-addr %q has no host and would bind every interface; use 127.0.0.1:<port>", addr)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("-config-ui-addr %q is not a loopback address; the Agent never listens on a public port", addr)
	}
	return nil
}

// formValues is what the HTML template renders and what a POST is parsed
// back into — the string-shaped, human-edited form of agentconfig.Config.
// Keeping this separate from Config itself means a malformed field (an
// unparsable duration, a non-numeric max_gateways) can be re-shown to the
// operator with the rest of their input intact, instead of losing the whole
// form to a single typo.
//
// formValues 是 HTML 模板渲染、以及 POST 解析回去的目标——agentconfig.
// Config 的字符串形态、给人编辑用的版本。把它与 Config 本身分开，使得一个
// 格式错误的字段（解析不了的时长、非数字的 max_gateways）可以带着其余输
// 入原样回显给操作者，而不会因为一个笔误丢掉整份表单。
type formValues struct {
	Endpoints            string
	Registry             string
	NodeID               string
	CertFile             string
	KeyFile              string
	CAFile               string
	BootstrapTokenFile   string
	AllowedRuntimes      string
	Labels               string
	MaxGateways          string
	Runtimes             string
	AutoDiscover         bool
	AutoDiscoverInterval string
	MetricsAddr          string
	LogLevel             string
	Message              string
	Error                string
}

// newIndexHandler builds the "/" handler: GET renders the form (pre-filled
// from configPath when it already exists and parses), POST parses and saves
// it, re-rendering the same form with a confirmation or an error.
func newIndexHandler(logger *slog.Logger, configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			values := formValuesFromFile(configPath)
			renderForm(w, values)
		case http.MethodPost:
			values := formValuesFromRequest(r)
			cfg := values.toConfig()
			if err := agentconfig.Save(configPath, cfg); err != nil {
				logger.Error("config not saved", slog.Any("error", err))
				values.Error = "保存失败 / save failed: " + err.Error()
				renderForm(w, values)
				return
			}
			logger.Info("config saved", slog.String("config_path", configPath))
			values.Message = "已保存。重启不带 -config-ui 的 Agent 以生效。 / Saved. Restart the Agent without -config-ui for this to take effect."
			renderForm(w, values)
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// formValuesFromFile loads configPath for the GET form. A file that does
// not exist yet, or fails to parse, yields an empty form rather than an
// error page: the whole point of this mode is to help produce that file
// for the first time, so its absence is the expected starting state, not a
// failure.
func formValuesFromFile(configPath string) formValues {
	cfg, err := agentconfig.Load(configPath)
	if err != nil {
		return formValues{LogLevel: "info", AutoDiscover: true}
	}
	return formValuesFromConfig(cfg)
}

// formValuesFromConfig renders cfg's typed fields into the form's editable
// string shape.
func formValuesFromConfig(cfg *agentconfig.Config) formValues {
	v := formValues{
		Endpoints:            strings.Join(cfg.Gateway.Endpoints, ", "),
		Registry:             cfg.Gateway.Registry,
		NodeID:               cfg.Gateway.NodeID,
		CertFile:             cfg.Gateway.CertFile,
		KeyFile:              cfg.Gateway.KeyFile,
		CAFile:               cfg.Gateway.CAFile,
		BootstrapTokenFile:   cfg.Gateway.BootstrapTokenFile,
		AllowedRuntimes:      strings.Join(cfg.Gateway.AllowedRuntimes, ", "),
		AutoDiscover:         true,
		AutoDiscoverInterval: cfg.AutoDiscoverInterval,
		LogLevel:             cfg.LogLevel,
	}
	if cfg.Gateway.MaxGateways > 0 {
		v.MaxGateways = strconv.Itoa(cfg.Gateway.MaxGateways)
	}
	if cfg.AutoDiscover != nil {
		v.AutoDiscover = *cfg.AutoDiscover
	}
	if cfg.MetricsAddr != nil {
		v.MetricsAddr = *cfg.MetricsAddr
	}
	if v.LogLevel == "" {
		v.LogLevel = "info"
	}
	labelLines := make([]string, 0, len(cfg.Gateway.Labels))
	for k, val := range cfg.Gateway.Labels {
		labelLines = append(labelLines, k+"="+val)
	}
	v.Labels = strings.Join(labelLines, "\n")
	runtimeLines := make([]string, 0, len(cfg.Runtimes))
	for _, rc := range cfg.Runtimes {
		runtimeLines = append(runtimeLines, rc.ID+","+rc.Kind+","+rc.BaseURL)
	}
	v.Runtimes = strings.Join(runtimeLines, "\n")
	return v
}

// formValuesFromRequest reads a POST's form fields as-is; r.ParseForm's own
// error is not fatal here because a malformed body still leaves r.Form
// usable for whatever fields did parse, and toConfig tolerates missing
// fields the same way main.go's flag parsers tolerate a malformed -labels
// entry.
func formValuesFromRequest(r *http.Request) formValues {
	_ = r.ParseForm()
	return formValues{
		Endpoints:            r.FormValue("endpoints"),
		Registry:             r.FormValue("registry"),
		NodeID:               r.FormValue("node_id"),
		CertFile:             r.FormValue("cert_file"),
		KeyFile:              r.FormValue("key_file"),
		CAFile:               r.FormValue("ca_file"),
		BootstrapTokenFile:   r.FormValue("bootstrap_token_file"),
		AllowedRuntimes:      r.FormValue("allowed_runtimes"),
		Labels:               r.FormValue("labels"),
		MaxGateways:          r.FormValue("max_gateways"),
		Runtimes:             r.FormValue("runtimes"),
		AutoDiscover:         r.FormValue("auto_discover") != "",
		AutoDiscoverInterval: r.FormValue("auto_discover_interval"),
		MetricsAddr:          r.FormValue("metrics_addr"),
		LogLevel:             r.FormValue("log_level"),
	}
}

// toConfig converts the form's string-shaped input into agentconfig.Config.
// Every list/map field tolerates a malformed entry by dropping it, the same
// restraint main.go's own -labels/-allowed-runtimes parsing already uses:
// a typo in one line should not cost the operator their whole saved
// configuration.
func (v formValues) toConfig() *agentconfig.Config {
	autoDiscover := v.AutoDiscover
	metricsAddr := strings.TrimSpace(v.MetricsAddr)
	maxGateways, _ := strconv.Atoi(strings.TrimSpace(v.MaxGateways))

	cfg := &agentconfig.Config{
		Gateway: agentconfig.GatewayConfig{
			Endpoints:          splitList(v.Endpoints, ","),
			Registry:           strings.TrimSpace(v.Registry),
			NodeID:             strings.TrimSpace(v.NodeID),
			CertFile:           strings.TrimSpace(v.CertFile),
			KeyFile:            strings.TrimSpace(v.KeyFile),
			CAFile:             strings.TrimSpace(v.CAFile),
			BootstrapTokenFile: strings.TrimSpace(v.BootstrapTokenFile),
			AllowedRuntimes:    splitList(v.AllowedRuntimes, ","),
			Labels:             parseLabels(v.Labels),
			MaxGateways:        maxGateways,
		},
		Runtimes:             parseRuntimes(v.Runtimes),
		AutoDiscover:         &autoDiscover,
		AutoDiscoverInterval: strings.TrimSpace(v.AutoDiscoverInterval),
		MetricsAddr:          &metricsAddr,
		LogLevel:             strings.TrimSpace(v.LogLevel),
	}
	return cfg
}

// splitList splits s on any of seps' bytes, trims each piece, and drops
// empty ones — the same lenient parsing tunnelOptions.seeds already applies
// to -gateway.
func splitList(s, seps string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(seps+"\n", r) })
	var out []string
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// parseLabels parses one "key=value" pair per line, the newline-separated
// analogue of tunnelOptions.nodeLabels' comma-separated parsing — a text
// area is a friendlier control than a single line for a map with more than
// one or two entries.
func parseLabels(s string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseRuntimes parses one "id,kind,base_url" declaration per line into
// agentconfig.RuntimeConfig. A line missing any of the three fields is
// dropped rather than failing the whole save; Codex has no base_url.
//
// parseRuntimes 解析每行的运行时声明；缺失字段时略过，Codex 的 base_url 留空。
func parseRuntimes(s string) []agentconfig.RuntimeConfig {
	var out []agentconfig.RuntimeConfig
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ",", 3)
		if len(parts) != 3 {
			continue
		}
		id, kind, baseURL := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		if id == "" || kind == "" || (baseURL == "" && kind != "codex") {
			continue
		}
		out = append(out, agentconfig.RuntimeConfig{ID: id, Kind: kind, BaseURL: baseURL})
	}
	return out
}

// formTemplate groups one shared form behind local navigation, without external assets.
// With JavaScript disabled, navigation links scroll to the visible groups.
//
// formTemplate 将同一表单按左侧菜单分组，不依赖外部资源。
// 禁用 JavaScript 时，导航链接滚动到保持可见的对应分组。
var formTemplate = template.Must(template.New("form").Parse(`<!DOCTYPE html>
<html lang="zh-Hans">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>AIServeWeave Agent 设置 / Setup</title>
<style>
* { box-sizing: border-box; }
body { margin: 0; background: #f5f7fb; color: #1e293b; font: 15px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
.layout { display: grid; grid-template-columns: 240px minmax(0, 1fr); min-height: 100vh; }
.sidebar { position: sticky; top: 0; height: 100vh; padding: 32px 20px; background: #fff; border-right: 1px solid #e2e8f0; }
.brand { margin: 0; font-size: 20px; font-weight: 700; letter-spacing: -.5px; }
.subtitle, .hint { color: #64748b; font-size: 13px; }
.subtitle { margin: 4px 0 28px; }
nav { display: grid; gap: 8px; }
nav a { display: flex; align-items: center; gap: 12px; padding: 12px; border-radius: 8px; color: #475569; text-decoration: none; }
nav a:hover { background: #f1f5f9; }
nav a[aria-current="page"] { background: #eff6ff; color: #1d4ed8; font-weight: 600; }
.menu-index { font-size: 12px; opacity: .65; }
.sidebar-note { margin-top: 32px; padding-top: 20px; border-top: 1px solid #e2e8f0; }
main { width: 100%; max-width: 1040px; padding: 36px 48px; }
h1 { margin: 0; font-size: 28px; }
.intro { margin: 6px 0 28px; color: #64748b; }
fieldset { min-width: 0; margin: 0 0 24px; padding: 24px; border: 1px solid #e2e8f0; border-radius: 12px; background: #fff; }
[hidden] { display: none !important; }
legend { padding: 0 8px; font-size: 18px; font-weight: 600; }
.group-hint { margin: 0 0 20px; color: #64748b; }
.fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 20px; }
.field { min-width: 0; }
.wide { grid-column: 1 / -1; }
label { display: block; margin-bottom: 6px; font-weight: 600; }
label span { display: block; color: #64748b; font-size: 12px; font-weight: 400; }
input[type=text], input[type=url], select, textarea { width: 100%; padding: 10px 12px; border: 1px solid #cbd5e1; border-radius: 7px; background: #fff; color: inherit; font: inherit; }
textarea { min-height: 100px; resize: vertical; font-family: ui-monospace, monospace; font-size: 13px; }
input:focus-visible, select:focus-visible, textarea:focus-visible, a:focus-visible, button:focus-visible { outline: 3px solid #bfdbfe; outline-offset: 2px; border-color: #3b82f6; }
.checkbox { display: flex; align-items: center; gap: 10px; margin: 0; }
input[type=checkbox] { width: 18px; height: 18px; accent-color: #2563eb; }
.hint { margin: 6px 0 0; overflow-wrap: anywhere; }
.message, .error { padding: 12px 16px; border-radius: 8px; overflow-wrap: anywhere; }
.message { background: #ecfdf5; color: #047857; }
.error { background: #fef2f2; color: #b91c1c; }
.actions { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.actions .hint { margin: 0; }
button { flex-shrink: 0; padding: 11px 22px; border: 0; border-radius: 8px; background: #2563eb; color: #fff; font: inherit; font-weight: 600; cursor: pointer; }
button:hover { background: #1d4ed8; }
.runtime-list { display: grid; gap: 16px; }
.runtime-card { padding: 16px; border: 1px solid #e2e8f0; border-radius: 8px; }
.runtime-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-bottom: 16px; }
.secondary { padding: 7px 12px; border: 1px solid #cbd5e1; background: #fff; color: #475569; font-size: 13px; }
.secondary:hover { background: #f1f5f9; }
.runtime-add { margin-top: 16px; }
@media (max-width: 760px) {
  .layout { display: block; }
  .sidebar { position: static; height: auto; padding: 20px; border-right: 0; border-bottom: 1px solid #e2e8f0; }
  .subtitle { margin-bottom: 16px; }
  nav { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  nav a { padding: 10px; }
  .sidebar-note { display: none; }
  main { padding: 24px 20px; }
  fieldset { padding: 20px 16px; }
  .fields { grid-template-columns: minmax(0, 1fr); }
  .actions { align-items: flex-start; flex-direction: column; }
  button { width: 100%; }
}
</style>
</head>
<body>
<div class="layout">
<aside class="sidebar">
<p class="brand">AIServeWeave</p>
<p class="subtitle">Agent · 本地配置</p>
<nav aria-label="配置分组 / Configuration groups">
<a href="#gateway"><span class="menu-index" aria-hidden="true">01</span>Gateway 连接</a>
<a href="#identity"><span class="menu-index" aria-hidden="true">02</span>节点与认证</a>
<a href="#runtimes"><span class="menu-index" aria-hidden="true">03</span>本地 AI</a>
<a href="#observability"><span class="menu-index" aria-hidden="true">04</span>日志与指标</a>
</nav>
<p class="hint sidebar-note">配置保存到本机文件。<br>重启 Agent 后生效。</p>
</aside>
<main>
<h1>Agent 配置</h1>
<p class="intro">连接 Gateway，配置本地推理后端。</p>
{{if .Message}}<p class="message" role="status">{{.Message}}</p>{{end}}
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<form method="post">
<fieldset id="gateway">
<legend>Gateway 连接 / Connection</legend>
<p class="group-hint">设置 Agent 连接的远程服务。</p>
<div class="fields">
<div class="field wide">
<label for="endpoints">Gateway 地址 <span>Gateway endpoints</span></label>
<input type="text" id="endpoints" name="endpoints" value="{{.Endpoints}}" placeholder="127.0.0.1:8443">
<p class="hint">使用 host:port，多个地址用逗号分隔。</p>
</div>
<div class="field">
<label for="registry">Registry 地址 <span>Registry endpoint</span></label>
<input type="text" id="registry" name="registry" value="{{.Registry}}">
</div>
<div class="field">
<label for="max_gateways">最大 Gateway 连接数 <span>Max simultaneous gateway replicas</span></label>
<input type="text" id="max_gateways" name="max_gateways" value="{{.MaxGateways}}" inputmode="numeric">
<p class="hint">填 0 或留空使用默认值。</p>
</div>
</div>
</fieldset>
<fieldset id="identity">
<legend>节点与认证 / Identity</legend>
<p class="group-hint">配置节点身份、标签和连接所需的证书文件。</p>
<div class="fields">
<div class="field wide">
<label for="node_id">节点 ID <span>Node ID</span></label>
<input type="text" id="node_id" name="node_id" value="{{.NodeID}}">
<p class="hint">留空由 Registry 分配。</p>
</div>
<div class="field">
<label for="cert_file">证书文件路径 <span>Cert file path</span></label>
<input type="text" id="cert_file" name="cert_file" value="{{.CertFile}}">
</div>
<div class="field">
<label for="key_file">私钥文件路径 <span>Key file path</span></label>
<input type="text" id="key_file" name="key_file" value="{{.KeyFile}}">
</div>
<div class="field">
<label for="ca_file">CA 证书路径 <span>CA file path</span></label>
<input type="text" id="ca_file" name="ca_file" value="{{.CAFile}}">
</div>
<div class="field">
<label for="bootstrap_token_file">一次性注册令牌文件路径 <span>Bootstrap token file path</span></label>
<input type="text" id="bootstrap_token_file" name="bootstrap_token_file" value="{{.BootstrapTokenFile}}">
</div>
<div class="field wide">
<label for="labels">节点标签 <span>Node labels</span></label>
<textarea id="labels" name="labels" placeholder="region=local">{{.Labels}}</textarea>
<p class="hint">每行一条 key=value。</p>
</div>
</div>
</fieldset>
<fieldset id="runtimes">
<legend>本地 AI / Local AI</legend>
<p class="group-hint">声明本机推理后端，并设置自动发现。</p>
<div class="fields">
<div class="field wide">
<label for="runtime_declarations">本地运行时 <span>Local runtimes</span></label>
<div id="runtime-editor" hidden>
<div class="runtime-list" id="runtime-list"></div>
<button type="button" class="secondary runtime-add" id="runtime-add">+ 添加本地 AI</button>
<p class="hint">可以同时配置多个后端，每个运行时 ID 必须唯一。CLI 使用本机已登录的账户。</p>
</div>
<textarea id="runtime_declarations" name="runtimes" placeholder="ollama-local,ollama,http://127.0.0.1:11434&#10;codex-local,codex,">{{.Runtimes}}</textarea>
<p class="hint" id="runtime-format-hint">每行一条 id,kind,base_url；支持 ollama / vllm / sglang / comfyui / codex，codex 的地址留空。</p>
<template id="runtime-card-template">
<section class="runtime-card">
<div class="runtime-heading"><strong>本地 AI 连接</strong><button type="button" class="secondary runtime-remove">移除</button></div>
<div class="fields">
<div class="field"><label>运行时 ID <span>Runtime ID</span><input type="text" class="runtime-id" placeholder="ollama-local" required pattern="[^,\s]+"></label></div>
<div class="field"><label>后端类型 <span>Backend</span><select class="runtime-kind">
<option value="ollama">Ollama</option><option value="vllm">vLLM</option><option value="sglang">SGLang</option><option value="comfyui">ComfyUI</option><option value="codex">Codex CLI</option><option value="claude" disabled>Claude Code CLI（尚未接入）</option>
</select></label></div>
<div class="field wide"><label>服务地址 <span>Base URL</span><input type="url" class="runtime-url" placeholder="http://127.0.0.1:11434" required pattern="https?://[^,\s]+"></label><p class="hint runtime-auth" hidden>Codex CLI 无需服务地址；请先在本机执行 codex login。</p></div>
</div>
</section>
</template>
</div>
<div class="field wide">
<label for="allowed_runtimes">允许调度的运行时 ID <span>Allowed runtime IDs</span></label>
<input type="text" id="allowed_runtimes" name="allowed_runtimes" value="{{.AllowedRuntimes}}">
<p class="hint">多个 ID 用逗号分隔，留空放行全部。</p>
</div>
<div class="field wide">
<label class="checkbox" for="auto_discover"><input type="checkbox" id="auto_discover" name="auto_discover" value="1" {{if .AutoDiscover}}checked{{end}}>自动发现本机 Ollama / vLLM</label>
</div>
<div class="field wide">
<label for="auto_discover_interval">自动发现间隔 <span>Auto-discover interval</span></label>
<input type="text" id="auto_discover_interval" name="auto_discover_interval" value="{{.AutoDiscoverInterval}}" placeholder="30s">
<p class="hint">例如 30s、2m。</p>
</div>
</div>
</fieldset>
<fieldset id="observability">
<legend>日志与指标 / Observability</legend>
<p class="group-hint">配置本机监控入口与日志输出。</p>
<div class="fields">
<div class="field wide">
<label for="metrics_addr">指标监听地址 <span>Metrics listen address</span></label>
<input type="text" id="metrics_addr" name="metrics_addr" value="{{.MetricsAddr}}" placeholder="127.0.0.1:9091">
<p class="hint">留空关闭指标监听。</p>
</div>
<div class="field wide">
<label for="log_level">日志级别 <span>Log level</span></label>
<input type="text" id="log_level" name="log_level" value="{{.LogLevel}}">
<p class="hint">debug、info、warn 或 error。</p>
</div>
</div>
</fieldset>
<div class="actions">
<p class="hint">统一保存全部分组的配置，重启 Agent 后生效。</p>
<button type="submit">保存配置 / Save</button>
</div>
</form>
</main>
</div>
<script>
const groups = Array.from(document.querySelectorAll('form fieldset'));
const menu = Array.from(document.querySelectorAll('nav a'));
function selectGroup() {
  const active = groups.find(group => '#' + group.id === window.location.hash) || groups[0];
  groups.forEach(group => { group.hidden = group !== active; });
  menu.forEach(link => {
    if (link.hash === '#' + active.id) link.setAttribute('aria-current', 'page');
    else link.removeAttribute('aria-current');
  });
}
window.addEventListener('hashchange', selectGroup);
selectGroup();
const declarations = document.getElementById('runtime_declarations');
const runtimeList = document.getElementById('runtime-list');
function syncRuntimes() {
  const rows = Array.from(runtimeList.children);
  const ids = rows.map(row => row.querySelector('.runtime-id').value.trim());
  declarations.value = rows.map((row, index) => {
    const id = row.querySelector('.runtime-id');
    id.setCustomValidity(ids[index] && ids.indexOf(ids[index]) !== index ? '运行时 ID 不能重复' : '');
    return [ids[index], row.querySelector('.runtime-kind').value, row.querySelector('.runtime-url').value.trim()].join(',');
  }).join('\n');
}
function addRuntime(id = '', kind = 'ollama', url = '') {
  const row = document.getElementById('runtime-card-template').content.firstElementChild.cloneNode(true);
  const idInput = row.querySelector('.runtime-id');
  const kindInput = row.querySelector('.runtime-kind');
  const urlInput = row.querySelector('.runtime-url');
  idInput.value = id;
  if (!Array.from(kindInput.options).some(option => option.value === kind)) {
    kindInput.add(new Option(kind + '（未知类型）', kind));
  }
  kindInput.value = kind;
  urlInput.value = url;
  function updateKind() {
    const cli = kindInput.value === 'codex';
    urlInput.disabled = cli;
    urlInput.required = !cli;
    urlInput.closest('label').hidden = cli;
    row.querySelector('.runtime-auth').hidden = !cli;
    if (cli) urlInput.value = '';
    syncRuntimes();
  }
  row.addEventListener('input', syncRuntimes);
  kindInput.addEventListener('change', updateKind);
  row.querySelector('.runtime-remove').addEventListener('click', () => { row.remove(); syncRuntimes(); });
  runtimeList.append(row);
  updateKind();
  return idInput;
}
const initialRuntimes = declarations.value.split('\n').filter(line => line.trim());
initialRuntimes.forEach(line => {
  const parts = line.split(',');
  addRuntime((parts[0] || '').trim(), (parts[1] || '').trim(), (parts.slice(2).join(',') || '').trim());
});
document.getElementById('runtime-add').addEventListener('click', () => { addRuntime().focus(); });
declarations.hidden = true;
document.getElementById('runtime-format-hint').hidden = true;
document.getElementById('runtime-editor').hidden = false;
document.querySelector('form').addEventListener('submit', syncRuntimes);
document.querySelector('form').addEventListener('invalid', event => {
  const group = event.target.closest('fieldset');
  if (group && group.hidden) {
    window.location.hash = group.id;
    selectGroup();
  }
}, true);
</script>
</body>
</html>
`))

// renderForm executes formTemplate. A template execution error only ever
// indicates a bug in formTemplate itself (every field is a plain string or
// bool), so it is logged rather than surfaced through the response, which
// has likely already been partially written by the time template.Execute
// can fail.
func renderForm(w http.ResponseWriter, v formValues) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = formTemplate.Execute(w, v)
}

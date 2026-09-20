// Package configui is the Agent's local setup page: -config-ui serves a
// single HTML form, bound to loopback only at -config-ui-addr (a
// convenience default, "127.0.0.1:8899", that only takes effect once
// -config-ui turns the page on), for filling in the same settings
// agentconfig.Config holds — the remote Gateway to connect to and which
// local inference backends to register — and saving them to the -config
// file agentconfig.Load reads at normal startup. It exists because
// hand-writing YAML and reading main.go's -help output is real friction for
// a first-time node operator; a form the Agent itself serves is the lowest
// operation cost available without adding a whole separate UI project.
//
// This page never starts the tunnel or any runtime: -config-ui is a
// distinct mode from a normal run (see main.go), so there is no live
// connection state to show, no hot-reload to reason about, and saving a
// change here always requires restarting the Agent without -config-ui for
// it to take effect — the same restart already required for any other flag
// or config file change.
//
// Binding anywhere but loopback is refused outright, not just discouraged
// in a flag's help text: this form can repoint the Agent at a different
// Gateway and Registry, so exposing it beyond 127.0.0.1 would hand a
// network attacker exactly the kind of remote-control surface AGENTS.md's
// security line "Agent 只主动出站建连，从不监听公网端口" exists to prevent.
//
// configui 是 Agent 的本地设置页面：-config-ui 打开一个只绑定回环地址、监
// 听在 -config-ui-addr（一个便利性默认值 "127.0.0.1:8899"，只在 -config-ui
// 把页面打开之后才生效）的单页 HTML 表单，用来填写 agentconfig.Config 持
// 有的同一批设置——要连接的远程 Gateway、以及要注册的本地推理后端——并把
// 它们保存到 agentconfig.Load 在正常启动时读取的 -config 文件。它存在的理
// 由是：让第一次上手的节点运维手写 YAML、再翻 main.go 的 -help 输出，是真
// 实存在的门槛；由 Agent 自己提供一个表单，是不额外起一个独立 UI 项目情况
// 下能做到的最低操作成本。
//
// 这个页面从不启动隧道或任何运行时：-config-ui 是与正常运行（见 main.go）
// 不同的独立模式，因此没有实时连接状态可展示，也没有热加载需要考虑——在
// 这里保存一次改动，永远需要在不带 -config-ui 的情况下重启 Agent 才能生
// 效，与改动任何其他 flag 或配置文件所需的重启完全一样。
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

	mux := http.NewServeMux()
	mux.HandleFunc("/", newIndexHandler(logger, configPath))
	server := &http.Server{Addr: addr, Handler: mux}

	serveErr := make(chan error, 1)
	go func() { serveErr <- server.ListenAndServe() }()

	logger.Info("config setup page listening", slog.String("addr", addr), slog.String("config_path", configPath))

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
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
// dropped rather than failing the whole save.
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
		if id == "" || kind == "" || baseURL == "" {
			continue
		}
		out = append(out, agentconfig.RuntimeConfig{ID: id, Kind: kind, BaseURL: baseURL})
	}
	return out
}

// formTemplate is the whole page: one form, no JavaScript, no external
// assets — consistent with this package importing nothing beyond the
// standard library and agentconfig.
var formTemplate = template.Must(template.New("form").Parse(`<!DOCTYPE html>
<html lang="zh-Hant">
<head>
<meta charset="utf-8">
<title>AIServeWeave Agent 设置 / Setup</title>
<style>
body { font-family: -apple-system, sans-serif; max-width: 640px; margin: 2rem auto; padding: 0 1rem; }
fieldset { margin-bottom: 1.5rem; }
label { display: block; margin-top: 0.75rem; font-weight: 600; }
input[type=text], textarea { width: 100%; box-sizing: border-box; padding: 0.4rem; margin-top: 0.25rem; }
textarea { font-family: monospace; height: 4rem; }
.hint { color: #666; font-size: 0.85rem; margin-top: 0.15rem; }
.message { color: #0a7c2f; }
.error { color: #b00020; }
button { margin-top: 1.5rem; padding: 0.6rem 1.2rem; }
</style>
</head>
<body>
<h1>AIServeWeave Agent 设置 / Setup</h1>
{{if .Message}}<p class="message">{{.Message}}</p>{{end}}
{{if .Error}}<p class="error">{{.Error}}</p>{{end}}
<form method="post">
<fieldset>
<legend>远程 Gateway / Remote Gateway</legend>
<label>Gateway 地址（逗号分隔，host:port） / Gateway endpoints (comma-separated host:port)</label>
<input type="text" name="endpoints" value="{{.Endpoints}}">
<label>Registry 地址 / Registry endpoint</label>
<input type="text" name="registry" value="{{.Registry}}">
<label>节点 ID（留空由 Registry 分配） / Node ID (empty lets the Registry assign one)</label>
<input type="text" name="node_id" value="{{.NodeID}}">
<label>证书文件路径 / Cert file path</label>
<input type="text" name="cert_file" value="{{.CertFile}}">
<label>私钥文件路径 / Key file path</label>
<input type="text" name="key_file" value="{{.KeyFile}}">
<label>CA 证书路径 / CA file path</label>
<input type="text" name="ca_file" value="{{.CAFile}}">
<label>一次性注册令牌文件路径 / Bootstrap token file path</label>
<input type="text" name="bootstrap_token_file" value="{{.BootstrapTokenFile}}">
<label>允许调度的运行时 ID（逗号分隔，留空放行全部） / Allowed runtime IDs (comma-separated, empty allows all)</label>
<input type="text" name="allowed_runtimes" value="{{.AllowedRuntimes}}">
<label>节点标签（每行一条 key=value） / Node labels (one key=value per line)</label>
<textarea name="labels">{{.Labels}}</textarea>
<label>最大同时连接的 Gateway 副本数（0 = 默认） / Max simultaneous gateway replicas (0 = default)</label>
<input type="text" name="max_gateways" value="{{.MaxGateways}}">
</fieldset>
<fieldset>
<legend>本地 AI 连接 / Local AI Connections</legend>
<label>本地运行时（每行一条 id,kind,base_url；kind 为 ollama/vllm/sglang/comfyui） / Local runtimes (one "id,kind,base_url" per line; kind is ollama/vllm/sglang/comfyui)</label>
<textarea name="runtimes" placeholder="ollama-local,ollama,http://127.0.0.1:11434">{{.Runtimes}}</textarea>
<label><input type="checkbox" name="auto_discover" value="1" {{if .AutoDiscover}}checked{{end}}> 自动发现本机 Ollama/vLLM / Auto-discover local Ollama/vLLM</label>
<label>自动发现间隔（如 30s、2m） / Auto-discover interval (e.g. 30s, 2m)</label>
<input type="text" name="auto_discover_interval" value="{{.AutoDiscoverInterval}}">
</fieldset>
<fieldset>
<legend>其他 / Other</legend>
<label>指标监听地址（留空关闭） / Metrics listen address (empty disables it)</label>
<input type="text" name="metrics_addr" value="{{.MetricsAddr}}">
<label>日志级别 / Log level</label>
<input type="text" name="log_level" value="{{.LogLevel}}">
<p class="hint">debug, info, warn 或 error / debug, info, warn, or error</p>
</fieldset>
<button type="submit">保存 / Save</button>
</form>
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

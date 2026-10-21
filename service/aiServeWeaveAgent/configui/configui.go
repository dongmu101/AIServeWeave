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
// The page edits the file and shows model and live Gateway connection snapshots
// from the running Agent, without hot reload.
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
// 页面编辑文件并列出运行中 Agent 的快照模型与实时 Gateway 连接状态，不做热加载，保存改动后需要重启 Agent
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
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"AIServeWeave/common/browserlang"
	"AIServeWeave/common/runtime"
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
// sources supplies the running Agent's runtime and Gateway snapshots;
// empty sources mean setup-only mode.
//
// Serve 在 addr 上运行本地设置页面，直到 ctx 被取消后优雅关闭。addr 必须
// 是回环地址（见 validateLoopback）；其他地址在开监听之前就会被拒绝。
//
// configPath 是表单当前值的读取来源（如果文件存在且能解析）以及保存的写
// 入目标。configPath 为空会被拒绝：这个模式存在的全部意义就是产出一份
// Agent 正常运行时会用 -config 加载的文件，因此必须有地方可写。
// sources 提供当前运行时与 Gateway 快照；空来源表示仅运行设置页面。
func Serve(ctx context.Context, logger *slog.Logger, addr, configPath string, sources Sources) error {
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
	mux.Handle("/assets/", http.FileServer(http.FS(pageAssets)))
	mux.HandleFunc("/i18n/", serveLanguageResource)
	mux.HandleFunc("/status/gateway", newGatewayHandler(sources.Gateway))
	mux.HandleFunc("/status/cli-usage", newUsageHandler(sources.CLIUsage))
	mux.HandleFunc("/", newIndexHandler(logger, configPath, sources))
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
	Locale               browserlang.Locale
	Text                 pageText
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
	Models               modelCatalogue
	Gateway              gatewayView
	CLIUsage             cliUsageView
}

// newIndexHandler builds the "/" handler: GET renders the form (pre-filled
// from configPath when it already exists and parses), POST parses and saves
// it, re-rendering the same form with a confirmation or an error.
// It also renders the Agent's read-only runtime and Gateway snapshots.
// newIndexHandler 渲染或保存配置，同时展示当前 Agent 的只读模型目录与 Gateway 快照。
func newIndexHandler(logger *slog.Logger, configPath string, sources Sources) http.HandlerFunc {
	render := func(w http.ResponseWriter, values formValues) {
		values.Models = availableModels(sources.Runtimes)
		values.Gateway = currentGateway(sources.Gateway, values.Locale)
		values.CLIUsage = currentCLIUsage(sources.CLIUsage, values.Locale)
		renderForm(w, values)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		locale := requestLocale(r)
		w.Header().Set("Content-Language", string(locale))
		w.Header().Set("Vary", "Cookie, Accept-Language")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.Method {
		case http.MethodHead:
			return
		case http.MethodGet:
			values := formValuesFromFile(configPath)
			values.Locale, values.Text = locale, newPageText(locale)
			render(w, values)
		case http.MethodPost:
			values := formValuesFromRequest(r)
			if selected, ok := browserlang.Parse(r.FormValue("locale")); ok {
				locale = selected
				http.SetCookie(w, browserlang.PreferenceCookie(localeCookie, locale, r.TLS != nil))
			}
			values.Locale, values.Text = locale, newPageText(locale)
			w.Header().Set("Content-Language", string(locale))
			if r.FormValue("action") == "locale" {
				render(w, values)
				return
			}
			cfg := values.toConfig()
			if err := agentconfig.Save(configPath, cfg); err != nil {
				logger.Error("config not saved", slog.String("error_kind", "save_failed"))
				values.Error = values.Text.Format("a0083", map[string]any{"error": values.Text.Text("a0102")})
				render(w, values)
				return
			}
			logger.Info("config saved", slog.String("config_path", configPath))
			values.Message = values.Text.Text("a0084")
			render(w, values)
		default:
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
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
		line := rc.ID + "," + rc.Kind + "," + rc.BaseURL
		if runtime.Kind(rc.Kind).IsHosted() && (rc.APIKeyEnv != "" || rc.Models != "") {
			line += "," + rc.APIKeyEnv + "," + strings.Join(rc.Models.IDs(), " ")
		}
		runtimeLines = append(runtimeLines, line)
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

// parseRuntimes parses one declaration per line into
// agentconfig.RuntimeConfig: "id,kind,base_url" for every kind, where
// base_url is the rest of the line as before, and
// "id,kind,base_url[,api_key_env[,models]]" for hosted kinds, models being
// space-separated. A line missing a required field is dropped rather than
// failing the whole save. CLI runtimes have no base_url; hosted providers
// may leave it empty for the official endpoint, except the two compatible
// kinds, which have none.
//
// parseRuntimes 每行解析一条声明：所有种类都是 "id,kind,base_url"，base_url 与
// 以往一样取该行剩余部分；托管种类为 "id,kind,base_url[,api_key_env[,models]]"，
// models 以空格分隔。缺失必填字段的行被略过，而不是让整次保存失败。CLI 运行时
// 没有 base_url；托管供应商可留空以使用官方端点，但两种 compatible 没有官方端点，
// 必须填写。
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
		id, kind, rest := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), parts[2]
		k := runtime.Kind(kind)
		rc := agentconfig.RuntimeConfig{ID: id, Kind: kind, BaseURL: strings.TrimSpace(rest)}
		if k.IsHosted() {
			fields := append(strings.SplitN(rest, ",", 3), "", "")
			rc.BaseURL, rc.APIKeyEnv = strings.TrimSpace(fields[0]), strings.TrimSpace(fields[1])
			rc.Models = agentconfig.ModelList(strings.Join(agentconfig.ModelList(fields[2]).IDs(), " "))
		}
		if id == "" || kind == "" || (rc.BaseURL == "" && !k.IsLocalCLI() && runtime.DefaultBaseURL(k) == "") {
			continue
		}
		out = append(out, rc)
	}
	return out
}

// formTemplate groups one shared form behind local navigation, without external assets.
// With JavaScript disabled, navigation links scroll to the visible groups.
//
// formTemplate 将同一表单按左侧菜单分组，不依赖外部资源。
// 禁用 JavaScript 时，导航链接滚动到保持可见的对应分组。
//
//go:embed templates/form.html
var formSource string

var formTemplate = template.Must(template.New("form").Funcs(template.FuncMap{"dict": templateValues, "json": jsonText, "stateText": runtimeStateText}).Parse(formSource))

// renderForm executes formTemplate. A template execution error only ever
// indicates a bug in formTemplate itself (every field is a plain string or
// bool), so it is logged rather than surfaced through the response, which
// has likely already been partially written by the time template.Execute
// can fail.
//
// renderForm 执行请求作用域的表单模板，并设置不缓存的 HTML 响应头。
// 模板执行失败只可能来自实现缺陷，不向响应写入内部错误。
func renderForm(w http.ResponseWriter, v formValues) {
	if v.Locale == "" {
		v.Locale = browserlang.Default
		v.Text = newPageText(v.Locale)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = formTemplate.Execute(w, v)
}

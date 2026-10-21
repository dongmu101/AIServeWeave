package configui

import (
	"net/http"
	"sort"

	"AIServeWeave/common/browserlang"
	"AIServeWeave/common/runtime"
)

// Sources supplies read-only snapshots of the running Agent to the setup page.
// Sources 为设置页面提供运行中 Agent 的只读快照。
type Sources struct {
	// Runtimes reports local runtime snapshots; nil means setup-only mode.
	// Runtimes 返回本地运行时快照；nil 表示仅配置模式。
	Runtimes func() []runtime.Snapshot
	// Gateway reports the active connection table, independently of edited settings.
	// Gateway 返回实际运行的连接表，与正在编辑的配置相互独立。
	Gateway func() GatewaySnapshot
}

// GatewaySnapshot describes the running tunnel manager without credentials.
// GatewaySnapshot 描述运行中的隧道管理器，不包含凭据。
type GatewaySnapshot struct {
	// Enabled reports whether tunnels are enabled in the running configuration.
	// Enabled 表示运行中的配置是否启用隧道。
	Enabled bool
	// Started reports whether the tunnel manager has been initialized.
	// Started 表示隧道管理器是否已完成初始化。
	Started bool
	// States maps Gateway endpoints to tunnel state names.
	// States 将 Gateway 地址映射到隧道状态名称。
	States map[string]string
}

type gatewayConnection struct {
	Endpoint string
	Label    string
	LabelKey string
	Tone     string
}

type gatewayView struct {
	Summary     string
	SummaryKey  string
	Text        pageText
	Tone        string
	Connected   int
	Total       int
	Connections []gatewayConnection
}

// currentGateway renders a single connection-table snapshot in endpoint order.
// currentGateway 按地址排序呈现一次连接表快照。
func currentGateway(source func() GatewaySnapshot, locales ...browserlang.Locale) (view gatewayView) {
	locale := browserlang.Default
	if len(locales) > 0 {
		locale = locales[0]
	}
	view = gatewayView{SummaryKey: "a0085", Tone: "neutral", Text: newPageText(locale)}
	defer func() {
		view.Summary = view.Text.Text(view.SummaryKey)
		for i := range view.Connections {
			view.Connections[i].Label = view.Text.Text(view.Connections[i].LabelKey)
		}
	}()
	if source == nil {
		return view
	}
	snapshot := source()
	if !snapshot.Enabled {
		view.SummaryKey = "a0086"
		return view
	}
	if !snapshot.Started {
		view.SummaryKey = "a0087"
		return view
	}
	for endpoint, state := range snapshot.States {
		connection := gatewayConnection{Endpoint: endpoint, LabelKey: "a0088", Tone: "neutral"}
		switch state {
		case "connected", "serving":
			connection.LabelKey, connection.Tone = "a0089", "online"
			view.Connected++
		case "connecting":
			connection.LabelKey, connection.Tone = "a0090", "pending"
		case "reconnecting":
			connection.LabelKey, connection.Tone = "a0091", "pending"
		case "draining":
			connection.LabelKey, connection.Tone = "a0092", "pending"
		case "failed":
			connection.LabelKey, connection.Tone = "a0093", "offline"
		case "retired":
			connection.LabelKey = "a0094"
		case "disconnected":
			connection.LabelKey, connection.Tone = "a0095", "offline"
		}
		view.Connections = append(view.Connections, connection)
	}
	sort.Slice(view.Connections, func(i, j int) bool { return view.Connections[i].Endpoint < view.Connections[j].Endpoint })
	view.Total = len(view.Connections)
	view.SummaryKey, view.Tone = "a0095", "offline"
	if view.Connected > 0 {
		view.SummaryKey, view.Tone = "a0089", "online"
		if view.Connected < view.Total {
			view.SummaryKey, view.Tone = "a0096", "pending"
		}
	}
	return view
}

// newGatewayHandler serves only the live connection fragment, without reading configuration.
// newGatewayHandler 仅提供实时连接片段，不读取配置文件。
func newGatewayHandler(source func() GatewaySnapshot) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		locale := requestLocale(r)
		w.Header().Set("Content-Language", string(locale))
		w.Header().Set("Vary", "Cookie, Accept-Language")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			return
		}
		_ = formTemplate.ExecuteTemplate(w, "gateway-status", currentGateway(source, locale))
	}
}

package comfyui

import (
	"context"
	"net/http"

	"github.com/coder/websocket"

	"AIServeWeave/common/runtime"
)

// wsReadLimit bounds a single inbound WebSocket frame. ComfyUI's "executed"
// events embed a node's full output list, which grows with the number of
// files a run produced, so the library's small default would drop the
// connection on an ordinary batch render.
const wsReadLimit = 4 << 20 // 4 MiB

// Dialer is the production runtime.WSDialer, backed by
// github.com/coder/websocket. It is the only place in this package that
// touches a third-party library; everything else works against the
// runtime.WSConn interface, which is what lets the adapter's tests run
// without a real server.
type Dialer struct {
	httpClient *http.Client
}

// NewDialer returns a WSDialer that performs the WebSocket handshake with
// httpClient, so the connection inherits the same transport, timeouts and
// TLS configuration as the adapter's HTTP calls. A nil httpClient uses the
// library's default.
func NewDialer(httpClient *http.Client) *Dialer {
	return &Dialer{httpClient: httpClient}
}

// Dial opens the event stream. The returned connection reads frames only;
// the adapter never writes to ComfyUI's WebSocket.
func (d *Dialer) Dial(ctx context.Context, url string, header http.Header) (runtime.WSConn, error) {
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPClient: d.httpClient,
		HTTPHeader: header,
	})
	if resp != nil && resp.Body != nil {
		// On success the library already detached the body; on failure it
		// leaves a small buffered copy for diagnostics. Closing whatever is
		// there keeps this call site independent of which case occurred.
		resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(wsReadLimit)
	return &wsConn{conn: conn}, nil
}

var _ runtime.WSDialer = (*Dialer)(nil)

// wsConn adapts a websocket.Conn to runtime.WSConn.
type wsConn struct {
	conn *websocket.Conn
}

func (c *wsConn) Read(ctx context.Context) (int, []byte, error) {
	messageType, data, err := c.conn.Read(ctx)
	return int(messageType), data, err
}

// Close tears the connection down immediately rather than performing the
// closing handshake: Close is called from shutdown and reconnect paths,
// where waiting on a server that may already be gone would stall them.
func (c *wsConn) Close() error {
	return c.conn.CloseNow()
}

var _ runtime.WSConn = (*wsConn)(nil)
